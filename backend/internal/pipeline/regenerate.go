package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// TaskRegenerateNote is the worker task that re-runs the prompt-dependent
// stage over every recording of one note and re-appends each where it stands
// (docs/design/regenerate.md). The API sends it for POST
// /v1/notes/{id}/regenerate with the ids it reset; `chintanctl regenerate`
// sends it with none, and the worker chooses and resets them itself.
//
// The payload is Invocation with Task set: {"task":"regenerate-note",
// "tenant_id","note_id","capture_ids","correlation_id"}.
const TaskRegenerateNote = "regenerate-note"

// RegenerateNote re-cleans noteID's recordings with the current prompts and
// puts each recording's new words where its old ones stand, then regenerates
// the whole-note cleaned view when a plain note has one (never a checklist:
// cleanNoteAfter returns at once for one, R8-F8). It never transcribes:
// every recording resumes from its stored transcript, exactly as a retry
// resumes a capture whose RawKey is set.
//
// One recording at a time, in the order they were made, so the appends to
// one body never wait on each other's stamp. A recording that fails on its
// own terms — the provider, the spend cap — is left with that verdict for
// the strip to show and Retry to resume, and the run goes on to the next; an
// infrastructure fault returns the error, Lambda retries the task, the
// recordings already appended are skipped by their status and the one left
// mid-way — cleaned, or at appending with its claim handed back — is
// resumed where it stood (regenerationInFlight). A plain note's whole-note
// view is regenerated once, at the end, rather than after each append: with
// auto_clean every append would have queued a run, and each run but the
// last would have been superseded after its model call was billed.
//
// The return value is the worker protocol: nil means every recording reached
// a verdict, an error means retry the task.
func (p *Pipeline) RegenerateNote(ctx context.Context, tenantID, noteID string, captureIDs []string) error {
	ctx = obs.WithTenant(ctx, tenantID)
	log := obs.Log(ctx).With(slog.String("note_id", noteID))

	note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
	if errors.Is(err, repository.ErrNotFound) {
		log.Info("regenerate-note: the note no longer exists; nothing to do")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: regenerate-note: get note: %w", err)
	}
	if !service.NoteIsActive(note) {
		log.Info("regenerate-note: the note is archived; nothing to do")
		return nil
	}

	if len(captureIDs) == 0 {
		// The operator's road: nothing was reset on the way in, so the
		// worker applies the request path's rule itself. A note with a
		// recording still in flight is left alone rather than raced.
		captures, err := service.RegenerableCaptures(ctx, p.cfg.Store, p.cfg.Objects, tenantID, note, p.now())
		if errors.Is(err, service.ErrRegenerateInFlight) {
			// The CLI reported this note queued; the log line and the
			// counter are where an operator learns it was passed over.
			log.Info("regenerate-note: a recording is still in flight; not regenerating this note now")
			obs.Count(ctx, "NoteRegenerateSkipped", map[string]string{"Reason": "in_flight"})
			return nil
		}
		if err != nil {
			return fmt.Errorf("pipeline: regenerate-note: list captures: %w", err)
		}
		for _, c := range captures {
			service.ResetForRegenerate(&c, p.now())
			if _, err := p.cfg.Store.PutCapture(ctx, c); err != nil {
				if errors.Is(err, repository.ErrVersionConflict) {
					// Somebody else is writing this row; it keeps its words.
					continue
				}
				return fmt.Errorf("pipeline: regenerate-note: reset capture: %w", err)
			}
			captureIDs = append(captureIDs, c.ID)
		}
	}

	done := 0
	for _, id := range captureIDs {
		capture, err := p.cfg.Store.GetCapture(ctx, tenantID, id)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("pipeline: regenerate-note: get capture: %w", err)
		}
		if capture.NoteID != noteID || capture.RawKey == "" || !regenerationInFlight(capture.Status) {
			// Appended by an earlier attempt of this task, failed on its own
			// terms, moved elsewhere or transcribed again meanwhile: not ours.
			continue
		}
		final, err := p.regenerateCapture(ctx, tenantID, &capture, note)
		if errors.Is(err, errDeliveryConceded) {
			continue
		}
		if err != nil {
			return err
		}
		if final.Status == model.StatusAppended {
			// Only a landed recording changed the body. One that failed or
			// was capped left it as it was, and the whole-note view is not
			// billed over an unchanged body.
			done++
		}
		obs.Count(ctx, "CaptureRegenerated", map[string]string{"Outcome": string(final.Status)})
	}

	// The row as the appends left it, since each index refresh rewrote it.
	current, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
	if err != nil {
		return fmt.Errorf("pipeline: regenerate-note: re-read note: %w", err)
	}
	if done > 0 && current.CleanedBody != "" {
		p.cleanNoteAfter(ctx, tenantID, current, "regenerate")
	}
	log.Info("regenerate-note: finished",
		slog.Int("captures", len(captureIDs)),
		slog.Int("regenerated", done),
		slog.Bool("cleaned_view", done > 0 && current.CleanedBody != ""))
	return nil
}

// regenerationInFlight reports whether status is one the regeneration itself
// leaves a recording in: reset by the request path (transcribed), or left
// mid-way by an earlier attempt of the task that hit a fault — the cleanup
// begun or persisted (cleaning, cleaned), or the append claimed and handed
// back (appending). Lambda's retry of the task finishes those instead of
// leaving one row for the strip's Retry a quarter of an hour on. An append
// still held by a live attempt is refused by the claim, as ever.
func regenerationInFlight(status model.CaptureStatus) bool {
	switch status {
	case model.StatusTranscribed, model.StatusCleaning, model.StatusCleaned, model.StatusAppending:
		return true
	}
	return false
}

// regenerateCapture is run from the point after transcription for a capture
// whose transcript is already stored: the cleanup or the item extraction with
// the current prompt — or neither, when an earlier attempt of the task did it
// and died before the append landed — then the append, which replaces the
// paragraph by its marker or the items by their words. The instruction strip
// is not run again — the routed transcript, when there is one, already has
// the words spoken to the app removed — and the language check is not, since
// the transcript is the one being kept.
func (p *Pipeline) regenerateCapture(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) (model.CaptureIndex, error) {
	previous, err := p.cleanForNote(ctx, tenantID, capture, note)
	if err != nil || service.CaptureIsTerminal(capture.Status) {
		return *capture, err
	}
	return p.append(ctx, tenantID, capture, note, appendOptions{previousItems: previous})
}

// dropReplacedItems takes a recording's earlier items out of a checklist
// when its transcript, extracted again, named nothing to add: the recording
// is no_content now, and a no_content recording owns no lines. Nothing is
// written for a recording that never had items, or whose items the person
// has already removed.
func (p *Pipeline) dropReplacedItems(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, previous []string) error {
	if capture.Status != model.StatusNoContent || len(previous) == 0 {
		return nil
	}
	written, err := service.RewriteNoteBody(ctx, p.cfg.Objects, note.S3MarkdownKey, func(existing string) (string, bool) {
		if !service.HasCaptureMarker(existing, capture.ID) {
			return existing, false
		}
		next := replaceChecklistItems(existing, capture.ID, previous, "")
		return next, next != existing
	})
	if err != nil {
		return fmt.Errorf("pipeline: remove the recording's earlier items: %w", err)
	}
	if !written {
		return nil
	}
	if _, err := service.RefreshNoteIndex(ctx, p.cfg.Store, p.cfg.Objects, tenantID, note.ID, service.RefreshOptions{
		Now:         p.now,
		RequireBody: true,
		Attempts:    maxIndexRefreshAttempts,
	}); err != nil {
		return fmt.Errorf("pipeline: refresh note index: %w", err)
	}
	return nil
}

// handleRegenerateNote runs one regenerate-note task. A payload that names
// the task but not a note is discarded like any other unparseable
// invocation.
func (w *Worker) handleRegenerateNote(ctx context.Context, task Invocation) error {
	if task.TenantID == "" || task.NoteID == "" {
		obs.Log(ctx).Error("discarding a regenerate-note invocation that names no note")
		obs.CountWithRollup(ctx, "WorkerMessagesDiscarded", map[string]string{"Reason": "unparseable"})
		return nil
	}
	id, ok := obs.SanitizeCorrelationID(task.CorrelationID)
	if !ok {
		id, ok = obs.SanitizeCorrelationID("regenerate-" + task.NoteID)
		if !ok {
			id = obs.NewCorrelationID()
		}
	}
	ctx = obs.WithCorrelationID(ctx, id)
	if err := w.pipeline.RegenerateNote(ctx, task.TenantID, task.NoteID, task.CaptureIDs); err != nil {
		obs.Log(ctx).Error("regenerate-note will be retried",
			slog.String("note_id", task.NoteID),
			slog.String("error", err.Error()))
		return err
	}
	return nil
}
