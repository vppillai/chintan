package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

var (
	// ErrCaptureUnfiled refuses to move a capture that has no note. Choosing
	// a first destination is SetCaptureTarget's job, and it also resumes the
	// pipeline; a move only relocates text that is already written.
	ErrCaptureUnfiled = errors.New("capture has no note to move from")
	// ErrMoveIncomplete means the move failed and was rolled back: the source
	// note holds the paragraph where it was, the capture still points at it,
	// and the request can simply be repeated.
	ErrMoveIncomplete = errors.New("capture move did not complete; nothing changed")
	// ErrMoveUnrecovered means the move failed after the paragraph was cut
	// from the source and the source could not be restored. The clean
	// transcript object still exists, and the failure is logged with the ids
	// an operator needs, but this is the one outcome a retry cannot fix.
	ErrMoveUnrecovered = errors.New("capture move failed and could not be undone")
)

// MoveCapture relocates one recording to another note: its paragraph is cut
// from the source body, inserted into the target in chronological position
// among the target's own recordings, the capture row is re-pointed, and both
// note indexes are refreshed. The marker travels with the paragraph, so the
// worker's exactly-once guard and every later edit keep working on the moved
// text.
//
// moved is false, with no error, when the capture is already in the target —
// the no-op the API answers 204.
//
// The two bodies are written one after the other, each under its ETag. There
// is no transaction across them, so the order and the rollback are what stand
// in for one: the source is cut first, and if the target write then fails the
// paragraph is put back where it was and ErrMoveIncomplete says the request
// can be repeated. Everything after the target write — the index refreshes,
// the row — is idempotent, so a failure there leaves a state a retry finishes:
// the cut finds nothing to cut, the insert finds its marker already there, and
// the rest runs again.
func (s *CaptureService) MoveCapture(ctx context.Context, userID, captureID, targetNoteID string) (capture *model.CaptureIndex, moved bool, err error) {
	current, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get capture: %w", err)
	}
	if current.NoteID == "" {
		return nil, false, ErrCaptureUnfiled
	}
	if CaptureIsPending(current.Status) {
		return nil, false, ErrCaptureInFlight
	}
	if current.NoteID == targetNoteID {
		return &current, false, nil
	}

	target, err := s.store.GetNote(ctx, userID, targetNoteID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get target note: %w", err)
	}
	if !NoteIsActive(target) {
		return nil, false, ErrNoteArchived
	}

	sourceID := current.NoteID
	sourceKey := ""
	switch source, err := s.store.GetNote(ctx, userID, sourceID); {
	case errors.Is(err, repository.ErrNotFound):
		// The source was purged from under the capture. There is no paragraph
		// to carry; the row still moves.
	case err != nil:
		return nil, false, fmt.Errorf("failed to get source note: %w", err)
	default:
		sourceKey = source.S3MarkdownKey
	}

	// Everything the insert needs is gathered before anything is written, so a
	// failure here changes nothing.
	before, err := s.olderCapturesIn(ctx, userID, targetNoteID, current.CreatedAt)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrMoveIncomplete, err)
	}

	// 1. Cut the paragraph out of the source.
	var text string
	cut := false
	if sourceKey != "" {
		cut, err = rewriteNoteBody(ctx, s.objects, sourceKey, func(body string) (string, bool) {
			rest, t, found := CutCaptureParagraph(body, captureID)
			text = t
			return rest, found
		})
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrMoveIncomplete, err)
		}
	}

	// 2. Put it into the target, or put it back where it was.
	if cut {
		_, err = rewriteNoteBody(ctx, s.objects, target.S3MarkdownKey, func(body string) (string, bool) {
			if HasCaptureMarker(body, captureID) {
				return body, false
			}
			return InsertCaptureParagraph(body, captureID, text, before), true
		})
		if err != nil {
			return nil, false, s.undoCut(ctx, userID, sourceKey, sourceID, targetNoteID, current, text, err)
		}
	}

	// 3. Both indexes follow their bodies, then the row follows the paragraph.
	// The row is last so a capture never claims a note its paragraph is not
	// in yet; a retry after a failure here re-runs exactly these steps.
	if sourceKey != "" {
		if err := refreshNoteIndex(ctx, s.store, s.objects, userID, sourceID); err != nil {
			return nil, false, fmt.Errorf("failed to refresh the source note index: %w", err)
		}
	}
	if err := refreshNoteIndex(ctx, s.store, s.objects, userID, targetNoteID); err != nil {
		return nil, false, fmt.Errorf("failed to refresh the target note index: %w", err)
	}
	updated, err := s.repointCapture(ctx, userID, captureID, targetNoteID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to re-point the capture: %w", err)
	}

	obs.Log(ctx).Info("capture moved",
		slog.String("capture_id", captureID),
		slog.String("from_note_id", sourceID),
		slog.String("to_note_id", targetNoteID),
		slog.Bool("paragraph_moved", cut))
	obs.Count(ctx, "CapturesMoved", map[string]string{"Stage": string(current.Status)})
	return &updated, true, nil
}

// olderCapturesIn returns the before() an insert into noteID uses: true for a
// capture of that note created after createdAt. CreatedAt is written with a
// fixed-width fraction, so the string comparison is the chronological one. A
// marker whose row is unknown is never "later", so the paragraph lands after
// it.
func (s *CaptureService) olderCapturesIn(ctx context.Context, userID, noteID, createdAt string) (func(id string) bool, error) {
	captures, err := repository.DrainPages(ctx, 0, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list the note's captures: %w", err)
	}
	created := make(map[string]string, len(captures))
	for _, c := range captures {
		created[c.ID] = c.CreatedAt
	}
	return func(id string) bool {
		at, ok := created[id]
		return ok && at > createdAt
	}, nil
}

// undoCut is the compensation for a target write that failed after the source
// was cut: the paragraph goes back into the source at its chronological place,
// and the caller reports a move that changed nothing. If even that fails the
// text is out of both notes, which is logged with every id an operator needs
// and reported as ErrMoveUnrecovered rather than dressed up as retryable.
func (s *CaptureService) undoCut(ctx context.Context, userID, sourceKey, sourceID, targetID string, c model.CaptureIndex, text string, cause error) error {
	before, err := s.olderCapturesIn(ctx, userID, sourceID, c.CreatedAt)
	if err != nil {
		// Losing the position is better than losing the text.
		before = func(string) bool { return false }
	}
	_, rerr := rewriteNoteBody(ctx, s.objects, sourceKey, func(body string) (string, bool) {
		if HasCaptureMarker(body, c.ID) {
			return body, false
		}
		return InsertCaptureParagraph(body, c.ID, text, before), true
	})
	if rerr != nil {
		obs.Log(ctx).Error("capture move failed and the source note could not be restored",
			slog.String("capture_id", c.ID),
			slog.String("from_note_id", sourceID),
			slog.String("to_note_id", targetID),
			slog.String("error", cause.Error()),
			slog.String("restore_error", rerr.Error()))
		obs.Count(ctx, "CaptureMoveUnrecovered", map[string]string{"Stage": string(c.Status)})
		return fmt.Errorf("%w: %w", ErrMoveUnrecovered, cause)
	}
	obs.Log(ctx).Warn("capture move failed after the cut; the source note was restored",
		slog.String("capture_id", c.ID),
		slog.String("from_note_id", sourceID),
		slog.String("to_note_id", targetID),
		slog.String("error", cause.Error()))
	obs.Count(ctx, "CaptureMoveRolledBack", map[string]string{"Stage": string(c.Status)})
	return fmt.Errorf("%w: %w", ErrMoveIncomplete, cause)
}

// repointCapture writes noteID onto the capture row under its version,
// re-reading on a conflict. A row that already points at noteID is done.
func (s *CaptureService) repointCapture(ctx context.Context, userID, captureID, noteID string) (model.CaptureIndex, error) {
	var lastErr error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		c, err := s.store.GetCapture(ctx, userID, captureID)
		if err != nil {
			return model.CaptureIndex{}, err
		}
		if c.NoteID == noteID {
			return c, nil
		}
		c.NoteID = noteID
		updated, err := s.store.PutCapture(ctx, c)
		if err == nil {
			return updated, nil
		}
		if !errors.Is(err, repository.ErrVersionConflict) {
			return model.CaptureIndex{}, err
		}
		lastErr = err
	}
	return model.CaptureIndex{}, lastErr
}
