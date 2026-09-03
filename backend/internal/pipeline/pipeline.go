// Package pipeline runs the slow half of a capture: transcribe, route, clean,
// append.
//
// It lives out of band, behind SQS, for one reason. API Gateway's HTTP API caps
// an integration at 30 seconds and the cap is not adjustable, so any capture
// whose speech-to-text plus LLM work exceeds that returned 504 to the user while
// the Lambda kept running and billing — and the client's retry then appended the
// same text a second time. For a driving-length recording that was the common
// case, not an edge case.
//
// Two properties hold throughout and are worth stating before the code:
//
//   - Every provider call goes through breaker.Do. There is no path to a
//     provider that skips the spend check or the metering record — and, since
//     the worker builds the breaker with a tenant cap resolver, the check is
//     against the cap the tenant set rather than only the instance-wide one.
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

	// maxRouteCandidates bounds the note list handed to the router.
	maxRouteCandidates = 50

	// maxRouteCandidatePool bounds how many notes are paged through before the
	// most recent maxRouteCandidates are chosen.
	maxRouteCandidatePool = 500

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
)

// NoteCreator creates the destination note for a capture that has none.
type NoteCreator interface {
	CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error)
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

	// Provider and model names are what the price table is keyed on. They are
	// passed in rather than inferred so an instance can point at a different
	// endpoint without the cost record quietly becoming wrong.
	STTProvider string
	STTModel    string
	LLMProvider string
	LLMModel    string

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
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Pipeline{cfg: cfg, now: now}, nil
}

// errDeliveryConceded means another delivery of the same capture owns the row,
// and this one stopped rather than fight it for the conditional write.
//
// It is not a failure. SQS is at-least-once, so two deliveries of one capture is
// a normal event; treating the loser as an error would redeliver it, exhaust
// maximumReceiveCount, and put a dead-letter entry and an alarm in front of a
// human for a system working exactly as designed.
var errDeliveryConceded = errors.New("pipeline: another delivery owns this capture")

// errAppendClaimHeld means this delivery found its own append claim already
// taken and not finished, inside the lease. Unlike errDeliveryConceded it IS a
// reason to redeliver: the holder may be dead, and only the lease expiring can
// prove it. See append.
var errAppendClaimHeld = errors.New("pipeline: append claim held by an unfinished attempt")

// Run drives one capture as far as it can go and returns its final state.
//
// A returned error means the invocation should be retried by SQS: it is an
// infrastructure fault, not a verdict on the capture. A capture that failed for
// its own reasons — a provider error, an exhausted spend cap, an undecidable
// destination — is persisted in that state and returned with a nil error, so the
// message is not redelivered to fail identically twice more before the DLQ. So
// is a capture another delivery is already carrying.
func (p *Pipeline) Run(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
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

	started := p.now()
	final, err := p.run(ctx, &capture)
	elapsed := p.now().Sub(started)

	// This carries the count as well as the timing: CloudWatch's SampleCount on
	// a duration metric is how many captures ended in that outcome. A separate
	// outcome counter alongside it would be a second billable metric per
	// dimension value telling us a number this one already holds.
	obs.Duration(ctx, "CapturePipelineDuration", elapsed, map[string]string{"Outcome": string(final.Status)})
	p.markAudioProcessedIfSafe(ctx, &final)
	if errors.Is(err, errDeliveryConceded) {
		// The other delivery is either finished or still running. Either way this
		// one is done and its message must be deleted, not redelivered. If the
		// owner dies mid-flight the queue's visibility timeout redelivers its
		// message, and the append claim's lease lets that redelivery take the
		// append over — so conceding drops nothing.
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
// A nil return means the verdict is recorded and the queue message is done. An
// error means the *recording* failed, so the message should come back.
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
		return *capture, p.markFailed(ctx, capture, service.ErrNoteArchived)
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

// ---------------------------------------------------------------------------
// Stage 1 — transcribe
// ---------------------------------------------------------------------------

func (p *Pipeline) transcribe(ctx context.Context, tenantID string, capture *model.CaptureIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusTranscribing); err != nil {
		return err
	}

	// A presigned GET, not the bytes. v1 pulled the whole object into the Lambda
	// heap and re-POSTed it, which made the heap — rather than the microphone —
	// the real cap on how long a recording could be.
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

	var result provider.Transcription
	_, err = p.cfg.Breaker.Do(ctx, tenantID, breaker.Estimate{
		Provider: p.cfg.STTProvider,
		Model:    p.cfg.STTModel,
		Op:       meter.OpTranscribe,
		Unit:     meter.UnitAudioSeconds,
		Quantity: estimateSeconds,
	}, func(ctx context.Context) (breaker.Result, error) {
		out, err := p.cfg.STT.Transcribe(ctx, provider.Audio{
			URL:         audioURL,
			ContentType: contentTypeForAudioKey(capture.AudioKey),
		})
		if err != nil {
			return breaker.Result{}, err
		}
		result = out
		return breaker.Result{Unit: meter.UnitAudioSeconds, Quantity: out.Duration}, nil
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

	decision, err := p.decideTarget(ctx, tenantID, transcript)
	if err != nil {
		if errors.Is(err, breaker.ErrSpendCapExceeded) {
			return p.handleProviderError(ctx, capture, "route", err)
		}
		// Routing is a convenience; a recording is never lost because of it.
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
		// now in different notes. The message is worth redelivering; a duplicate
		// note is not worth creating.
		note, err := p.cfg.Store.GetNote(ctx, tenantID, decision.NoteID)
		switch {
		case err == nil && service.NoteIsActive(note):
			if decision.Confidence >= routeConfidenceThreshold {
				capture.NoteID = decision.NoteID
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
	capture.Status = model.StatusTranscribed
	return p.persist(ctx, capture)
}

func (p *Pipeline) decideTarget(ctx context.Context, tenantID, transcript string) (provider.RouteDecision, error) {
	if p.cfg.Router == nil {
		return provider.RouteDecision{}, fmt.Errorf("pipeline: routing is not configured")
	}

	// Notes are paged, and the router only ever sees the most recent
	// maxRouteCandidates of them, so the pool has to be drained before it can be
	// ordered.
	active, err := repository.DrainPages(ctx, maxRouteCandidatePool, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return p.cfg.Store.ListNotes(ctx, tenantID, opts)
	})
	if err != nil {
		return provider.RouteDecision{}, fmt.Errorf("pipeline: list notes: %w", err)
	}

	// Most recently touched notes are the likeliest destinations. Compare parsed
	// instants: v1 compared RFC3339Nano strings, and Go trims trailing fractional
	// zeros, so "…:00Z" sorted above "…:00.1Z" because 'Z' > '.' and the router
	// was handed the wrong fifty notes.
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

	var decision provider.RouteDecision
	_, err = p.cfg.Breaker.Do(ctx, tenantID, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpRoute,
		Unit:     meter.UnitInputTokens,
		Quantity: estimateTokens(transcript) + estimateCandidateTokens(candidates),
	}, func(ctx context.Context) (breaker.Result, error) {
		out, err := p.cfg.Router.Route(ctx, transcript, candidates)
		if err != nil {
			return breaker.Result{}, err
		}
		decision = out
		return breaker.Result{
			Unit:     meter.UnitInputTokens,
			Quantity: float64(out.Usage.InputTokens),
		}, nil
	})
	if err != nil {
		return provider.RouteDecision{}, err
	}
	return decision, nil
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
	_, err = p.cfg.Breaker.Do(ctx, tenantID, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpCleanup,
		Unit:     meter.UnitInputTokens,
		Quantity: estimateTokens(source),
	}, func(ctx context.Context) (breaker.Result, error) {
		out, err := p.cfg.LLM.Cleanup(ctx, capture.Mode, source)
		if err != nil {
			return breaker.Result{}, err
		}
		cleaned = out
		return breaker.Result{
			Unit:     meter.UnitInputTokens,
			Quantity: float64(out.Usage.InputTokens),
		}, nil
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

	// The append is the one step that must happen exactly once. v1 appended, then
	// updated the index, then set the status, with nothing tying the three
	// together: a failure after the append left the capture in `cleaned`, and the
	// retry path re-appended the same text. The token is derived from the capture
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
	// what lets the append below check the note body instead of trusting the
	// lease, so exactly-once no longer rests on AppendClaimLease being kept
	// above the queue's visibility timeout by hand.
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
		// here the two look the same. Conceding — which is what used to happen
		// — acked the message and, in the second case, stranded the capture in
		// `appending` for good: SQS redelivers at 960 s, inside the 20-minute
		// lease, and nothing is redelivered after a concede.
		//
		// If the text is already in the note body the dangerous part is over,
		// whoever did it: finishing the bookkeeping is idempotent (the index
		// refresh re-derives from the body, the completion is a versioned write
		// on the same token), so do it now rather than wait for the lease.
		// Otherwise ask to be redelivered; by the time the message comes back
		// the lease has either been finished by its holder or run out, and the
		// takeover above resumes the interrupted attempt with the body check.
		if written, err := p.textAlreadyInNote(ctx, note.S3MarkdownKey, cleanedText); err != nil {
			return current, fmt.Errorf("pipeline: check note for interrupted append: %w", err)
		} else if written {
			obs.Log(ctx).Info("append claim is held but the text is already in the note; finishing the interrupted attempt",
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

	if err := p.appendToNote(ctx, note.S3MarkdownKey, cleanedText, resumingOwnAttempt); err != nil {
		// Hand the claim back so a transient object-store failure does not park
		// the capture until the claim lease expires.
		p.releaseAppendClaim(ctx, capture)
		return *capture, fmt.Errorf("pipeline: append to note: %w", err)
	}

	return p.finishAppend(ctx, tenantID, capture, note, cleanedText, token)
}

// finishAppend is the bookkeeping after the text is durably in the note body:
// the index refresh and the completion of the claim. Both are safe to repeat,
// which is what lets a redelivery that finds the text already written finish an
// attempt that died here.
func (p *Pipeline) finishAppend(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, cleanedText, token string) (model.CaptureIndex, error) {
	if err := p.refreshNoteIndex(ctx, tenantID, note.ID, cleanedText); err != nil {
		return *capture, fmt.Errorf("pipeline: refresh note index: %w", err)
	}

	appended, err := p.cfg.Store.CompleteCaptureAppend(ctx, tenantID, capture.ID, token)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: complete capture append: %w", err)
	}
	*capture = appended
	return appended, nil
}

// textAlreadyInNote reports whether text is already in the note body. It is the
// same test appendToNote applies when resuming, and carries the same caveat: it
// is only meaningful for an attempt that already holds the claim for this exact
// capture and cleaned artefact, never as a general "is this text here?".
func (p *Pipeline) textAlreadyInNote(ctx context.Context, noteKey, text string) (bool, error) {
	if text == "" {
		return false, nil
	}
	existing, err := p.cfg.Objects.Get(ctx, noteKey)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.Contains(string(existing), text), nil
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

// appendToNote adds text to the end of a note body under a conditional write.
//
// v1 did read-concat-write with no concurrency control, so a voice append
// landing while the editor was saving silently discarded one of the two. Here
// the write carries the ETag that was read; a lost race re-reads and retries so
// both edits survive.
//
// resuming says this call is a redelivery of an attempt that already held the
// claim for this exact capture and cleaned artefact. Only then is the body
// inspected for the text, and only then is finding it a reason to do nothing:
// the alternative — a plain "is this text already here?" — would swallow a
// second capture in which the user said the same short sentence twice.
func (p *Pipeline) appendToNote(ctx context.Context, noteKey, text string, resuming bool) error {
	var lastErr error
	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		existingContent, etag, err := p.cfg.Objects.GetWithETag(ctx, noteKey)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			existingContent, etag = nil, ""
		case err != nil:
			return fmt.Errorf("pipeline: get existing note: %w", err)
		}

		if resuming && text != "" && strings.Contains(string(existingContent), text) {
			// The interrupted attempt got as far as the body. Nothing to write;
			// the caller goes on to finish the bookkeeping it never reached.
			obs.Log(ctx).Info("append already in the note body; finishing the interrupted attempt instead of repeating it",
				slog.String("note_key", noteKey))
			obs.Count(ctx, "AppendResumedWithoutRewriting", map[string]string{"Stage": string(service.StatusAppending)})
			return nil
		}

		newContent := text
		if len(existingContent) > 0 {
			newContent = string(existingContent) + "\n\n" + text
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

// refreshNoteIndex re-derives the snippet and touch time from the note body that
// is now in object storage. The body is authoritative, so a version conflict is
// resolved by re-reading rather than by overwriting whoever won.
func (p *Pipeline) refreshNoteIndex(ctx context.Context, tenantID, noteID, fallbackBody string) error {
	var lastErr error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
		if err != nil {
			return err
		}
		if existing, err := p.cfg.Objects.Get(ctx, note.S3MarkdownKey); err == nil {
			note.Snippet = service.Snippet(string(existing))
		} else {
			note.Snippet = service.Snippet(fallbackBody)
		}
		note.UpdatedAt = model.FormatTime(p.now())

		if _, err := p.cfg.Store.PutNote(ctx, tenantID, note); err == nil {
			return nil
		} else if !errors.Is(err, repository.ErrVersionConflict) {
			return err
		} else {
			lastErr = err
		}
	}
	return lastErr
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

	// Info, not warn. A duplicate delivery is expected of an at-least-once queue;
	// the counter is here so that "expected" can be checked against reality
	// rather than assumed, because a sustained rate of these means the visibility
	// timeout is shorter than the pipeline and every capture is being done twice.
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

// handleProviderError records the capture's verdict and reports whether the
// invocation itself should be retried.
//
// A spend cap gets its own status because it is a budget decision, not a fault:
// telling the user "your daily provider budget is spent" is actionable and
// "capture failed" is not. Neither outcome asks SQS to redeliver — the same call
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
		return p.markFailed(ctx, capture, ErrProviderKeyRejected)

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
	return p.markFailed(ctx, capture, cause)
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

// markFailed records the capture's own verdict. It returns the write's error
// rather than swallowing it: a conceded write here means another delivery owns
// the capture, and reporting that as "recorded" would hide a duplicate delivery
// behind a status this worker never actually wrote.
func (p *Pipeline) markFailed(ctx context.Context, capture *model.CaptureIndex, cause error) error {
	capture.Status = model.StatusFailed
	capture.Error = cause.Error()
	return p.persist(ctx, capture)
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// estimateTokens is a pre-call guess at prompt size. Four characters per token
// is the usual English rule of thumb; the breaker reconciles against the
// provider's own count once the call returns, so the guess only has to be close
// enough to reserve against.
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
