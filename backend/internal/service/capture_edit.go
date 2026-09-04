package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Editing one recording after the fact: deleting it, and (capture_move.go)
// moving it to another note. Both cut along the paragraph boundary
// note_markers.go defines, and both write the note body under the same
// ETag condition the worker's append and the editor's save use, so a voice
// append landing in the middle of a delete is re-read and re-applied rather
// than overwritten.

var (
	// ErrCaptureInFlight refuses to edit a capture the worker may still be
	// writing. Deleting the row from under an append that has not landed yet
	// would leave its paragraph in the note with nothing pointing at it, and
	// cutting the paragraph before the append has written it cuts nothing.
	ErrCaptureInFlight = errors.New("capture is still being processed")
)

// maxBodyEditAttempts bounds the ETag-conditional retry on a note body. Five is
// the worker's figure for the same loop (pipeline.maxAppendAttempts): a lost
// race is re-read and re-applied, and losing five in a row on one note is not
// contention, it is something wrong.
const maxBodyEditAttempts = 5

// maxIndexRefreshAttempts bounds the optimistic-concurrency retry on the note
// index after a body edit.
const maxIndexRefreshAttempts = 3

// DeleteCapture removes one recording: its paragraph from the note body, the
// note's derived index fields, every object the capture owns, and finally its
// row. The row goes last so a failure anywhere leaves the capture visible and
// the delete retryable; each earlier step is idempotent, so the retry only
// does what is left.
//
// A second call for the same id is ErrNotFound, which is the 404 the API
// promises. Confirmation is the client's concern; the API has none.
func (s *CaptureService) DeleteCapture(ctx context.Context, userID, captureID string) error {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return fmt.Errorf("failed to get capture: %w", err)
	}
	if CaptureIsPending(capture.Status) {
		return ErrCaptureInFlight
	}

	if capture.NoteID != "" {
		note, err := s.store.GetNote(ctx, userID, capture.NoteID)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			// The note was purged from under the capture; there is no body to
			// cut and no index to refresh.
		case err != nil:
			return fmt.Errorf("failed to get note: %w", err)
		default:
			cut, err := rewriteNoteBody(ctx, s.objects, note.S3MarkdownKey, func(body string) (string, bool) {
				rest, _, found := CutCaptureParagraph(body, captureID)
				return rest, found
			})
			if err != nil {
				return fmt.Errorf("failed to remove the paragraph from the note: %w", err)
			}
			if cut {
				if err := refreshNoteIndex(ctx, s.store, s.objects, userID, note.ID); err != nil {
					return fmt.Errorf("failed to refresh the note index: %w", err)
				}
			}
		}
	}

	for _, key := range captureObjectKeys(userID, capture) {
		if err := deleteObjectIfPresent(ctx, s.objects, key); err != nil {
			return fmt.Errorf("failed to delete a capture object: %w", err)
		}
	}
	if err := s.store.DeleteCapture(ctx, userID, captureID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("failed to delete the capture row: %w", err)
	}

	obs.Log(ctx).Info("capture deleted",
		slog.String("capture_id", captureID),
		slog.String("note_id", capture.NoteID),
		slog.String("status", string(capture.Status)))
	obs.Count(ctx, "CapturesDeleted", map[string]string{"Stage": string(capture.Status)})
	return nil
}

// rewriteNoteBody applies edit to the body stored at key under an ETag
// condition, re-reading and re-applying on a lost race. edit returns the new
// body and whether there is anything to write; a body it declines to change
// is not written and written is false. A missing object is an empty body.
func rewriteNoteBody(ctx context.Context, objects repository.Objects, key string, edit func(body string) (string, bool)) (written bool, err error) {
	var lastErr error
	for attempt := 0; attempt < maxBodyEditAttempts; attempt++ {
		existing, etag, err := objects.GetWithETag(ctx, key)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			existing, etag = nil, ""
		case err != nil:
			return false, err
		}

		next, change := edit(string(existing))
		if !change {
			return false, nil
		}

		err = objects.PutIfMatch(ctx, key, []byte(next), "text/markdown", etag)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, repository.ErrPreconditionFailed) {
			return false, err
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	return false, fmt.Errorf("note body changed under %d attempts: %w", maxBodyEditAttempts, lastErr)
}

// refreshNoteIndex re-derives the index fields that follow the body — snippet,
// search text, touch time — from the body now in object storage, under the
// note's version. The body is authoritative, so a version conflict is answered
// by re-reading rather than by overwriting whoever won. It is the API-side
// twin of the worker's refresh after an append.
func refreshNoteIndex(ctx context.Context, store repository.Store, objects repository.Objects, userID, noteID string) error {
	var lastErr error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		note, err := store.GetNote(ctx, userID, noteID)
		if err != nil {
			return err
		}
		body, err := objects.Get(ctx, note.S3MarkdownKey)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		note.Snippet = generateSnippet(string(body))
		note.SearchText = SearchText(string(body))
		note.UpdatedAt = model.Now()

		_, err = store.PutNote(ctx, userID, note)
		if err == nil {
			return nil
		}
		if !errors.Is(err, repository.ErrVersionConflict) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// captureObjectKeys lists every object a capture may own, whether or not the
// row records it.
//
// The peaks key is derived rather than trusted: the worker clears
// CaptureIndex.PeaksKey when the client had not uploaded peaks by the time the
// pipeline finished (pipeline.verifyPeaks), and a late upload would otherwise
// outlive its capture. Deleting a key that names nothing is a no-op, so the
// derived key is always unlinked.
func captureObjectKeys(userID string, c model.CaptureIndex) []string {
	peaksKey := c.PeaksKey
	if peaksKey == "" {
		if derived, err := keys.CapturePeaks(userID, c.ID); err == nil {
			peaksKey = derived
		}
	}
	return []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey, c.SegmentsKey, peaksKey}
}

// deleteObjectIfPresent removes a key, treating "already gone" as success so a
// retried delete can make progress. An empty key names nothing.
func deleteObjectIfPresent(ctx context.Context, objects repository.Objects, key string) error {
	if key == "" {
		return nil
	}
	if err := objects.Delete(ctx, key); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	return nil
}
