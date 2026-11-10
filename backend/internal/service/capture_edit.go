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

// maxBodyEditAttempts bounds the ETag-conditional retry on a note body, for
// the editor's paths and the worker's append alike (RewriteNoteBody): a lost
// race is re-read and re-applied, and losing five in a row on one note is not
// contention, it is something wrong.
const maxBodyEditAttempts = 5

// MaxIndexRefreshAttempts bounds the optimistic-concurrency retry on a note
// or capture row around a body edit, for the request path and the worker
// alike.
const MaxIndexRefreshAttempts = 5

// errNoteBusy refuses a cut or an insert on a note that carries another
// capture's stamp which did not clear in noteBusyWait. The row holds one
// stamp, so stamping over a live one would leave that write unguarded.
var errNoteBusy = errors.New("a recording is being added to the note")

// StampNoteForBodyWrite puts captureID's append stamp on the note row under
// its version, re-reading on a lost race as every writer of the row does. It
// is what every body write that is not an editor save does first — the
// worker's append, a move's insert and cut, a delete's cut — because such a
// write moves the body's ETag and not the row's version: without the stamp
// an editor save that read the version before the write and the ETag after
// it passes both checks and stores a body from before the write
// (UpdateNote). The stamp bumps the version and makes UpdateNote refuse until
// the writer's index refresh clears it (RefreshOptions.ClearAppendStampFor)
// or ClearNoteAppend hands it back.
//
// check sees each read of the row before the stamp is written: an error ends
// the attempt, and wait re-reads without stamping, which is how the worker
// sits out another capture's stamp.
func StampNoteForBodyWrite(ctx context.Context, store repository.Store, userID, noteID, captureID string, now func() time.Time, check func(model.NoteIndex) (wait bool, err error)) error {
	for conflicts := 0; ; {
		note, err := store.GetNote(ctx, userID, noteID)
		if err != nil {
			return err
		}
		if wait, err := check(note); err != nil {
			return err
		} else if wait {
			continue
		}
		_, err = store.StampNoteAppend(ctx, userID, noteID, captureID, note.Version, now())
		if err == nil || !errors.Is(err, repository.ErrVersionConflict) {
			return err
		}
		if conflicts++; conflicts >= MaxIndexRefreshAttempts {
			return err
		}
	}
}

// noteBusyWait is how long a delete or a move waits for another capture's
// stamp on the same note to clear before it is refused, and noteBusyPoll how
// often it looks. A stamp is held for a handful of store calls, and the
// Recordings tab deletes a selection as one request per recording, all at
// once: refused outright, every sibling that met the first one's stamp
// answered 409 and the person repeated the delete once per recording. Three
// seconds is far above that span and far below the gateway's 29.
const (
	noteBusyWait = 3 * time.Second
	noteBusyPoll = 100 * time.Millisecond
)

// stampNote is StampNoteForBodyWrite on the request path. Another capture's
// stamp younger than noteBusyWait is a write in flight — a sibling delete or
// move, or the worker's append — and is waited out for at most that long. An
// older one was left by a writer that died or failed before its refresh;
// it is never stamped over (the row holds one stamp, and that write's guard
// would go with it), so the request is refused for the person to repeat.
func (s *CaptureService) stampNote(ctx context.Context, userID, noteID, captureID string) error {
	giveUp := time.Now().Add(noteBusyWait)
	return StampNoteForBodyWrite(ctx, s.store, userID, noteID, captureID, s.now, func(note model.NoteIndex) (bool, error) {
		if note.AppendingCapture == captureID || !AppendInProgress(note, s.now()) {
			return false, nil
		}
		at, err := model.ParseTime(note.AppendingAt)
		if err != nil || s.now().Sub(at) >= noteBusyWait || !time.Now().Before(giveUp) {
			return false, errNoteBusy
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(noteBusyPoll):
			return true, nil
		}
	})
}

// clearNoteStamp hands back a stamp whose body write did not happen. Best
// effort: one left behind expires with the lease.
func (s *CaptureService) clearNoteStamp(ctx context.Context, userID, noteID, captureID string) {
	if err := s.store.ClearNoteAppend(ctx, userID, noteID, captureID); err != nil {
		obs.Log(ctx).Warn("could not clear the note's append stamp; it expires with the lease",
			slog.String("capture_id", captureID),
			slog.String("note_id", noteID),
			slog.String("error", err.Error()))
	}
}

// DeleteCapture removes one recording: its paragraph from the note body, the
// note's derived index fields, every object the capture owns, and finally its
// row. The row goes last so a failure anywhere leaves the capture visible and
// the delete retryable; each earlier step is idempotent, so the retry only
// does what is left.
//
// A second call for the same id is ErrNotFound, which is the 404 the API
// promises. Confirmation is the client's concern; the API has none.
//
// A capture still moving is refused on RetryCapture's rule: while a worker
// can still be writing its row (CaptureStuck says not yet), deleting under
// it would leave the append to re-create what was just cut. Once stuck it
// may go — unless it is an upload whose link could still land its object —
// and before, a recording whose upload never completed sat in
// `uploaded` for good, its "still not done" card undeletable (QA
// 2026-09-24).
func (s *CaptureService) DeleteCapture(ctx context.Context, userID, captureID string) error {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return fmt.Errorf("failed to get capture: %w", err)
	}
	now := s.now()
	if CaptureIsPending(capture.Status) && !CaptureStuck(capture, now) {
		return ErrCaptureInFlight
	}
	// Stuck is not enough for an upload with no object yet: its link outlives
	// the stuck threshold, so another device's delete could land between the
	// recording device's PUT starting and finishing, and the recording would
	// be lost (UploadMayStillLand). Once the object is there the row goes as
	// any stuck one does.
	if capture.Status == model.StatusUploaded && capture.AudioKey != "" && UploadMayStillLand(capture, now) {
		present, err := s.objects.Exists(ctx, capture.AudioKey)
		if err != nil {
			return fmt.Errorf("failed to check the recording's audio: %w", err)
		}
		if !present {
			return ErrCaptureInFlight
		}
	}

	// The note whose body this delete changed, once the paragraph is out, so
	// its cleaned view can be regenerated after the row is gone.
	var touched *model.NoteIndex
	if capture.NoteID != "" {
		note, err := s.store.GetNote(ctx, userID, capture.NoteID)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			// The note was purged from under the capture; there is no body to
			// cut and no index to refresh.
		case err != nil:
			return fmt.Errorf("failed to get note: %w", err)
		default:
			refreshed, err := s.cutFromNote(ctx, userID, note, capture)
			if err != nil {
				return err
			}
			touched = refreshed
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

	// The paragraph is out of the body, so the cleaned view is regenerated if
	// the note asks for it — asynchronously; the request path never runs the
	// model.
	if touched != nil {
		autoCleanAfterBodyWrite(ctx, s.store, s.worker, userID, *touched)
	}
	return nil
}

// cutFromNote takes capture's paragraph out of note's body and re-derives the
// index, returning the row as refreshed, or nil when the note was not
// touched: a capture that never reached the body has nothing there.
//
// The row is stamped before the cut and the refresh clears the stamp, as an
// append does, so an editor save cannot land between the two and write the
// paragraph back from a draft that still holds it. The refresh runs whenever
// the paragraph was there, the capture was appended or the row still carries
// this capture's stamp, not only when this call cut: a delete whose refresh
// failed is repeated, finds nothing left to cut, and must still take the
// words out of the search text and clear the stamp the first attempt left —
// whatever the capture's status, since a recording whose retranscription
// failed keeps its paragraph.
func (s *CaptureService) cutFromNote(ctx context.Context, userID string, note model.NoteIndex, capture model.CaptureIndex) (*model.NoteIndex, error) {
	body, err := s.objects.Get(ctx, note.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("failed to read the note body: %w", err)
	}
	if HasCaptureMarker(string(body), capture.ID) {
		if err := s.stampNote(ctx, userID, note.ID, capture.ID); err != nil {
			if errors.Is(err, errNoteBusy) {
				return nil, ErrAppendInProgress
			}
			return nil, fmt.Errorf("failed to stamp the note for the cut: %w", err)
		}
		if _, err := RewriteNoteBody(ctx, s.objects, note.S3MarkdownKey, func(body string) (string, bool) {
			rest, _, found := CutCaptureParagraph(body, capture.ID)
			return rest, found
		}); err != nil {
			s.clearNoteStamp(ctx, userID, note.ID, capture.ID)
			return nil, fmt.Errorf("failed to remove the paragraph from the note: %w", err)
		}
	} else if capture.Status != model.StatusAppended && note.AppendingCapture != capture.ID {
		return nil, nil
	}
	refreshed, err := RefreshNoteIndex(ctx, s.store, s.objects, userID, note.ID, RefreshOptions{ClearAppendStampFor: capture.ID})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh the note index: %w", err)
	}
	return &refreshed, nil
}

// RewriteNoteBody applies edit to the body stored at key under an ETag
// condition, re-reading and re-applying on a lost race. edit returns the new
// body and whether there is anything to write; a body it declines to change
// is not written and written is false. A missing object is an empty body.
//
// It is the one conditional body-rewrite loop: the delete and move paths here
// and the worker's append (pipeline.appendToNote) all go through it, so the
// retry bound and the backoff are decided once and a fix to the protocol lands
// on every writer.
func RewriteNoteBody(ctx context.Context, objects repository.Objects, key string, edit func(body string) (string, bool)) (written bool, err error) {
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

// RefreshOptions are what a refresh after a stamped body write needs beyond
// the editor's: a clock the worker can pin, the stamp it is clearing, and a
// body the worker will not guess at. The zero value is the editor's refresh.
type RefreshOptions struct {
	// Now supplies the touch time; nil is the wall clock.
	Now func() time.Time
	// ClearAppendStampFor names the capture whose append stamp the refresh
	// clears, in the same PutNote that publishes the paragraph's snippet and
	// search text, so the row's version is once more a witness of the body
	// and editor saves may resume. Only that capture's own stamp is cleared:
	// two captures appending to one note stamp in turn, and the second's body
	// write may still be in flight when the first refreshes. Empty clears
	// nothing.
	ClearAppendStampFor string
	// RequireBody makes a body that cannot be read an error rather than an
	// empty body. The editor's paths index a missing body as empty because a
	// note whose paragraph was just cut may have none left; the worker has
	// just written a paragraph, so a read that fails is a fault, not an empty
	// note. Until 2026-09 the worker guessed instead — one S3 5xx at this
	// moment rewrote the note's search text and snippet to the paragraph
	// alone, the capture was marked appended, nothing retried, and search,
	// the offline corpus and Ask lost the rest of the note until the next
	// body write. Failing the invocation lets the retry find the marker in
	// the body and finish from here, so nothing is written twice.
	RequireBody bool
}

// RefreshNoteIndex re-derives the index fields that follow the body — snippet,
// search text, touch time, and the cleaned view's stale flag — from the body
// now in object storage, under the note's version. The body is authoritative,
// so a version conflict is answered by re-reading rather than by overwriting
// whoever won. It is the one re-indexer: the editor's paths call it with the
// zero RefreshOptions, the worker (pipeline.finishAppend) with its extras. It
// returns the note as stored so the caller can act on what it carries
// (auto_clean).
func RefreshNoteIndex(ctx context.Context, store repository.Store, objects repository.Objects, userID, noteID string, opts RefreshOptions) (model.NoteIndex, error) {
	var lastErr error
	for attempt := 0; attempt < MaxIndexRefreshAttempts; attempt++ {
		note, err := store.GetNote(ctx, userID, noteID)
		if err != nil {
			return model.NoteIndex{}, err
		}
		body, err := objects.Get(ctx, note.S3MarkdownKey)
		switch {
		case err == nil:
		case opts.RequireBody:
			return model.NoteIndex{}, fmt.Errorf("read note body: %w", err)
		case !errors.Is(err, repository.ErrNotFound):
			return model.NoteIndex{}, err
		}
		note.Snippet = Snippet(string(body))
		note.SearchText = SearchText(string(body))
		if opts.Now != nil {
			note.UpdatedAt = model.FormatTime(opts.Now())
		} else {
			note.UpdatedAt = model.Now()
		}
		MarkCleanedStale(&note)
		if opts.ClearAppendStampFor != "" && note.AppendingCapture == opts.ClearAppendStampFor {
			note.AppendingCapture, note.AppendingAt = "", ""
		}

		stored, err := store.PutNote(ctx, userID, note)
		if err == nil {
			return stored, nil
		}
		if !errors.Is(err, repository.ErrVersionConflict) {
			return model.NoteIndex{}, err
		}
		lastErr = err
	}
	return model.NoteIndex{}, lastErr
}

// captureObjectKeys lists every object a capture may own, whether or not the
// row records it.
//
// Every key derivable from the id is unlinked too (keys.CaptureDerived), not
// only the ones the row names: the worker clears CaptureIndex.PeaksKey when
// the client had not uploaded peaks by the time the pipeline finished
// (pipeline.verifyPeaks), and a Transcribe again clears the transcript keys
// until the new ones are written, so a delete in between left the objects
// behind. Deleting a key that names nothing is a no-op.
func captureObjectKeys(userID string, c model.CaptureIndex) []string {
	return append([]string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey, c.SegmentsKey, c.PeaksKey},
		keys.CaptureDerived(userID, c.ID)...)
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
