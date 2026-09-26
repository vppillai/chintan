package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

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
	// Every write is progress: the API's retry reads this to know whether a
	// worker can still be alive on the capture (service.CaptureStuck).
	capture.LastProgressAt = model.FormatTime(p.now())
	// The same instant is the stage's entry time in the timing record, once
	// per status; CaptureStageEntered counts the stages, StageAt holds the
	// times.
	capture.StageEntered(capture.Status, capture.LastProgressAt)
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
