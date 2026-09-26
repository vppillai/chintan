package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 1 — transcribe
// ---------------------------------------------------------------------------

func (p *Pipeline) transcribe(ctx context.Context, tenantID string, capture *model.CaptureIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusTranscribing); err != nil {
		return err
	}

	// A presigned GET, not the bytes. Pulling the whole object into the Lambda
	// heap and re-POSTing it would make the heap — rather than the microphone —
	// the real cap on how long a recording can be.
	audioURL, err := p.cfg.Objects.PresignGet(ctx, capture.AudioKey, audioURLTTL)
	if err != nil {
		return fmt.Errorf("pipeline: presign audio: %w", err)
	}

	// Estimating from the recorder's own measurement keeps the reservation honest
	// before the provider has told us anything. Whatever it says afterwards
	// reconciles it.
	estimateSeconds := float64(capture.DurationMS) / 1000
	if estimateSeconds <= 0 {
		estimateSeconds = defaultAudioSecondsEstimate
	}

	language, err := p.transcriptionLanguage(ctx, tenantID, capture)
	if err != nil {
		return err
	}
	// "auto" is the setting's word for it; the provider's is an absent field.
	sent := language
	if sent == model.LanguageAuto {
		sent = ""
	}

	var result provider.Transcription
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.STTProvider,
		Model:    p.cfg.STTModel,
		Op:       meter.OpTranscribe,
		Usage:    meter.Quantities{meter.UnitAudioSeconds: estimateSeconds},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		// The deadline applies to the provider call only, as in routeOnce:
		// breaker.Do releases the reservation on the caller's context, which
		// is still live when this one has expired.
		stageCtx, cancel := context.WithTimeout(ctx, p.cfg.TranscribeTimeout)
		defer cancel()
		out, err := p.cfg.STT.Transcribe(stageCtx, provider.Audio{
			URL:         audioURL,
			ContentType: contentTypeForAudioKey(capture.AudioKey),
			Language:    sent,
		})
		if err != nil {
			return breaker.Result{}, err
		}
		result = out
		return breaker.Result{Usage: meter.Quantities{meter.UnitAudioSeconds: out.Duration}}, nil
	})
	if err != nil {
		return p.handleProviderError(ctx, capture, "transcribe", err)
	}

	rawKey, err := keys.CaptureRaw(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: raw key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, rawKey, []byte(result.Text), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store raw text: %w", err)
	}

	segmentsKey := ""
	if len(result.Segments) > 0 || len(result.Words) > 0 {
		encoded, err := json.Marshal(newTranscriptDocument(result))
		if err != nil {
			return fmt.Errorf("pipeline: encode segments: %w", err)
		}
		segmentsKey, err = keys.CaptureSegments(tenantID, capture.ID)
		if err != nil {
			return fmt.Errorf("pipeline: segments key: %w", err)
		}
		if err := p.cfg.Objects.Put(ctx, segmentsKey, encoded, "application/json"); err != nil {
			return fmt.Errorf("pipeline: store segments: %w", err)
		}
	}

	// Shape, never content. The shape is taken on its own line so the
	// log-hygiene check, which reads an emitter's argument list for anything
	// that names user content, sees only the summary being logged.
	rawShape := obs.Redact(result.Text)
	// Both languages are metadata, not content: the code this worker sent and
	// the name Whisper answered with. Sixteen days of prod logs could not say
	// whether Malayalam was being transcribed as Tamil until these two fields
	// existed (review 2026-09-21, T10).
	obs.Log(ctx).Info("transcribed capture",
		slog.String("capture_id", capture.ID),
		slog.Int64("duration_ms", result.DurationMS()),
		slog.Int("segments", len(result.Segments)),
		slog.String("language_sent", language),
		slog.String("language_detected", result.Language),
		slog.Any("raw", rawShape))
	obs.Count(ctx, "TranscribedLanguage", map[string]string{"Outcome": languageOutcome(sent, result.Language)})

	capture.RawKey = rawKey
	capture.Language = language
	capture.LanguageDetected = result.Language
	if segmentsKey != "" {
		capture.SegmentsKey = segmentsKey
	}
	if ms := result.DurationMS(); ms > 0 {
		capture.DurationMS = ms
	}
	capture.Status = model.StatusTranscribed
	capture.Error = ""
	return p.persist(ctx, capture)
}

// transcriptionLanguage decides what the recording is transcribed in: the
// destination note's language when the capture has a note, else the tenant's
// default. It returns model.LanguageAuto or a code — the setting's own words,
// which is what the capture records; the caller turns "auto" into the absent
// field the provider reads it as.
//
// Routing runs AFTER transcription — the router reads the transcript to pick
// the note — so a capture without note_id is transcribed in the default
// first. run() compares that with the note the router lands on and, when the
// note asks for another language, comes back here with the note set and
// transcribes once more (review 2026-09-21, T2).
//
// A note or settings read that fails is a retryable fault, not a reason to
// guess: guessing English for a Tamil note and appending the result is the
// failure this setting exists to prevent.
func (p *Pipeline) transcriptionLanguage(ctx context.Context, tenantID string, capture *model.CaptureIndex) (string, error) {
	if capture.RequestedLanguage != "" {
		// A person chose, for this recording; nothing outranks that.
		return capture.RequestedLanguage, nil
	}
	language := ""
	if capture.NoteID != "" {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, capture.NoteID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return "", fmt.Errorf("pipeline: get target note for language: %w", err)
		}
		if err == nil {
			language = note.Language
		}
	}
	if language == "" {
		settings, err := p.cfg.Store.GetSettings(ctx, tenantID)
		if err != nil {
			return "", fmt.Errorf("pipeline: get settings for language: %w", err)
		}
		language = settings.DefaultLanguage
	}
	if language == "" {
		language = model.DefaultLanguage
	}
	return language, nil
}

// languageOutcome is the Outcome dimension of TranscribedLanguage: whether the
// language Whisper detected agrees with the code it was told. Four fixed
// values, because a dimension is a metric identity and is billed as one; the
// language itself is in the log line, not in a dimension. A detected name
// outside the code table is a mismatch like any other — a four-second clip
// sent as English and detected as Icelandic is exactly what the metric is
// for, not an unknown.
func languageOutcome(sent, detected string) string {
	switch {
	case detected == "":
		return "undetected"
	case sent == "":
		return "auto"
	case model.LanguageCode(detected) == sent:
		return "match"
	default:
		return "mismatch"
	}
}

// defaultAudioSecondsEstimate is what a transcription is reserved against when
// the client did not report a duration. Deliberately generous: under-reserving
// is how a cap gets crossed without ever being enforced.
const defaultAudioSecondsEstimate = 300

// transcriptDocument is what segments.json holds.
//
// Times are milliseconds because the player seeks in milliseconds and a float
// second is a rounding argument waiting to happen.
type transcriptDocument struct {
	Version    int              `json:"version"`
	Language   string           `json:"language,omitempty"`
	DurationMS int64            `json:"duration_ms"`
	Segments   []transcriptSpan `json:"segments"`
	Words      []transcriptSpan `json:"words,omitempty"`
}

type transcriptSpan struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

func newTranscriptDocument(t provider.Transcription) transcriptDocument {
	doc := transcriptDocument{
		Version:    1,
		Language:   t.Language,
		DurationMS: t.DurationMS(),
		Segments:   make([]transcriptSpan, 0, len(t.Segments)),
	}
	for _, s := range t.Segments {
		doc.Segments = append(doc.Segments, transcriptSpan{
			StartMS: seconds(s.Start), EndMS: seconds(s.End), Text: s.Text,
		})
	}
	for _, w := range t.Words {
		doc.Words = append(doc.Words, transcriptSpan{
			StartMS: seconds(w.Start), EndMS: seconds(w.End), Text: w.Word,
		})
	}
	return doc
}

func seconds(v float64) int64 {
	if v <= 0 {
		return 0
	}
	return int64(v*1000 + 0.5)
}

func contentTypeForAudioKey(audioKey string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(audioKey), "."))
	switch ext {
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "ogg":
		return "audio/ogg"
	case "webm":
		return "audio/webm"
	case "wav":
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}
