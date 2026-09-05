package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// TaskCleanNote is the worker task that regenerates one note's whole-note
// cleaned view (service/note_clean.go). The API sends it for POST
// /v1/notes/{id}/clean and for a recording moved or deleted from a note with
// auto_clean; the pipeline sends it to itself after an append to such a note.
//
// The payload is Invocation with Task set: {"task":"clean-note","tenant_id",
// "note_id","mode","correlation_id"}.
const TaskCleanNote = "clean-note"

// The verdicts a clean-note run can leave on the row. Fixed sentences, for the
// same reason as ErrProviderKeyRejected: they reach the user, and they must
// never carry a provider's words or anything from the note.
const (
	cleanNoteEmpty         = "the note has no text to clean"
	cleanNoteTooLong       = "the note is too long to clean as one document (limit 150 KB)"
	cleanNoteOutputTooLong = "the cleaned text was too long to store (limit 200 KB)"
	cleanNoteUnusable      = "the cleanup model returned nothing usable"
	cleanNoteSpendCapped   = "daily provider spend cap reached"
	cleanNoteProviderFail  = "the cleanup provider failed; try again"
)

// maxCleanNoteStoreAttempts bounds the optimistic-concurrency retry when the
// result is written back to a row that other writers touch.
const maxCleanNoteStoreAttempts = 5

// CleanNote regenerates noteID's cleaned view from the body as it stands now.
//
// It is idempotent in the only sense that matters for an at-least-once
// transport: every run reads the current body and writes a view of it, so two
// deliveries produce the same view and the later one wins. The return value is
// the worker protocol — nil means the run reached a verdict (a view, or a
// stored error the user can read) and must not be retried; an error means an
// infrastructure fault interrupted it and Lambda should try again.
//
// One LLM call, through the breaker, priced like cleanup: output reserved at
// about the size of the input, reconciled to what the provider reports.
func (p *Pipeline) CleanNote(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode) error {
	ctx = obs.WithTenant(ctx, tenantID)
	log := obs.Log(ctx).With(slog.String("note_id", noteID))

	note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
	if errors.Is(err, repository.ErrNotFound) {
		// Purged between the request and the run. Nothing to clean, nothing
		// to record it on, and retrying cannot bring it back.
		log.Info("clean-note: the note no longer exists; nothing to do")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: clean-note: get note: %w", err)
	}
	if !service.NoteIsActive(note) {
		log.Info("clean-note: the note is archived; nothing to do")
		return nil
	}
	if !model.ValidNoteCleanMode(mode) {
		mode = service.EffectiveCleanMode(note)
	}

	// The body and its ETag together. The ETag is re-read after the model
	// answers: a body write that lands during the call — an append, an edit —
	// has already set cleaned_stale on the row, but this run is about to
	// overwrite that row with a view of the OLDER body, so the flag has to be
	// derived from what was actually cleaned rather than copied from the row.
	raw, etag, err := p.cfg.Objects.GetWithETag(ctx, note.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("pipeline: clean-note: get note body: %w", err)
	}
	body := strings.TrimSpace(service.StripCaptureMarkers(string(raw)))

	switch {
	case body == "":
		return p.recordCleanNoteVerdict(ctx, tenantID, noteID, mode, cleanNoteEmpty, "empty")
	case len(body) > model.MaxCleanNoteInputBytes:
		log.Warn("clean-note: the note is over the input cap",
			slog.Int("body_bytes", len(body)),
			slog.Int("limit_bytes", model.MaxCleanNoteInputBytes))
		return p.recordCleanNoteVerdict(ctx, tenantID, noteID, mode, cleanNoteTooLong, "too_long")
	}

	var cleaned provider.Cleaned
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpCleanNote,
		Usage: meter.Quantities{
			meter.UnitInputTokens: estimateTokens(body),
			// A rewrite is about as long as what it rewrites; the provider's
			// count reconciles it.
			meter.UnitOutputTokens: estimateTokens(body),
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		out, err := p.cfg.LLM.CleanNote(ctx, mode, body)
		if err != nil {
			return breaker.Result{}, err
		}
		cleaned = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return p.recordCleanNoteVerdict(ctx, tenantID, noteID, mode, cleanNoteProviderVerdict(ctx, log, err), "provider")
	}

	text, err := cleanup.NoteOutput(cleaned.Text)
	if err != nil {
		log.Warn("clean-note: the model returned nothing usable")
		return p.recordCleanNoteVerdict(ctx, tenantID, noteID, mode, cleanNoteUnusable, "unusable")
	}
	if len(text) > model.MaxCleanedBodyBytes {
		// Refused whole rather than cut: a truncated document presented as the
		// cleaned note is worse than an honest "too long".
		log.Warn("clean-note: the cleaned text is over the output cap",
			slog.Int("output_bytes", len(text)),
			slog.Int("limit_bytes", model.MaxCleanedBodyBytes))
		return p.recordCleanNoteVerdict(ctx, tenantID, noteID, mode, cleanNoteOutputTooLong, "output_too_long")
	}

	// Did the body move while the model was working?
	_, etagNow, err := p.cfg.Objects.GetWithETag(ctx, note.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("pipeline: clean-note: re-read note body: %w", err)
	}
	stale := etagNow != etag

	err = p.updateNoteRow(ctx, tenantID, noteID, func(n *model.NoteIndex) {
		n.CleanedBody = text
		n.CleanedMode = mode
		n.CleanedAt = model.FormatTime(p.now())
		n.CleanedStale = stale
		n.CleanedError = ""
	})
	if err != nil {
		return fmt.Errorf("pipeline: clean-note: store the cleaned view: %w", err)
	}
	log.Info("clean-note: cleaned view stored",
		slog.String("mode", string(mode)),
		slog.Int("input_bytes", len(body)),
		slog.Int("output_bytes", len(text)),
		slog.Bool("stale", stale))
	obs.Count(ctx, "NoteCleanOutcome", map[string]string{"Outcome": "ok"})
	return nil
}

// cleanNoteProviderVerdict turns a failed provider call into the sentence the
// row records, and emits the same signals the capture pipeline does for the
// same failures, so a revoked key seen here raises the same alarm.
func cleanNoteProviderVerdict(ctx context.Context, log *slog.Logger, cause error) string {
	switch {
	case errors.Is(cause, breaker.ErrSpendCapExceeded):
		log.Warn("clean-note: stopped by the daily spend cap")
		return cleanNoteSpendCapped
	case provider.IsAuthRejection(cause):
		log.Error("clean-note: provider rejected this instance's API key", slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderKeyRejected", map[string]string{"Provider": "openai"})
		return ErrProviderKeyRejected.Error()
	case provider.IsRateLimited(cause):
		log.Warn("clean-note: provider rate-limited the call", slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderRateLimited", map[string]string{"Provider": "openai"})
		return cleanNoteProviderFail
	default:
		log.Error("clean-note: provider call failed", slog.String("error", cause.Error()))
		return cleanNoteProviderFail
	}
}

// recordCleanNoteVerdict stores a failed run's reason. An existing view is
// kept: a stored error beside the previous document is more useful than an
// empty pane, and the stale flag already says the view predates the body. With
// no view, the mode and time describe the attempt so the client has something
// to show.
func (p *Pipeline) recordCleanNoteVerdict(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode, reason, outcome string) error {
	err := p.updateNoteRow(ctx, tenantID, noteID, func(n *model.NoteIndex) {
		n.CleanedError = reason
		if n.CleanedBody == "" {
			n.CleanedMode = mode
			n.CleanedAt = model.FormatTime(p.now())
			n.CleanedStale = false
		}
	})
	if err != nil {
		return fmt.Errorf("pipeline: clean-note: record the verdict: %w", err)
	}
	obs.Count(ctx, "NoteCleanOutcome", map[string]string{"Outcome": outcome})
	return nil
}

// updateNoteRow applies change to the current row under its version, re-reading
// on a lost race. Other writers touch this row constantly — every append and
// edit refreshes it — so the loop is the normal path, not the exception.
func (p *Pipeline) updateNoteRow(ctx context.Context, tenantID, noteID string, change func(*model.NoteIndex)) error {
	var lastErr error
	for attempt := 0; attempt < maxCleanNoteStoreAttempts; attempt++ {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
		if err != nil {
			return err
		}
		change(&note)
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

// autoCleanAfterAppend regenerates the cleaned view of a note that asks for
// it, once the append is complete. Through the invoker when one is configured
// — a separate invocation, retried on its own — and inline otherwise, since
// this is already the worker and not the request path. Neither outcome can
// fail the capture: the dictation is in the note, and a view that stays stale
// is what the row already says.
func (p *Pipeline) autoCleanAfterAppend(ctx context.Context, tenantID string, note model.NoteIndex) {
	if !note.AutoClean {
		return
	}
	mode := service.EffectiveCleanMode(note)
	if p.cfg.CleanInvoker != nil {
		if err := p.cfg.CleanInvoker.InvokeCleanNote(ctx, tenantID, note.ID, mode); err != nil {
			obs.Log(ctx).Error("could not hand the note to the worker for auto-clean; the cleaned view stays stale",
				slog.String("note_id", note.ID),
				slog.String("error", err.Error()))
			obs.Count(ctx, "NoteCleanInvokeFailures", map[string]string{"Trigger": "append"})
			return
		}
		obs.Count(ctx, "NoteCleanRequested", map[string]string{"Mode": string(mode), "Trigger": "append"})
		return
	}
	obs.Count(ctx, "NoteCleanRequested", map[string]string{"Mode": string(mode), "Trigger": "append"})
	if err := p.CleanNote(ctx, tenantID, note.ID, mode); err != nil {
		obs.Log(ctx).Error("auto-clean after append did not finish; the cleaned view stays stale",
			slog.String("note_id", note.ID),
			slog.String("error", err.Error()))
	}
}
