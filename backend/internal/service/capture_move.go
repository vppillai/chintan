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
	// ErrMoveIncomplete means the move failed before it finished: the source
	// note still holds the paragraph, the capture still points at it, and the
	// request can simply be repeated. The paragraph is inserted into the
	// target before it is cut from the source, so no failure takes it out of
	// both; the copy a failed move may leave in the target is removed where
	// the target can still be written, and a repeat replaces it otherwise.
	ErrMoveIncomplete = errors.New("capture move did not complete; nothing changed")
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
// is no transaction across them, so the order is what stands in for one: the
// paragraph is copied into the target first and only then cut from the
// source. A process killed between the two writes — a Lambda timeout, say —
// leaves the text in both notes, never in neither, and the source is still
// the one the capture points at. A repeat then finds the paragraph in the
// source, replaces the target's copy with it, and finishes the cut. A write
// that fails in-process is compensated instead: while the source still holds
// the paragraph (read back, since a write that reported a fault may have
// landed) the target's copy is removed and ErrMoveIncomplete says the
// request can be repeated. Everything after the
// cut — the index refreshes, the row — is idempotent, so a failure there
// leaves a state a retry finishes: the source has nothing to copy, the target
// already holds the paragraph, and the rest runs again.
func (s *CaptureService) MoveCapture(ctx context.Context, userID, captureID, targetNoteID string) (capture *model.CaptureIndex, moved bool, err error) {
	current, err := s.movableCapture(ctx, userID, captureID)
	if err != nil {
		return nil, false, err
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
	capture, err = s.moveInto(ctx, userID, current, target)
	if err != nil {
		return nil, false, err
	}
	return capture, true, nil
}

// MoveCaptureToNewNote is MoveCapture into a note that does not exist yet: the
// note is created with title, empty, and the recording moves into it, so its
// paragraph is the new note's first. The capture is checked before the note is
// made, so a refusal leaves no empty note behind; and a capture cannot already
// be in a note that did not exist, so there is no no-op answer.
//
// A move that fails is compensated (discardUnusedNote), because the note has
// a fresh id on every call and a 5xx answer is not replayed under its
// Idempotency-Key: the client repeats the request, and each attempt would
// otherwise leave one more empty note with the title behind an answer that
// says nothing changed.
func (s *CaptureService) MoveCaptureToNewNote(ctx context.Context, userID, captureID, title string) (*model.CaptureIndex, error) {
	current, err := s.movableCapture(ctx, userID, captureID)
	if err != nil {
		return nil, err
	}
	if s.notes == nil {
		return nil, ErrNoteCreationUnavailable
	}
	note, err := s.notes.CreateNote(ctx, userID, title, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create note: %w", err)
	}
	moved, err := s.moveInto(ctx, userID, current, note)
	if err != nil {
		s.discardUnusedNote(ctx, userID, captureID, note, err)
		return nil, err
	}
	return moved, nil
}

// discardUnusedNote is the compensation for a move into a note made for it
// that failed. The note's body decides, read back rather than inferred from
// where the move stopped, since a write that reported a fault may still have
// landed. Empty — true of every ErrMoveIncomplete whose compensation could
// write the note — means nothing reached it, so it is removed and "nothing
// changed" is true again. Non-empty means the paragraph is in it, and after
// the cut the note is the only copy of the text, so it stays either way; it
// is logged by id (ids are not user content) for the operator, because a
// repeat of the request cannot find it and moves the capture into a second
// note.
func (s *CaptureService) discardUnusedNote(ctx context.Context, userID, captureID string, note model.NoteIndex, cause error) {
	log := obs.Log(ctx).With(
		slog.String("capture_id", captureID),
		slog.String("note_id", note.ID),
		slog.String("error", cause.Error()))
	body, err := s.objects.Get(ctx, note.S3MarkdownKey)
	switch {
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		log.Error("capture move into a new note failed and the note could not be read; it is kept",
			slog.String("read_error", err.Error()))
	case len(body) > 0:
		log.Error("capture move into a new note failed after the paragraph landed; the note is kept")
	default:
		if err := s.notes.DiscardNote(ctx, userID, note); err != nil {
			log.Error("capture move into a new note failed and the empty note could not be removed",
				slog.String("discard_error", err.Error()))
		}
	}
}

// movableCapture is the capture a move starts from: one the caller owns, with
// a note to move from, that the worker is no longer writing.
func (s *CaptureService) movableCapture(ctx context.Context, userID, captureID string) (model.CaptureIndex, error) {
	current, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return model.CaptureIndex{}, fmt.Errorf("failed to get capture: %w", err)
	}
	if current.NoteID == "" {
		return model.CaptureIndex{}, ErrCaptureUnfiled
	}
	if CaptureIsPending(current.Status) {
		return model.CaptureIndex{}, ErrCaptureInFlight
	}
	return current, nil
}

// moveInto is the move itself: current's paragraph out of its note and into
// the active target, then both indexes and the row.
func (s *CaptureService) moveInto(ctx context.Context, userID string, current model.CaptureIndex, target model.NoteIndex) (*model.CaptureIndex, error) {
	captureID, targetNoteID := current.ID, target.ID
	sourceID := current.NoteID
	sourceKey := ""
	switch source, err := s.store.GetNote(ctx, userID, sourceID); {
	case errors.Is(err, repository.ErrNotFound):
		// The source was purged from under the capture. There is no paragraph
		// to carry; the row still moves.
	case err != nil:
		return nil, fmt.Errorf("failed to get source note: %w", err)
	default:
		sourceKey = source.S3MarkdownKey
	}

	// Everything the insert needs is gathered before anything is written, so a
	// failure here changes nothing.
	before, err := s.olderCapturesIn(ctx, userID, targetNoteID, current.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMoveIncomplete, err)
	}

	// 1. Read the paragraph from the source without changing it.
	var text string
	found := false
	if sourceKey != "" {
		body, err := s.objects.Get(ctx, sourceKey)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: failed to read the source body: %w", ErrMoveIncomplete, err)
		}
		_, text, found = CutCaptureParagraph(string(body), captureID)
	}

	if found {
		// 2. Copy it into the target. A copy already there is left by a move
		// that stopped between the writes; the source's text replaces it,
		// since the source is the note the user has been editing since.
		_, err = RewriteNoteBody(ctx, s.objects, target.S3MarkdownKey, func(body string) (string, bool) {
			rest, _, _ := CutCaptureParagraph(body, captureID)
			next := InsertCaptureParagraph(rest, captureID, text, before)
			return next, next != body
		})
		if err != nil {
			return nil, s.undoInsert(ctx, target.S3MarkdownKey, sourceID, current, err)
		}

		// 3. Cut it from the source. The paragraph must still read as what
		// was copied: an edit that landed in between would otherwise be cut
		// away with the only copy of the new words in the source.
		edited := false
		_, err = RewriteNoteBody(ctx, s.objects, sourceKey, func(body string) (string, bool) {
			rest, now, ok := CutCaptureParagraph(body, captureID)
			if ok && now != text {
				edited = true
				return body, false
			}
			return rest, ok
		})
		if err == nil && edited {
			err = errors.New("the paragraph was edited during the move")
		}
		if err != nil {
			// A write that reported a fault may still have landed, and then
			// the target's copy is the only one: the source is read back, and
			// the copy is removed only while the source still holds the
			// paragraph. A source that no longer does means the cut went
			// through, so the move carries on; one that cannot be read keeps
			// the copy, since a duplicate is recoverable and a loss is not,
			// and a repeat finds the source empty and finishes the move.
			switch holds, rerr := s.sourceHolds(ctx, sourceKey, captureID); {
			case rerr != nil:
				obs.Log(ctx).Error("capture move failed at the source cut and the source could not be read back; the target keeps its copy",
					slog.String("capture_id", captureID),
					slog.String("from_note_id", sourceID),
					slog.String("to_note_id", targetNoteID),
					slog.String("error", err.Error()),
					slog.String("read_error", rerr.Error()))
				return nil, fmt.Errorf("failed to cut the paragraph from the source: %w", err)
			case holds:
				return nil, s.undoInsert(ctx, target.S3MarkdownKey, sourceID, current, err)
			}
			obs.Log(ctx).Warn("the source cut reported a fault but landed; finishing the move",
				slog.String("capture_id", captureID),
				slog.String("error", err.Error()))
		}
	}

	// 4. Both indexes follow their bodies, then the row follows the paragraph.
	// The row is last so a capture never claims a note its paragraph is not
	// in yet; a retry after a failure here re-runs exactly these steps.
	var touched []model.NoteIndex
	if sourceKey != "" {
		refreshed, err := RefreshNoteIndex(ctx, s.store, s.objects, userID, sourceID, RefreshOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to refresh the source note index: %w", err)
		}
		touched = append(touched, refreshed)
	}
	refreshed, err := RefreshNoteIndex(ctx, s.store, s.objects, userID, targetNoteID, RefreshOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh the target note index: %w", err)
	}
	touched = append(touched, refreshed)
	updated, err := s.repointCapture(ctx, userID, captureID, targetNoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to re-point the capture: %w", err)
	}

	// Both bodies changed, so both cleaned views are regenerated where asked
	// for (D2: "structured mode re-runs on both notes") — asynchronously, and
	// only now that the row agrees with the paragraph.
	for _, n := range touched {
		autoCleanAfterBodyWrite(ctx, s.store, s.worker, userID, n)
	}

	obs.Log(ctx).Info("capture moved",
		slog.String("capture_id", captureID),
		slog.String("from_note_id", sourceID),
		slog.String("to_note_id", targetNoteID),
		slog.Bool("paragraph_moved", found))
	obs.Count(ctx, "CapturesMoved", map[string]string{"Stage": string(current.Status)})
	return &updated, nil
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

// sourceHolds reports whether the source body still carries captureID's
// marker, read back after a cut whose outcome is unknown.
func (s *CaptureService) sourceHolds(ctx context.Context, sourceKey, captureID string) (bool, error) {
	body, err := s.objects.Get(ctx, sourceKey)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return HasCaptureMarker(string(body), captureID), nil
}

// undoInsert is the compensation for a move that failed after it may have
// written the target: the target's copy of the paragraph is removed, so the
// source is once more its only home and the caller reports a move that
// changed nothing. If the target cannot be written either, the copy stays —
// a duplicate, not a loss, since the source was never cut — and a repeat of
// the request replaces it.
func (s *CaptureService) undoInsert(ctx context.Context, targetKey, sourceID string, c model.CaptureIndex, cause error) error {
	log := obs.Log(ctx).With(
		slog.String("capture_id", c.ID),
		slog.String("from_note_id", sourceID),
		slog.String("error", cause.Error()))
	_, rerr := RewriteNoteBody(ctx, s.objects, targetKey, func(body string) (string, bool) {
		rest, _, found := CutCaptureParagraph(body, c.ID)
		return rest, found
	})
	if rerr != nil {
		log.Warn("capture move failed and the target's copy could not be removed; a retry replaces it",
			slog.String("restore_error", rerr.Error()))
	} else {
		log.Warn("capture move failed; the source note still holds the paragraph")
	}
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
		// CreatedNote reports what the router or the needs_target answer did,
		// which a person's move supersedes: a recording routed into a new note
		// and then moved must not still say "Started" of its new home. A move
		// into a new note reads as filed too, which is the harmless side.
		c.CreatedNote = false
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
