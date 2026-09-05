// Package pipeline runs the slow half of a capture: transcribe, route, clean,
// append.
//
// It lives out of band, in a Lambda that S3 invokes directly when a recording
// lands, for one reason. API Gateway's HTTP API caps an integration at 30
// seconds and the cap is not adjustable, so any capture whose speech-to-text
// plus LLM work exceeds that returned 504 to the user while the Lambda kept
// running and billing — and the client's retry then appended the same text a
// second time. For a driving-length recording that was the common case, not an
// edge case.
//
// The transport is Lambda's own asynchronous invocation: S3 ObjectCreated
// invokes the worker, and the API's retry and target endpoints invoke it with
// InvocationType Event. A returned error is retried twice, about a minute and
// then about two minutes later; an invocation that fails all three lands in the
// dead-letter queue and raises the alarm. There is no queue in front of the
// worker any more, and no visibility timeout to keep in step with the Lambda
// timeout and the append claim lease.
//
// Two properties hold throughout and are worth stating before the code:
//
//   - Every provider call goes through breaker.Do. There is no path to a
//     provider that skips the reservation against the instance's daily spend
//     counter, and none that skips the usage log line the breaker writes.
//   - Every stage persists its status and its artifact before the next begins,
//     so a failure resumes from the last good stage instead of re-transcribing
//     twenty minutes of audio.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/routing"
	"github.com/vppillai/chintan/backend/internal/service"
)

const (
	// routeConfidenceThreshold is how sure the router must be before appending to
	// an existing note without asking. Below it, the user confirms first.
	routeConfidenceThreshold = 0.75

	// maxRouteCandidates bounds the note list handed to the router: the most
	// recently touched fifty. Both stores list notes in that order over the
	// whole partition (repository.MaxNotesDrained), so the first page of the
	// list IS the window; there is no separate pool to drain and cut.
	maxRouteCandidates = 50

	// maxAppendAttempts bounds the ETag-conditional retry when a note body is
	// being written concurrently.
	maxAppendAttempts = 5

	// maxIndexRefreshAttempts bounds the optimistic-concurrency retry on the note
	// index after an append.
	maxIndexRefreshAttempts = 5

	// audioURLTTL is how long the presigned GET handed to the speech provider
	// stays valid. It has to outlast a long transcription without becoming a
	// standing grant.
	audioURLTTL = 60 * time.Minute

	// defaultRouteAttemptTimeout bounds one routing call. The 2026-09-04 log
	// review found 2 of 19 routing calls queued at the provider for ~60 s while
	// the other 17 took 0.4-5.8 s on the same prompt shape; a routing answer
	// that has not arrived in fifteen seconds is far likelier to be stuck in
	// that queue than to be about to arrive. Routing is a convenience with a
	// fallback (a new, auto-titled note), so the cap trades a rare mis-filing
	// for never making the user watch "routing" for a minute.
	defaultRouteAttemptTimeout = 15 * time.Second

	// routeAttempts is how many times the router is asked before the fallback.
	// Two: a stall or an overloaded provider clears more often than not on
	// the second try, and a third would cost another timeout's worth of the
	// user's patience for a diminishing return.
	routeAttempts = 2

	// Per-stage deadlines on the two long provider calls. The HTTP clients
	// carry an 840 s timeout as the outer bound — just under the worker's 900 s
	// Lambda limit — and before these existed that was the ONLY bound, so one
	// stalled transcription held the invocation for fourteen minutes and then
	// died with it, retried, and could do it twice more. Each stage now gets
	// what it plausibly needs and no more; the rest of the Lambda's time is
	// left for the stages after it and for the retry protocol. A deadline
	// that fires is an infrastructure fault, classified retryable by
	// handleProviderError, and the retry resumes at the stage whose artefact
	// is missing — a transcript already in S3 is not transcribed again. See
	// docs/design/pipeline-deadlines.md.
	//
	// Transcription: a twenty-minute recording comes back from Whisper turbo
	// in well under a minute, so five minutes is several times the worst case
	// this pipeline accepts (service.MaxCaptureBytes) and still leaves nine
	// minutes of Lambda for what follows.
	defaultTranscribeTimeout = 5 * time.Minute
	// Cleanup rewrites one dictation; the output is about the size of the
	// input, a few thousand tokens at most. Two minutes is generous.
	defaultCleanupTimeout = 2 * time.Minute
	// The whole-note clean reads up to 150 KB and writes up to 200 KB
	// (model.MaxCleanNoteInputBytes, MaxCleanedBodyBytes), a longer completion
	// than cleanup by an order of magnitude, so it gets a minute more. The
	// number lives in service because the request path's in-flight guard is
	// the same duration: a stamped request younger than this may still be
	// running.
	defaultCleanNoteTimeout = service.CleanNoteTimeout
)

// NoteCreator creates the destination note for a capture that has none.
type NoteCreator interface {
	CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error)
}

// NoteCleanInvoker is the slice of service.Invoker the pipeline needs to queue
// a clean-note task for itself.
type NoteCleanInvoker interface {
	InvokeCleanNote(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode) error
}

// Config is everything the pipeline needs. Breaker is not optional: a nil one
// would be a path to a provider that skips the spend check.
type Config struct {
	Store   repository.Store
	Objects repository.Objects
	STT     provider.STT
	LLM     provider.LLM
	Router  provider.Router
	Notes   NoteCreator
	Breaker *breaker.Breaker
	// CleanInvoker hands a note with auto_clean back to the worker for its
	// cleaned view after an append (TaskCleanNote). Optional: without it the
	// view is regenerated inline, which is correct here — this is the worker,
	// not the request path — but couples the clean's failure to the capture's
	// invocation rather than giving it retries of its own.
	CleanInvoker NoteCleanInvoker

	// Provider and model names are what the price table is keyed on. They are
	// passed in rather than inferred so an instance can point at a different
	// endpoint without the cost record quietly becoming wrong.
	STTProvider string
	STTModel    string
	LLMProvider string
	LLMModel    string

	// RouteAttemptTimeout bounds each of the routeAttempts routing calls. Zero
	// means defaultRouteAttemptTimeout; tests shorten it.
	RouteAttemptTimeout time.Duration
	// AskAttemptTimeout bounds the first model call of an ask task; the one
	// retry gets askRetryShare of it. Zero means defaultAskAttemptTimeout;
	// tests shorten it.
	AskAttemptTimeout time.Duration
	// TranscribeTimeout, CleanupTimeout and CleanNoteTimeout bound the
	// transcription, per-capture cleanup and whole-note clean provider calls.
	// Zero means the default beside each; tests shorten them.
	TranscribeTimeout time.Duration
	CleanupTimeout    time.Duration
	CleanNoteTimeout  time.Duration

	Now func() time.Time
}

// Pipeline processes one capture at a time.
type Pipeline struct {
	cfg Config
	now func() time.Time
}

// New validates the configuration and builds a pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil:
		return nil, fmt.Errorf("pipeline: store is required")
	case cfg.Objects == nil:
		return nil, fmt.Errorf("pipeline: objects are required")
	case cfg.STT == nil:
		return nil, fmt.Errorf("pipeline: stt provider is required")
	case cfg.LLM == nil:
		return nil, fmt.Errorf("pipeline: llm provider is required")
	case cfg.Breaker == nil:
		// Without it there is a route to a paid API that neither meters nor caps.
		return nil, fmt.Errorf("pipeline: breaker is required")
	}
	if cfg.STTProvider == "" {
		cfg.STTProvider = "groq"
	}
	if cfg.LLMProvider == "" {
		cfg.LLMProvider = "openai"
	}
	if cfg.RouteAttemptTimeout <= 0 {
		cfg.RouteAttemptTimeout = defaultRouteAttemptTimeout
	}
	if cfg.AskAttemptTimeout <= 0 {
		cfg.AskAttemptTimeout = defaultAskAttemptTimeout
	}
	if cfg.TranscribeTimeout <= 0 {
		cfg.TranscribeTimeout = defaultTranscribeTimeout
	}
	if cfg.CleanupTimeout <= 0 {
		cfg.CleanupTimeout = defaultCleanupTimeout
	}
	if cfg.CleanNoteTimeout <= 0 {
		cfg.CleanNoteTimeout = defaultCleanNoteTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Pipeline{cfg: cfg, now: now}, nil
}

// errDeliveryConceded means another delivery of the same capture owns the row,
// and this one stopped rather than fight it for the conditional write.
//
// It is not a failure. Asynchronous invocation is at-least-once, and the API's
// retry can arrive while an S3-triggered attempt is still running, so two
// deliveries of one capture is a normal event; treating the loser as an error
// would retry it, exhaust the retries, and put a dead-letter entry and an alarm
// in front of a human for a system working exactly as designed.
var errDeliveryConceded = errors.New("pipeline: another delivery owns this capture")

// errAppendClaimHeld means this delivery found its own append claim already
// taken and not finished, inside the lease, with no marker in the note. Unlike
// errDeliveryConceded it IS a reason to fail the invocation: the holder may be
// dead, and only the lease expiring can prove it. See append.
var errAppendClaimHeld = errors.New("pipeline: append claim held by an unfinished attempt")

// Run drives one capture as far as it can go and returns its final state.
//
// A returned error means the invocation should be retried by Lambda: it is an
// infrastructure fault, not a verdict on the capture. A capture that failed for
// its own reasons — a provider error, an exhausted spend cap, an undecidable
// destination — is persisted in that state and returned with a nil error, so the
// invocation is not retried to fail identically twice more before the DLQ. So
// is a capture another delivery is already carrying.
func (p *Pipeline) Run(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
	return p.runCapture(ctx, tenantID, captureID, 0)
}

// RunUpload is Run for the S3 notification that starts a capture: the same
// pipeline, plus the one fact only that notification carries — how many bytes
// were actually written — stamped on the row so GET /v1/usage can sum what a
// tenant's recordings occupy. The request-time size_bytes is the client's
// claim; this is the measurement.
func (p *Pipeline) RunUpload(ctx context.Context, ref CaptureRef) (model.CaptureIndex, error) {
	return p.runCapture(ctx, ref.TenantID, ref.CaptureID, ref.SizeBytes)
}

func (p *Pipeline) runCapture(ctx context.Context, tenantID, captureID string, audioBytes int64) (model.CaptureIndex, error) {
	ctx = obs.WithTenant(ctx, tenantID)
	log := obs.Log(ctx).With(slog.String("capture_id", captureID))

	capture, err := p.cfg.Store.GetCapture(ctx, tenantID, captureID)
	if err != nil {
		return model.CaptureIndex{}, fmt.Errorf("pipeline: get capture: %w", err)
	}

	if service.CaptureIsTerminal(capture.Status) {
		log.Info("capture already finished; nothing to do", slog.String("status", string(capture.Status)))
		return capture, nil
	}
	if audioBytes > 0 && capture.AudioBytes == 0 {
		// Carried to the row by the first stage's own write; it earns no
		// write of its own.
		capture.AudioBytes = audioBytes
	}

	started := p.now()
	final, err := p.run(ctx, &capture)
	elapsed := p.now().Sub(started)

	// This carries the count as well as the timing: CloudWatch's SampleCount on
	// a duration metric is how many captures ended in that outcome. A separate
	// outcome counter alongside it would be a second billable metric per
	// dimension value telling us a number this one already holds.
	obs.Duration(ctx, "CapturePipelineDuration", elapsed, map[string]string{"Outcome": string(final.Status)})
	p.markAudioProcessedIfSafe(ctx, &final)
	if err == nil && service.CaptureIsTerminal(final.Status) {
		p.verifyPeaks(ctx, &final)
	}
	if errors.Is(err, errDeliveryConceded) {
		// The other delivery is either finished or still running. Either way this
		// one is done and must succeed, not be retried. If the owner dies
		// mid-flight its own invocation fails and is retried, and that retry
		// finishes the interrupted attempt — so conceding drops nothing.
		log.Info("capture is owned by a concurrent delivery; leaving it to that one",
			slog.String("status", string(final.Status)),
			slog.Bool("already_finished", service.CaptureIsTerminal(final.Status)))
		return final, nil
	}
	if err != nil {
		obs.Count(ctx, "CaptureStageFailures", map[string]string{"Stage": string(capture.Status)})
		log.Error("capture pipeline could not complete", slog.String("error", err.Error()))
		return final, err
	}
	log.Info("capture pipeline finished",
		slog.String("status", string(final.Status)),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()))
	return final, nil
}

// RejectOversizedCapture fails a capture whose uploaded object is larger than
// service.MaxCaptureBytes, and deletes the object.
//
// Both halves matter. Failing the capture keeps the bytes away from a provider
// billed by the audio second; deleting the object keeps them out of a versioned
// bucket whose only expiry rule, ExpireCaptureAudio, exists solely when the
// stack was deployed with a retention setting. An oversized upload that is
// merely refused is still an oversized upload being paid for every month.
//
// The delete comes first and is not conditional on the capture row existing: an
// object can outlive its row, and an object with no row is reachable by nothing
// else in this system.
//
// A nil return means the verdict is recorded and the invocation is done. An
// error means the *recording* failed, so the invocation should be retried.
func (p *Pipeline) RejectOversizedCapture(ctx context.Context, ref CaptureRef) error {
	ctx = obs.WithTenant(ctx, ref.TenantID)
	log := obs.Log(ctx).With(slog.String("capture_id", ref.CaptureID))

	log.Warn("refusing a recording larger than the capture limit",
		slog.Int64("size_bytes", ref.SizeBytes),
		slog.Int64("limit_bytes", service.MaxCaptureBytes))
	obs.Count(ctx, "CaptureRejectedOversize", map[string]string{"Stage": string(model.StatusUploaded)})

	capture, getErr := p.cfg.Store.GetCapture(ctx, ref.TenantID, ref.CaptureID)

	audioKey := ref.ObjectKey
	if getErr == nil && capture.AudioKey != "" {
		audioKey = capture.AudioKey
	}
	if audioKey != "" {
		if err := p.cfg.Objects.Delete(ctx, audioKey); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("pipeline: delete oversized audio: %w", err)
		}
	}

	if errors.Is(getErr, repository.ErrNotFound) {
		// No row to mark. The object is gone, which is the part that costs money.
		log.Warn("oversized object had no capture row; deleted the object only")
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("pipeline: get capture: %w", getErr)
	}
	if service.CaptureIsTerminal(capture.Status) {
		return nil
	}

	capture.Status = model.StatusFailed
	capture.Error = fmt.Sprintf("recording is too large: %d bytes, limit %d bytes",
		ref.SizeBytes, service.MaxCaptureBytes)
	if err := p.persist(ctx, &capture); err != nil {
		if errors.Is(err, errDeliveryConceded) {
			// Another delivery owns the row. The object is deleted either way.
			return nil
		}
		return err
	}
	return nil
}

func (p *Pipeline) run(ctx context.Context, capture *model.CaptureIndex) (model.CaptureIndex, error) {
	tenantID := capture.UserID

	if capture.RawKey == "" {
		if err := p.transcribe(ctx, tenantID, capture); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	}

	if capture.NoteID == "" {
		if capture.Status == model.StatusNeedsTarget {
			// Nothing is written until the user picks a destination.
			return *capture, nil
		}
		if err := p.route(ctx, tenantID, capture); err != nil {
			return *capture, err
		}
		if capture.NoteID == "" {
			return *capture, nil
		}
	}

	note, err := p.cfg.Store.GetNote(ctx, tenantID, capture.NoteID)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: get note: %w", err)
	}
	if !service.NoteIsActive(note) {
		return *capture, p.markFailed(ctx, capture, service.ErrNoteArchived.Error())
	}

	if capture.RoutedKey == "" && capture.CleanKey == "" {
		// Recorded into a note, so routing — and with it the removal of the
		// words addressed to the app — was skipped.
		if err := p.stripInstructions(ctx, tenantID, capture, note); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	}

	if capture.CleanKey == "" {
		if err := p.clean(ctx, tenantID, capture); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	}

	return p.append(ctx, tenantID, capture, note)
}

// stripInstructions removes a spoken app instruction from a capture that was
// recorded into a note and so never reached routing.
//
// Routing deletes the words addressed to the app — "add this to my roof
// note", "create a note with the title …" — before the dictation is cleaned
// and appended, but a capture that already has its destination skips routing,
// and the same words were appended verbatim (QA 2026-09-05 §5a). This is the
// same span-extraction call, with the target note as the only candidate and
// the destination it answers ignored: one small routing-priced call for a
// targeted capture whose transcript contains an instruction cue, and no call
// for one that does not (routing.MentionsInstruction). The result is stored as
// the routed text, so a retry after a later fault finds it and does not call
// again.
//
// The call is a convenience, as routing is: its own failure keeps the words as
// spoken; a spend cap stops the capture as it would at the routing stage; and
// only a store or object-store fault fails the invocation.
func (p *Pipeline) stripInstructions(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) error {
	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)
	if p.cfg.Router == nil || !routing.MentionsInstruction(transcript) {
		obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": "no_cue"})
		return nil
	}

	candidates := []routing.Candidate{{NoteID: note.ID, Title: note.Title, Aliases: note.Aliases}}
	decision, err := p.routeWithRetries(ctx, tenantID, capture.ID, transcript, candidates)
	if err != nil {
		if errors.Is(err, breaker.ErrSpendCapExceeded) {
			return p.handleProviderError(ctx, capture, "route", err)
		}
		if ctx.Err() != nil {
			return err
		}
		obs.Log(ctx).Warn("instruction check failed; keeping the dictation as spoken",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
		obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": "failed"})
		return nil
	}

	routedKey, err := keys.CaptureRouted(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: routed key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, routedKey, []byte(decision.Content), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store routed text: %w", err)
	}
	capture.RoutedKey = routedKey
	outcome := "nothing_removed"
	if decision.Content != transcript {
		outcome = "removed"
	}
	obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": outcome})
	return nil
}

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
			Language:    language,
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

	obs.Log(ctx).Info("transcribed capture",
		slog.String("capture_id", capture.ID),
		slog.Int64("duration_ms", result.DurationMS()),
		slog.Int("segments", len(result.Segments)),
		// Shape, never content.
		slog.Any("raw", obs.Redact(result.Text)))

	capture.RawKey = rawKey
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

// transcriptionLanguage decides what the speech provider is told the recording
// is in: the target note's language when the capture was started with one,
// else the tenant's default, with "auto" becoming "" (send no language).
//
// The note wins only when it is already known. Routing runs AFTER
// transcription — the router reads the transcript to pick the note — so a
// capture without note_id cannot be transcribed in the language of a note
// nobody has chosen yet, and gets the tenant's default. That is the honest
// limit of a per-note setting on this pipeline, and it is why the setting
// also exists per tenant.
//
// A note or settings read that fails is a retryable fault, not a reason to
// guess: guessing English for a Tamil note and appending the result is the
// failure this setting exists to prevent.
func (p *Pipeline) transcriptionLanguage(ctx context.Context, tenantID string, capture *model.CaptureIndex) (string, error) {
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
	if language == model.LanguageAuto {
		return "", nil
	}
	return language, nil
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

// ---------------------------------------------------------------------------
// Stage 2 — route
// ---------------------------------------------------------------------------

func (p *Pipeline) route(ctx context.Context, tenantID string, capture *model.CaptureIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusRouting); err != nil {
		return err
	}

	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)

	decision, err := p.decideTarget(ctx, tenantID, capture.ID, transcript)
	if err != nil {
		if errors.Is(err, breaker.ErrSpendCapExceeded) {
			return p.handleProviderError(ctx, capture, "route", err)
		}
		if errors.Is(err, errRouteCandidates) {
			// The router was never asked. Filing the dictation into a new note
			// here would be the fault the GetNote branch below describes — a
			// DynamoDB throttle starting a second note on the subject the user
			// has been dictating into all week — one step earlier. The
			// invocation is worth retrying; a duplicate note is not.
			return err
		}
		// Routing is a convenience; a recording is never lost because of it.
		// From here the failure is the router's own — a stall past both
		// attempts, a 5xx, an answer that would not parse — and nothing the
		// store said is being second-guessed.
		obs.Log(ctx).Warn("routing failed; keeping the dictation in a new note",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
		decision = provider.RouteDecision{Action: provider.RouteNew, Content: transcript}
	}

	// Persist the transcript minus any spoken instruction, so cleanup and any
	// later retry work from the words the user meant to keep.
	routedKey, err := keys.CaptureRouted(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: routed key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, routedKey, []byte(decision.Content), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store routed text: %w", err)
	}
	capture.RoutedKey = routedKey
	capture.RouteConfidence = decision.Confidence

	if decision.Action == provider.RouteAppend && decision.NoteID != "" {
		// Falling through to "make a new note" is only correct for an answer, not
		// for a failure to get one. A throttle or a 5xx on this read used to be
		// indistinguishable from ErrNotFound, so a transient DynamoDB fault
		// started a second note on the subject the user has been dictating into
		// all week — silently, unretried, and with the two halves of the thought
		// now in different notes. The invocation is worth retrying; a duplicate
		// note is not worth creating.
		note, err := p.cfg.Store.GetNote(ctx, tenantID, decision.NoteID)
		switch {
		case err == nil && service.NoteIsActive(note):
			if decision.Confidence >= routeConfidenceThreshold {
				capture.NoteID = decision.NoteID
				capture.TargetSource = model.TargetSourceRouter
				capture.Status = model.StatusTranscribed
			} else {
				// Plausible but unsure: ask before writing into an existing note.
				capture.SuggestedNoteID = decision.NoteID
				capture.Status = model.StatusNeedsTarget
			}
			return p.persist(ctx, capture)
		case err == nil:
			obs.Log(ctx).Info("routed note is archived; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
		case errors.Is(err, repository.ErrNotFound):
			obs.Log(ctx).Info("routed note no longer exists; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
		default:
			return fmt.Errorf("pipeline: get routed note %s: %w", decision.NoteID, err)
		}
	}

	title := service.SanitizeTitle(decision.Title)
	if title == "" {
		title = fallbackNoteTitle(p.now())
	}
	if p.cfg.Notes == nil {
		capture.SuggestedTitle = title
		capture.Status = model.StatusNeedsTarget
		return p.persist(ctx, capture)
	}

	note, err := p.cfg.Notes.CreateNote(ctx, tenantID, title, nil)
	if err != nil {
		return fmt.Errorf("pipeline: create note for capture: %w", err)
	}
	capture.NoteID = note.ID
	capture.TargetSource = model.TargetSourceRouter
	capture.Status = model.StatusTranscribed
	return p.persist(ctx, capture)
}

// errRouteCandidates marks a routing failure that happened before the router
// was asked: the store would not list the notes it chooses among. It is the
// one routing error route() does not turn into a new note.
var errRouteCandidates = errors.New("pipeline: list routing candidates")

func (p *Pipeline) decideTarget(ctx context.Context, tenantID, captureID, transcript string) (provider.RouteDecision, error) {
	if p.cfg.Router == nil {
		return provider.RouteDecision{}, fmt.Errorf("pipeline: routing is not configured")
	}

	// The store orders the list most recently touched first over every note
	// the tenant has, so the leading maxRouteCandidates are the window the
	// router should see — the likeliest destinations. Until 2026-09 this
	// drained a 500-note pool and cut it here, which was only right while the
	// store's own order was by creation.
	active, err := repository.DrainPages(ctx, maxRouteCandidates, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return p.cfg.Store.ListNotes(ctx, tenantID, opts)
	})
	if err != nil {
		return provider.RouteDecision{}, fmt.Errorf("%w: %w", errRouteCandidates, err)
	}

	// Kept as a guard on the order the store promised, and because it is the
	// place this lesson lives: compare parsed instants, never RFC3339Nano
	// strings. Go trims trailing fractional zeros, so "…:00Z" sorts above
	// "…:00.1Z" because 'Z' > '.' and the router would be handed the wrong
	// fifty notes.
	sort.SliceStable(active, func(i, j int) bool {
		return noteTouchedAt(active[i]).After(noteTouchedAt(active[j]))
	})
	if len(active) > maxRouteCandidates {
		active = active[:maxRouteCandidates]
	}

	candidates := make([]routing.Candidate, 0, len(active))
	for _, n := range active {
		candidates = append(candidates, routing.Candidate{
			NoteID:  n.ID,
			Title:   n.Title,
			Aliases: n.Aliases,
		})
	}

	decision, err := p.routeWithRetries(ctx, tenantID, captureID, transcript, candidates)
	if err != nil {
		return decision, err
	}
	return preferExistingTitle(ctx, decision, active), nil
}

// preferExistingTitle applies the rule the prompt already states — an existing
// note with the same title is the destination — after the model has answered.
// A "new" decision whose title names an active candidate, by title or alias
// and compared case- and whitespace-insensitively, appends to that note. The
// model started a second "staging smoke" beside the one that existed (live QA
// 2026-09-05 §5b), and a rule this mechanical is the code's to enforce, not the
// model's to remember. The candidates are the notes the router saw, so the
// rule reaches exactly as far as routing does; the id, never the title, is
// what gets logged.
func preferExistingTitle(ctx context.Context, decision provider.RouteDecision, active []model.NoteIndex) provider.RouteDecision {
	if decision.Action != provider.RouteNew {
		return decision
	}
	want := normalizeTitle(decision.Title)
	if want == "" {
		return decision
	}
	for _, n := range active {
		if !titleNames(n, want) {
			continue
		}
		obs.Log(ctx).Info("router chose a new note whose title names an existing note; appending to it instead",
			slog.String("note_id", n.ID))
		obs.Count(ctx, "RouterTitleMatchedExistingNote", map[string]string{})
		decision.Action = provider.RouteAppend
		decision.NoteID = n.ID
		decision.Title = ""
		decision.Confidence = 1
		return decision
	}
	return decision
}

// titleNames reports whether want, already normalised, is n's title or one of
// its aliases.
func titleNames(n model.NoteIndex, want string) bool {
	if normalizeTitle(n.Title) == want {
		return true
	}
	for _, alias := range n.Aliases {
		if normalizeTitle(alias) == want {
			return true
		}
	}
	return false
}

// normalizeTitle is the comparison form of a title: lowercased, one space
// between words, none around them.
func normalizeTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// routeWithRetries asks the router, with one retry on a stall or a 5xx. It
// is the model half of decideTarget, shared with stripInstructions, which
// wants the instruction spans and not the destination.
func (p *Pipeline) routeWithRetries(ctx context.Context, tenantID, captureID, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	// Each attempt is its own breaker.Do, so each reserves before it calls and
	// settles for itself. An attempt that fails — a timeout included — reports
	// no usage, and the breaker releases exactly what that attempt reserved,
	// so two stalls cost the day's budget nothing and a retry that succeeds is
	// charged once, for what the provider said it consumed. Wrapping both
	// attempts in one reservation would have charged the estimate for a call
	// that never answered, or left the breaker's latency metric summing two
	// calls into one.
	var lastErr error
	for attempt := 1; attempt <= routeAttempts; attempt++ {
		decision, err := p.routeOnce(ctx, tenantID, transcript, candidates)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// The invocation itself is ending; this is not a provider stall to
			// retry, and a fresh context would only borrow time the worker no
			// longer has.
			return provider.RouteDecision{}, err
		}
		reason, retryable := routeRetryReason(err)
		if !retryable {
			return provider.RouteDecision{}, err
		}
		if reason == routeRetryTimeout {
			obs.Count(ctx, "RouterTimedOut", map[string]string{"Attempt": strconv.Itoa(attempt)})
		}
		if attempt == routeAttempts {
			break
		}
		// The correlation id rides on the context (obs.Log), so this line joins
		// the API request that started the capture to the retry it caused.
		obs.Log(ctx).Warn("routing attempt failed; retrying once with a fresh context",
			slog.String("capture_id", captureID),
			slog.Int("attempt", attempt),
			slog.String("reason", reason),
			slog.Int64("attempt_timeout_ms", p.cfg.RouteAttemptTimeout.Milliseconds()),
			slog.String("error", err.Error()))
		obs.Count(ctx, "RouterRetried", map[string]string{"Reason": reason})
	}
	return provider.RouteDecision{}, lastErr
}

// routeOnce is one reserved, bounded routing call.
//
// The attempt's deadline applies to the provider call only. breaker.Do runs on
// the caller's context, so when the attempt times out the release of its
// reservation still has a live context to run on; a release attempted on the
// expired context would fail, and the estimate would stay in the day's total
// as spend that never happened.
func (p *Pipeline) routeOnce(ctx context.Context, tenantID, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	var decision provider.RouteDecision
	_, err := p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpRoute,
		Usage: meter.Quantities{
			meter.UnitInputTokens:  estimateTokens(transcript) + estimateCandidateTokens(candidates),
			meter.UnitOutputTokens: routeOutputTokensEstimate,
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, p.cfg.RouteAttemptTimeout)
		defer cancel()
		out, err := p.cfg.Router.Route(attemptCtx, transcript, candidates)
		if err != nil {
			return breaker.Result{}, err
		}
		decision = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return provider.RouteDecision{}, err
	}
	return decision, nil
}

// Values of the Reason dimension on RouterRetried. Two values, fixed: a
// dimension is a metric identity and is billed as one.
const (
	routeRetryTimeout     = "timeout"
	routeRetryServerError = "provider_5xx"
)

// routeRetryReason classifies a failed routing attempt as worth one more try.
//
// Only two things are: the attempt hitting its own deadline (the provider-side
// queueing the timeout exists for) and a 5xx or 529 from the provider (an
// overloaded moment). A 4xx is our request and will fail identically; a spend
// cap rejection must not be retried around; an unparseable answer or an
// unlisted note id is the model's verdict, and the fallback is the right
// answer to it. The caller has already ruled out its own context ending, so a
// deadline seen here is the attempt's.
func routeRetryReason(err error) (string, bool) {
	switch {
	case errors.Is(err, breaker.ErrSpendCapExceeded):
		return "", false
	case errors.Is(err, context.DeadlineExceeded):
		return routeRetryTimeout, true
	case provider.IsServerError(err):
		return routeRetryServerError, true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------------
// Stage 3 — clean
// ---------------------------------------------------------------------------

func (p *Pipeline) clean(ctx context.Context, tenantID string, capture *model.CaptureIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusCleaning); err != nil {
		return err
	}

	sourceKey := capture.RoutedKey
	if sourceKey == "" {
		sourceKey = capture.RawKey
	}
	sourceBytes, err := p.cfg.Objects.Get(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("pipeline: get source text: %w", err)
	}
	source := string(sourceBytes)

	if strings.TrimSpace(source) == "" {
		// The speaker only told the app what to do, so the note they asked for
		// exists and there is nothing to clean or append.
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return p.persist(ctx, capture)
	}

	var cleaned provider.Cleaned
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpCleanup,
		Usage: meter.Quantities{
			meter.UnitInputTokens: estimateTokens(source),
			// Cleanup rewrites the transcript, so it writes about as much as
			// it reads. Reserving for the output too is what keeps the
			// reservation near the bill: output tokens cost four times input.
			meter.UnitOutputTokens: estimateTokens(source),
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		stageCtx, cancel := context.WithTimeout(ctx, p.cfg.CleanupTimeout)
		defer cancel()
		out, err := p.cfg.LLM.Cleanup(stageCtx, capture.Mode, source)
		if err != nil {
			return breaker.Result{}, err
		}
		cleaned = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return p.handleProviderError(ctx, capture, "cleanup", err)
	}

	cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: clean key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(cleaned.Text), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store clean text: %w", err)
	}

	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return p.persist(ctx, capture)
}

// ---------------------------------------------------------------------------
// Stage 4 — append
// ---------------------------------------------------------------------------

func (p *Pipeline) append(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) (model.CaptureIndex, error) {
	if err := p.setStatus(ctx, capture, service.StatusAppending); err != nil {
		return *capture, err
	}

	cleanBytes, err := p.cfg.Objects.Get(ctx, capture.CleanKey)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: get clean text: %w", err)
	}
	cleanedText := string(cleanBytes)

	// The append is the one step that must happen exactly once. Append, index
	// update and status flip are three writes with nothing tying them together:
	// a failure after the append leaves the capture in `cleaned`, and an
	// unguarded retry re-appends the same text.
	//
	// Two things guard it, and they do different jobs. The claim is a
	// mutex: one attempt at a time writes to the note, and a holder that dies
	// releases it when AppendClaimLease runs out. The marker is the idempotency
	// guard: appendToNote writes "<!-- chintan:capture:<id> -->" into the body
	// in the same conditional PUT as the paragraph, so any later attempt can
	// ask the body the exact question "did this capture's text land?" rather
	// than trusting the lease arithmetic. The token is derived from the capture
	// and its cleaned artefact, so every attempt at the same work computes the
	// same value and can recognise its own earlier claim.
	token := appendToken(capture.ID, capture.CleanKey)

	// Read before claiming, because claiming overwrites it with our own token.
	//
	// A recorded token equal to the one we just computed cannot belong to
	// another writer: it is derived from this capture and this cleaned artefact,
	// so it is this same work, interrupted. Once the claim's lease expires that
	// interrupted attempt becomes takeable — legitimately, since the worker
	// holding it really is gone — and the takeover is where a claim-only guard
	// writes the paragraph a second time. Knowing the takeover is our own is
	// what lets appendToNote look for the marker and skip the write.
	resumingOwnAttempt := capture.AppendToken == token && capture.AppendedAt == 0

	claimed, current, err := p.cfg.Store.ClaimCaptureAppend(ctx, tenantID, capture.ID, token)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: claim capture append: %w", err)
	}
	if !claimed {
		*capture = current
		if current.AppendToken != token || current.AppendedAt != 0 {
			// Somebody else owns this append, or an earlier attempt finished
			// it. Either way this attempt must not write the text a second time.
			return current, nil
		}

		// Our own token, unfinished, inside the lease. Either the earlier
		// attempt is still running, or it died after taking the claim; from
		// here the two look the same, and the marker is what tells them apart.
		//
		// If the marker is in the note body the dangerous part is over,
		// whoever did it: finishing the bookkeeping is idempotent (the index
		// refresh re-derives from the body, the completion is a versioned write
		// on the same token), so do it now rather than wait for the lease. This
		// is the case a Lambda retry a minute later actually meets — the
		// paragraph was written and the worker died before marking the capture
		// appended.
		//
		// Otherwise fail the invocation. Conceding here would leave the capture
		// in `appending` with nothing left to finish it; appending would race a
		// holder that may still be about to write. Lambda's automatic retries
		// at about one and two minutes both fall inside the twenty-minute
		// lease, so an attempt that died between claiming and writing — a
		// window of one object read and one write — dead-letters and raises
		// the alarm, and the user's retry after the lease takes the claim over
		// and does the append once.
		if written, err := p.markerInNote(ctx, note.S3MarkdownKey, capture.ID); err != nil {
			return current, fmt.Errorf("pipeline: check note for interrupted append: %w", err)
		} else if written {
			obs.Log(ctx).Info("append claim is held but the capture's marker is already in the note; finishing the interrupted attempt",
				slog.String("note_id", note.ID))
			obs.Count(ctx, "AppendResumedWithoutRewriting", map[string]string{"Stage": string(service.StatusAppending)})
			return p.finishAppend(ctx, tenantID, capture, note, cleanedText, token)
		}
		return current, fmt.Errorf("pipeline: append claim for this capture is still held by an "+
			"unfinished attempt (claimed %s ago, lease %s): %w",
			time.Since(time.Unix(current.AppendClaimedAt, 0)).Round(time.Second),
			repository.AppendClaimLease, errAppendClaimHeld)
	}
	*capture = current

	if err := p.appendToNote(ctx, note.S3MarkdownKey, capture.ID, cleanedText, resumingOwnAttempt); err != nil {
		// Hand the claim back so a transient object-store failure does not park
		// the capture until the claim lease expires.
		p.releaseAppendClaim(ctx, capture)
		return *capture, fmt.Errorf("pipeline: append to note: %w", err)
	}

	return p.finishAppend(ctx, tenantID, capture, note, cleanedText, token)
}

// finishAppend is the bookkeeping after the text is durably in the note body:
// the index refresh and the completion of the claim. Both are safe to repeat,
// which is what lets a retry that finds the marker already written finish an
// attempt that died here.
func (p *Pipeline) finishAppend(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, cleanedText, token string) (model.CaptureIndex, error) {
	refreshed, err := p.refreshNoteIndex(ctx, tenantID, note.ID)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: refresh note index: %w", err)
	}

	appended, err := p.cfg.Store.CompleteCaptureAppend(ctx, tenantID, capture.ID, token)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: complete capture append: %w", err)
	}
	*capture = appended

	// Only now, with the capture marked appended: the cleaned view follows the
	// body, and the body is settled.
	p.autoCleanAfterAppend(ctx, tenantID, refreshed)
	return appended, nil
}

// markerInNote reports whether the note body carries captureID's append
// marker — the exact statement that this capture's paragraph has been written.
// It is the same test appendToNote applies when resuming.
func (p *Pipeline) markerInNote(ctx context.Context, noteKey, captureID string) (bool, error) {
	existing, err := p.cfg.Objects.Get(ctx, noteKey)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return service.HasCaptureMarker(string(existing), captureID), nil
}

// appendToken is deterministic so a retry of the same work recognises its own
// claim rather than treating it as somebody else's.
func appendToken(captureID, cleanKey string) string {
	sum := sha256.Sum256([]byte(captureID + "\x00" + cleanKey))
	return hex.EncodeToString(sum[:16])
}

func (p *Pipeline) releaseAppendClaim(ctx context.Context, capture *model.CaptureIndex) {
	released := *capture
	released.AppendToken = ""
	released.AppendClaimedAt = 0
	if updated, err := p.cfg.Store.PutCapture(ctx, released); err == nil {
		*capture = updated
	}
}

// appendToNote adds text to the end of a note body under a conditional write,
// preceded by the capture's marker.
//
// A bare read-concat-write with no concurrency control silently discards one of
// a voice append and an editor save that land together. Here the write carries
// the ETag that was read; a lost race re-reads and retries so both edits
// survive.
//
// The marker and the paragraph go into the body in one PUT, so there is no
// state in which one is present without the other. The API keeps the marker
// out of everything the user sees (service.StripCaptureMarkers) and puts it
// back on every save (service.CarryCaptureMarkers), so it survives edits.
//
// resuming says this call is a retry of an attempt that already held the claim
// for this exact capture and cleaned artefact. Only then is the marker a
// reason to do nothing — a first attempt that finds one has found a bug, not a
// shortcut, and appends so the dictation is not lost.
func (p *Pipeline) appendToNote(ctx context.Context, noteKey, captureID, text string, resuming bool) error {
	var lastErr error
	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		existingContent, etag, err := p.cfg.Objects.GetWithETag(ctx, noteKey)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			existingContent, etag = nil, ""
		case err != nil:
			return fmt.Errorf("pipeline: get existing note: %w", err)
		}

		if resuming && service.HasCaptureMarker(string(existingContent), captureID) {
			// The interrupted attempt got as far as the body. Nothing to write;
			// the caller goes on to finish the bookkeeping it never reached.
			obs.Log(ctx).Info("append already in the note body; finishing the interrupted attempt instead of repeating it",
				slog.String("note_key", noteKey))
			obs.Count(ctx, "AppendResumedWithoutRewriting", map[string]string{"Stage": string(service.StatusAppending)})
			return nil
		}

		newContent := service.CaptureMarker(captureID) + "\n" + text
		if len(existingContent) > 0 {
			newContent = string(existingContent) + "\n\n" + newContent
		}

		err = p.cfg.Objects.PutIfMatch(ctx, noteKey, []byte(newContent), "text/markdown", etag)
		if err == nil {
			return nil
		}
		if !errors.Is(err, repository.ErrPreconditionFailed) {
			return fmt.Errorf("pipeline: update note: %w", err)
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	return fmt.Errorf("pipeline: update note after %d attempts: %w", maxAppendAttempts, lastErr)
}

// refreshNoteIndex re-derives the snippet, search text, touch time and the
// cleaned view's stale flag from the note body that is now in object storage,
// and returns the note as stored. The body is authoritative, so a version
// conflict is resolved by re-reading rather than by overwriting whoever won.
//
// A body that cannot be read is an error, not a body to guess at. Until
// 2026-09 a failed read fell back to the paragraph just appended, so one S3
// 5xx or timeout at this moment rewrote the note's search text and snippet to
// that paragraph alone; the capture was then marked appended, nothing retried,
// and server search, the offline corpus and Ask lost the rest of the note
// until the next body write. Returning the error fails the invocation instead.
// The retry finds the capture's marker in the body and finishes the append
// from here, so nothing is written twice.
func (p *Pipeline) refreshNoteIndex(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	var lastErr error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
		if err != nil {
			return model.NoteIndex{}, err
		}
		existing, err := p.cfg.Objects.Get(ctx, note.S3MarkdownKey)
		if err != nil {
			return model.NoteIndex{}, fmt.Errorf("read note body: %w", err)
		}
		body := string(existing)
		note.Snippet = service.Snippet(body)
		note.SearchText = service.SearchText(body)
		note.UpdatedAt = model.FormatTime(p.now())
		// The paragraph just appended is not in the cleaned view.
		service.MarkCleanedStale(&note)

		if stored, err := p.cfg.Store.PutNote(ctx, tenantID, note); err == nil {
			return stored, nil
		} else if !errors.Is(err, repository.ErrVersionConflict) {
			return model.NoteIndex{}, err
		} else {
			lastErr = err
		}
	}
	return model.NoteIndex{}, lastErr
}

// ---------------------------------------------------------------------------
// Status bookkeeping
// ---------------------------------------------------------------------------

// setStatus persists an in-progress stage. The frontend's progress card polls
// exactly these values, so a stage that does not write one is a stage the user
// watches as a stall.
func (p *Pipeline) setStatus(ctx context.Context, capture *model.CaptureIndex, status model.CaptureStatus) error {
	capture.Status = status
	capture.Error = ""
	if err := p.persist(ctx, capture); err != nil {
		return err
	}
	obs.Count(ctx, "CaptureStageEntered", map[string]string{"Stage": string(status)})
	return nil
}

// persist writes the capture row under its optimistic-concurrency version.
//
// Losing that write is not a fault. The conditional write is load-bearing — it
// is what stops two writers silently discarding one another — so the answer to
// losing it is neither to drop the condition nor to retry until we win, both of
// which reintroduce the lost update it prevents. The answer is to concede.
func (p *Pipeline) persist(ctx context.Context, capture *model.CaptureIndex) error {
	updated, err := p.cfg.Store.PutCapture(ctx, *capture)
	if err == nil {
		*capture = updated
		return nil
	}
	if !errors.Is(err, repository.ErrVersionConflict) {
		return fmt.Errorf("pipeline: persist capture: %w", err)
	}
	return p.concede(ctx, capture)
}

// markAudioProcessedIfSafe lets the retention lifecycle rule see this
// capture's audio once it is no longer needed for the pipeline to make
// progress — which is the moment transcription succeeds and RawKey is set,
// not necessarily the moment the whole capture reaches a terminal status.
// Every stage after transcription resumes from RawKey (resumeStatusFor prefers
// it explicitly) and never re-reads the audio object, so protecting it any
// longer than this buys nothing.
//
// Before that point — the capture is still `uploaded`, or transcription
// itself failed — the audio is the only surviving evidence of what was said,
// and a retry needs that very object, so it must not expire regardless of
// age. This is what closes the gap a capture falls into when the upload event
// that should drive the worker never arrives: with no RawKey, this never
// tags it, so the lifecycle rule (which now requires the tag) leaves it alone
// indefinitely instead of deleting it out from under a delivery that has not
// happened yet.
//
// Failing to tag is logged and swallowed rather than propagated: the
// capture's own status write already succeeded, and erring toward keeping
// audio longer than necessary is the safe direction, not the one this exists
// to prevent.
func (p *Pipeline) markAudioProcessedIfSafe(ctx context.Context, capture *model.CaptureIndex) {
	if capture.RawKey == "" || capture.AudioKey == "" {
		return
	}
	if err := p.cfg.Objects.MarkProcessed(ctx, capture.AudioKey); err != nil {
		obs.Log(ctx).Warn("could not tag capture audio as processed",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
	}
}

// verifyPeaks makes the capture's peaks key mean what the API reports it as.
//
// POST /v1/captures records PeaksKey when it *issues* the presigned PUT for the
// client-computed waveform, and the API derives `has_peaks` from that key — so
// a client that never uploaded peaks (an old build, a failed best-effort PUT, a
// tab closed after the audio landed) was reported as having a waveform, and the
// note screen's request for it 404'd. The bucket is the only party that knows
// whether the object exists, and this is the one moment the worker is already
// running and the answer is almost certainly settled: the client PUTs peaks
// straight after the audio, and the pipeline has spent seconds on providers
// since the audio landed.
//
// A key that names nothing is cleared rather than annotated, for a reason that
// is not cosmetic: GSI1 projects `peaks_key` and cannot project a new attribute
// without an index rebuild, so any second flag would be invisible to every
// note-detail list. The key is derivable (keys.CapturePeaks) and the cascade
// delete removes the derived key regardless, so a peaks object that lands after
// this check is still cleaned up with its capture; it is merely not shown.
//
// Only a terminal capture is checked — a failed transcription can finish inside
// the second the client needs to upload peaks, and a retry would re-check
// anyway. Failing to check is logged and swallowed: the capture's own outcome
// is already recorded, and erring toward the old optimistic answer is the
// safe direction.
func (p *Pipeline) verifyPeaks(ctx context.Context, capture *model.CaptureIndex) {
	if capture.PeaksKey == "" {
		return
	}
	present, err := p.cfg.Objects.Exists(ctx, capture.PeaksKey)
	if err != nil {
		obs.Log(ctx).Warn("could not check for the capture's peaks object",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
		return
	}
	if present {
		return
	}
	obs.Log(ctx).Info("client uploaded no peaks for this capture; clearing the peaks key",
		slog.String("capture_id", capture.ID))
	obs.Count(ctx, "CapturePeaksMissing", map[string]string{"Stage": string(capture.Status)})
	capture.PeaksKey = ""
	if err := p.persist(ctx, capture); err != nil && !errors.Is(err, errDeliveryConceded) {
		obs.Log(ctx).Warn("could not clear the capture's peaks key",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
	}
}

// concede reloads the capture after a lost conditional write and stops this
// delivery.
//
// Whoever won holds a newer version than the copy this delivery is carrying, so
// every subsequent write here would lose too. Reloading first means the status
// this delivery reports is the truth rather than its own stale guess.
func (p *Pipeline) concede(ctx context.Context, capture *model.CaptureIndex) error {
	current, err := p.cfg.Store.GetCapture(ctx, capture.UserID, capture.ID)
	if err != nil {
		// Genuinely retryable: we know we lost, but not to what.
		return fmt.Errorf("pipeline: reload capture after a lost write: %w", err)
	}
	*capture = current

	// Info, not warn. A duplicate delivery is expected of an at-least-once
	// transport; the counter is here so that "expected" can be checked against
	// reality rather than assumed, because a sustained rate of these means
	// something is invoking the worker twice for every capture.
	obs.Log(ctx).Info("lost a conditional write to a concurrent delivery",
		slog.String("capture_id", current.ID),
		slog.String("status", string(current.Status)))
	obs.Count(ctx, "DuplicateDelivery", map[string]string{"Status": string(current.Status)})
	return errDeliveryConceded
}

// ErrProviderKeyRejected is the verdict recorded on a capture whose provider
// refused this instance's credential.
//
// It is a fixed sentence rather than the provider's own words for two reasons.
// It reaches the user, and "status 401" tells them nothing they can act on;
// and it is the only thing distinguishing a revoked key from every other
// failure on the wire, so it must not drift with a provider's error text.
var ErrProviderKeyRejected = errors.New("the provider rejected this instance's API key")

// captureProviderFailed is the verdict for every provider fault that is not
// classified in handleProviderError: a 5xx, a rate limit, a dial or TLS
// error, a reply that would not decode. One fixed sentence, for the reason
// ErrProviderKeyRejected is one: it reaches the user, and the cause —
// `Post "https://api.groq.com/…": dial tcp …`, `unexpected end of JSON input`
// — tells them nothing they can act on while naming hosts and internals that
// belong in the log. The log line written beside it carries the cause.
const captureProviderFailed = "the transcription or cleanup provider failed; try again"

// handleProviderError records the capture's verdict and reports whether the
// invocation itself should be retried.
//
// A call that ran out of time is not a verdict on the capture. The stage's own
// deadline (TranscribeTimeout, CleanupTimeout) firing means the provider
// stalled, and the invocation ending underneath the call means the same for
// the Lambda; either way the recording is fine and the same call a minute
// later usually is too. So the capture is left in the stage's status, nothing
// is written, and the error goes back to Lambda for its retry, which resumes at
// this stage because the artefact it was making is still missing. Marking it
// failed here — which is what happened before the deadlines existed, when the
// HTTP client's timeout eventually fired — put a permanent "capture failed" in
// front of the user for a transient stall.
//
// A spend cap gets its own status because it is a budget decision, not a fault:
// telling the user "your daily provider budget is spent" is actionable and
// "capture failed" is not. Neither outcome asks Lambda to retry — the same call
// would be refused or fail identically, and three of those is a DLQ entry and an
// alarm for something that is working as designed.
//
// The two provider rejections below are classified rather than merged because
// they need opposite responses. A 401 or 403 will not resolve itself: every
// capture fails identically until somebody replaces the key, so it is worth an
// email on the first occurrence. A 429 usually resolves itself within minutes,
// so alerting on the first one is how an operator learns to ignore the alert
// that matters. Both emit a counter and neither notifies anybody directly —
// the alarms in infrastructure/template.yaml notify on their own state
// transition, which is what makes a dead key one email rather than one per
// capture.
func (p *Pipeline) handleProviderError(ctx context.Context, capture *model.CaptureIndex, stage string, cause error) error {
	if isDeadline(cause) {
		// ctx is the invocation's context. Live here means the stage's own
		// deadline fired, which is the case worth a counter: it says the
		// number beside the stage is too small or the provider is stalling.
		stalled := ctx.Err() == nil
		obs.Log(ctx).Warn("provider call ran out of time; leaving the capture for the retry",
			slog.String("capture_id", capture.ID),
			slog.String("stage", stage),
			slog.Bool("stage_deadline", stalled),
			slog.String("error", cause.Error()))
		if stalled {
			obs.Count(ctx, "ProviderTimedOut", map[string]string{"Stage": stage})
		}
		return fmt.Errorf("pipeline: %s: provider call ran out of time: %w", stage, cause)
	}

	if errors.Is(cause, breaker.ErrSpendCapExceeded) {
		obs.Log(ctx).Warn("capture stopped by the daily spend cap",
			slog.String("capture_id", capture.ID),
			slog.String("stage", stage))
		obs.Count(ctx, "CaptureSpendCapped", map[string]string{"Stage": stage})
		capture.Status = service.StatusSpendCapped
		capture.Error = "daily provider spend cap reached"
		return p.persist(ctx, capture)
	}

	// Provider, not Provider+Op. The dimension set is the metric's identity and
	// is billed as such: Provider alone is two values on this instance, where
	// adding Op would be six for the same answer, since a revoked key is
	// revoked for every op that uses it.
	dims := map[string]string{"Provider": p.providerForStage(stage)}

	switch {
	case provider.IsAuthRejection(cause):
		obs.Log(ctx).Error("provider rejected this instance's API key",
			slog.String("capture_id", capture.ID),
			slog.String("stage", stage),
			slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderKeyRejected", dims)
		obs.Count(ctx, "CaptureStageFailures", map[string]string{"Stage": stage})
		// The user is told what actually happened. Every capture from here on
		// fails the same way until the key is replaced, and "capture failed"
		// would have them re-recording it.
		return p.markFailed(ctx, capture, ErrProviderKeyRejected.Error())

	case provider.IsRateLimited(cause):
		// Warn, not error: this is the expected shape of a busy provider.
		obs.Log(ctx).Warn("provider rate-limited the call",
			slog.String("capture_id", capture.ID),
			slog.String("stage", stage),
			slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderRateLimited", dims)
	default:
		obs.Log(ctx).Error("provider call failed",
			slog.String("capture_id", capture.ID),
			slog.String("stage", stage),
			slog.String("error", cause.Error()))
	}

	obs.Count(ctx, "CaptureStageFailures", map[string]string{"Stage": stage})
	return p.markFailed(ctx, capture, captureProviderFailed)
}

// isDeadline reports whether a provider call ended because its context did —
// the stage's deadline, or the invocation's. The providers wrap the transport
// error with %w and *url.Error unwraps, so errors.Is reaches the sentinel.
func isDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// providerForStage names the provider a stage's call was made against.
//
// The names come from the configuration the price table is keyed on, not from
// a second list here, so a metric can never disagree with the cost record about
// which provider was called.
func (p *Pipeline) providerForStage(stage string) string {
	if stage == "transcribe" {
		return p.cfg.STTProvider
	}
	return p.cfg.LLMProvider
}

// markFailed records the capture's own verdict. reason is one of the fixed
// sentences — never a provider's or Go's error text, which until 2026-09 the
// default branch above wrote to capture.error and the API served as it was.
// It returns the write's error rather than swallowing it: a conceded write
// here means another delivery owns the capture, and reporting that as
// "recorded" would hide a duplicate delivery behind a status this worker never
// actually wrote.
func (p *Pipeline) markFailed(ctx context.Context, capture *model.CaptureIndex, reason string) error {
	capture.Status = model.StatusFailed
	capture.Error = reason
	return p.persist(ctx, capture)
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// estimateTokens is a pre-call guess at prompt size. Four characters per token
// is the usual English rule of thumb; the breaker reconciles against the
// provider's own count once the call returns, so the guess only has to be close
// enough to reserve against.
// routeOutputTokensEstimate is what a routing decision is reserved against
// before the model answers. The answer is a short JSON object — an id, a
// confidence, a title — so the number is small and fixed; the reconcile step
// replaces it with what the provider reports.
const routeOutputTokensEstimate = 64

// tokenUsage converts a provider's token report into what the breaker prices.
// An empty report (a provider that returned no usage block) stays empty, so
// the breaker keeps the estimate rather than reconciling to zero.
func tokenUsage(u provider.TokenUsage) meter.Quantities {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return meter.Quantities{
		meter.UnitInputTokens:  float64(u.InputTokens),
		meter.UnitOutputTokens: float64(u.OutputTokens),
	}
}

func estimateTokens(s string) float64 {
	if s == "" {
		return 0
	}
	return float64(len(s))/4 + 1
}

func estimateCandidateTokens(candidates []routing.Candidate) float64 {
	total := 0
	for _, c := range candidates {
		total += len(c.Title)
		for _, a := range c.Aliases {
			total += len(a)
		}
	}
	return float64(total)/4 + 1
}

func fallbackNoteTitle(now time.Time) string {
	return "Voice note " + now.UTC().Format("2006-01-02 15:04")
}

// noteTouchedAt parses a note's update time, tolerating the RFC3339 and
// RFC3339Nano values written before the fixed-width layout existed.
func noteTouchedAt(n model.NoteIndex) time.Time {
	t, err := model.ParseTime(n.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
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
