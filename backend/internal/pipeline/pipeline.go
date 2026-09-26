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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
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
	InvokeCleanNote(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode, requestedAt string) error
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
	return p.runCapture(ctx, CaptureRef{TenantID: tenantID, CaptureID: captureID})
}

// RunUpload is Run for the S3 notification that starts a capture: the same
// pipeline, plus the one fact only that notification carries — how many bytes
// were actually written — stamped on the row so GET /v1/usage can sum what a
// tenant's recordings occupy. The request-time size_bytes is the client's
// claim; this is the measurement.
func (p *Pipeline) RunUpload(ctx context.Context, ref CaptureRef) (model.CaptureIndex, error) {
	return p.runCapture(ctx, ref)
}

// orphanObjectAfter is how old an S3 notification must be before a missing
// capture row is read as "deleted" rather than "not visible yet". GetCapture
// is an eventually consistent read and the inbox writes the row and then the
// object back to back, so the first delivery can miss a row that exists;
// replication lag is well under a second, and Lambda's first retry of a
// failed invocation comes about a minute later, so thirty seconds cannot be
// lag and is always met by the retry.
const orphanObjectAfter = 30 * time.Second

// ref.ObjectKey and ref.EventTime are set only for an S3 notification: the
// object it names is what an upload with no row left behind.
func (p *Pipeline) runCapture(ctx context.Context, ref CaptureRef) (model.CaptureIndex, error) {
	tenantID, captureID, audioBytes := ref.TenantID, ref.CaptureID, ref.SizeBytes
	ctx = obs.WithTenant(ctx, tenantID)
	log := obs.Log(ctx).With(slog.String("capture_id", captureID))

	capture, err := p.cfg.Store.GetCapture(ctx, tenantID, captureID)
	if errors.Is(err, repository.ErrNotFound) && ref.ObjectKey != "" &&
		!ref.EventTime.IsZero() && p.now().Sub(ref.EventTime) >= orphanObjectAfter {
		// The row was deleted before its upload landed: DELETE lets a stuck
		// upload go after fifteen minutes while the presigned PUT issued with
		// it is good for thirty. Nothing can reach an object with no row, so
		// it is removed here — as RejectOversizedCapture removes its own —
		// rather than failing this invocation three times into the
		// dead-letter queue and leaving the audio in the bucket for good
		// (review 2026-09-24 R4-8). Only a delivery old enough to be a retry
		// says so, see orphanObjectAfter: a first delivery that misses the
		// row fails below and is retried, which is when the row shows up or
		// the object goes.
		if derr := p.cfg.Objects.Delete(ctx, ref.ObjectKey); derr != nil && !errors.Is(derr, repository.ErrNotFound) {
			return model.CaptureIndex{}, fmt.Errorf("pipeline: delete object for a deleted capture: %w", derr)
		}
		log.Warn("object for a deleted capture; removed")
		obs.Count(ctx, "CaptureOrphanObjectRemoved", map[string]string{"Stage": string(model.StatusUploaded)})
		return model.CaptureIndex{}, nil
	}
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

	// The timing record's first hop: how long the capture waited between the
	// row being written and this worker picking it up. Source is two values,
	// app or device, never the device id — a dimension is a billed identity.
	started := p.now()
	source := map[string]string{"Source": sourceDim(capture.Source)}
	created, createdErr := model.ParseTime(capture.CreatedAt)
	var queue time.Duration
	if createdErr == nil {
		queue = started.Sub(created)
		obs.Duration(ctx, "CaptureQueueDelay", queue, source)
	}
	final, err := p.run(ctx, &capture)
	elapsed := p.now().Sub(started)

	// This carries the count as well as the timing: CloudWatch's SampleCount on
	// a duration metric is how many captures ended in that outcome. A separate
	// outcome counter alongside it would be a second billable metric per
	// dimension value telling us a number this one already holds.
	obs.Duration(ctx, "CapturePipelineDuration", elapsed, map[string]string{"Outcome": string(final.Status)})
	if createdErr == nil && final.Status == model.StatusAppended {
		obs.Duration(ctx, "CaptureEndToEnd", p.now().Sub(created), source)
	}
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
	finished := []any{
		slog.String("status", string(final.Status)),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.Int64("queue_ms", queue.Milliseconds()),
		slog.String("source", source["Source"]),
	}
	// How far behind the device's own clock the row was written, when the
	// sender said when it recorded.
	if recorded, rerr := model.ParseTime(capture.RecordedAt); rerr == nil && createdErr == nil {
		finished = append(finished, slog.Int64("device_lag_ms", created.Sub(recorded).Milliseconds()))
	}
	log.Info("capture pipeline finished", finished...)
	return final, nil
}

// sourceDim is the Source dimension of the timing metrics: "device" for a
// capture a device key made, "app" for the app's own. Two values, so the
// metric costs two identities; the device id stays in the row.
func sourceDim(source string) string {
	if strings.HasPrefix(source, "device:") {
		return "device"
	}
	return "app"
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
	if errors.Is(err, repository.ErrNotFound) {
		// The destination was purged between the recording and this run — a
		// "delete forever" while the capture was transcribing. Retrying cannot
		// bring the note back, so this is not the infrastructure fault a
		// dead-letter is for; it is the same question an unroutable capture
		// asks: which note should this go in? The transcript and cleaned text
		// are kept, and the person's answer resumes the pipeline from them.
		obs.Log(ctx).Info("destination note no longer exists; asking for a new one",
			slog.String("capture_id", capture.ID),
			slog.String("note_id", capture.NoteID))
		obs.Count(ctx, "CaptureDestinationPurged", nil)
		capture.NoteID = ""
		capture.TargetSource = ""
		capture.Status = model.StatusNeedsTarget
		capture.Error = ""
		return *capture, p.persist(ctx, capture)
	}
	if err != nil {
		return *capture, fmt.Errorf("pipeline: get note: %w", err)
	}
	if !service.NoteIsActive(note) {
		return *capture, p.markFailed(ctx, capture, service.ErrNoteArchived.Error())
	}

	if capture.CleanKey == "" && wantsNoteLanguage(*capture, note) {
		// The destination was not known when the recording was transcribed
		// (it was routed, or a person chose it afterwards), so the transcript
		// is in the tenant's default and the note's "Transcription language"
		// was never applied — a Malayalam dictation aimed by voice at an ml
		// note went to Whisper as auto and came back in Tamil script (review
		// 2026-09-21, T2). Transcribing once more in the note's language is
		// the promise that field makes; it costs one more STT call only in
		// the mismatch case. The routed text goes too, so the instruction
		// strip below runs over the new transcript with the destination
		// pinned. Language is written with RawKey, so a retry that finds the
		// second transcript does not make a third.
		obs.Log(ctx).Info("destination note asks for another language; transcribing again",
			slog.String("capture_id", capture.ID),
			slog.String("note_id", note.ID),
			slog.String("language_sent", capture.Language),
			slog.String("language_wanted", note.Language))
		obs.Count(ctx, "CaptureRetranscribedForNote", nil)
		capture.RawKey, capture.SegmentsKey, capture.RoutedKey = "", "", ""
		if err := p.transcribe(ctx, tenantID, capture); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	}

	switch {
	case capture.CleanKey != "":
		// Cleaned already; a retry resumes at the append.
	case note.Kind == model.NoteKindChecklist:
		// A checklist takes items, not a cleaned paragraph, and the items
		// come from the raw transcript: the extraction prompt handles the
		// words addressed to the app itself, so neither the router's spans
		// nor the instruction strip below is consulted — the item is not at
		// the mercy of where a span ended ("Add umbrella to shopping list"
		// routed as the item "list", owner feedback 2026-09-26). A verbatim
		// checklist takes the raw transcript itself as its one item, on the
		// routed path as on the targeted one; the routed text would carry
		// the same span damage.
		if err := p.extractItems(ctx, tenantID, capture, note); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	default:
		if capture.RoutedKey == "" {
			// Recorded into a note, so routing — and with it the removal of
			// the words addressed to the app — was skipped.
			if err := p.stripInstructions(ctx, tenantID, capture, note); err != nil {
				return *capture, err
			}
			if service.CaptureIsTerminal(capture.Status) {
				return *capture, nil
			}
		}
		if err := p.clean(ctx, tenantID, capture, note.Verbatim); err != nil {
			return *capture, err
		}
		if service.CaptureIsTerminal(capture.Status) {
			return *capture, nil
		}
	}

	return p.append(ctx, tenantID, capture, note)
}

// wantsNoteLanguage reports whether the transcript at RawKey was made in a
// language other than the one its destination note asks for. A note that
// inherits the default, or asks for auto-detection, never asks for a second
// transcription: the first was in the default, and auto is what the review
// found unreliable for the languages this matters for. A recording a person
// asked to have transcribed in a particular language keeps that answer.
func wantsNoteLanguage(c model.CaptureIndex, note model.NoteIndex) bool {
	if c.RequestedLanguage != "" || c.AudioKey == "" {
		// A person chose, or the capture arrived as text (POST
		// /v1/inbox/text) and there is nothing to transcribe again.
		return false
	}
	return note.Language != "" && note.Language != model.LanguageAuto && c.Language != note.Language
}
