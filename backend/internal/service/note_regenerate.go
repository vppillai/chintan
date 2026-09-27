package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Regenerating a note from its recordings (docs/design/regenerate.md).
//
// The prompts change, and a note made before the change keeps the words the
// old prompt produced. Regeneration re-runs the one stage that depends on a
// prompt over every recording the note holds — the cleanup for a plain note,
// the item extraction for a checklist — from the transcript that is already
// stored, and the worker puts each recording's new words where its old ones
// stand. Nothing is transcribed again: that is the per-recording "Transcribe
// again" (RetranscribeCapture). The whole-note cleaned view follows when the
// note has one, and the title is not touched.
//
// The request path does what RetranscribeCapture does for one recording, for
// every recording that qualifies: each row goes back to `transcribed` with its
// clean artefact and its append claim cleared, and the worker is handed the
// note and the ids. The captures' own statuses are the whole of the state —
// the strip on the note shows them moving, the note's poll follows them, and
// a second request while any is non-terminal is refused — so there is no
// note-level flag to keep in step.

// ErrRegenerateInFlight refuses a regeneration while one is already running
// or a recording is still being filed into the note: both leave a capture of
// the note non-terminal, and a second run over the same rows would race the
// first for their conditional writes.
var ErrRegenerateInFlight = errors.New("this note is being regenerated, or a recording is being filed into it; wait for it to finish")

// MaxRegenerateCaptures bounds one regeneration. It is MaxRecordingURLs for
// the same reason: the request path reads every row and rewrites each one it
// resets, and two hundred conditional writes fit the gateway's ceiling with
// room to spare.
//
// ponytail: a note with more recordings than this regenerates its newest two
// hundred, which is the order the store lists them in; a second request once
// those have landed takes nothing further, since they are appended again. A
// cursor across requests can come when a note that large exists.
const MaxRegenerateCaptures = 200

// RegenerableCaptures lists the recordings of note whose words a regeneration
// would replace, oldest first, or ErrRegenerateInFlight when the note cannot
// take one now. It is the one rule the API's request path and the worker's
// own selection (an operator's `chintanctl regenerate`, which resets nothing
// on the way in) both apply, so the two roads regenerate the same recordings.
//
// A recording qualifies when it is appended and has a transcript to work
// from. A verbatim note qualifies none: its cleanup is bypassed, so no prompt
// had a hand in its words. For a plain note the recording's paragraph must
// still be under its marker: a marker the editor carried to the end of the
// body says the person rewrote those words into their own, and replacing
// what is no longer there would land a second copy beside the edit. A
// checklist's items are found by their words instead (pipeline.append), so a
// list that has been ticked or reordered — every save from the Items tab
// carries the markers — still regenerates.
func RegenerableCaptures(ctx context.Context, store repository.Store, objects repository.Objects, userID string, note model.NoteIndex, now time.Time) ([]model.CaptureIndex, error) {
	if AppendInProgress(note, now) {
		return nil, ErrRegenerateInFlight
	}
	// The listing is GSI1's projection — status, the keys, the times of the
	// capture — and not the whole row: it carries no last_progress_at to
	// judge a stuck capture by, and a row written back from it would drop
	// the language, the source and the timing record. So the listing picks
	// the candidates and each is read whole before it is judged or returned.
	listed, err := repository.DrainPages(ctx, MaxRegenerateCaptures, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return store.ListCapturesByNote(ctx, userID, note.ID, opts)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list captures: %w", err)
	}
	whole := func(c model.CaptureIndex) (model.CaptureIndex, error) {
		full, err := store.GetCapture(ctx, userID, c.ID)
		if err != nil {
			return model.CaptureIndex{}, fmt.Errorf("failed to get capture: %w", err)
		}
		return full, nil
	}
	for _, c := range listed {
		if model.IsTerminalStatus(c.Status) {
			continue
		}
		full, err := whole(c)
		if err != nil {
			return nil, err
		}
		if !model.IsTerminalStatus(full.Status) && !CaptureStuck(full, now) {
			return nil, ErrRegenerateInFlight
		}
	}
	if note.Verbatim {
		return nil, nil
	}
	raw, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("failed to read the note body: %w", err)
	}
	body := string(raw)

	var out []model.CaptureIndex
	for _, c := range listed {
		if c.Status != model.StatusAppended || c.RawKey == "" {
			continue
		}
		if note.Kind != model.NoteKindChecklist {
			if _, text, found := CutCaptureParagraph(body, c.ID); !found || strings.TrimSpace(text) == "" {
				continue
			}
		}
		full, err := whole(c)
		if err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	// Oldest first: the order the note was dictated in, so a body read while
	// the run is half way through reads as the note did.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ResetForRegenerate puts c where the pipeline resumes from once a transcript
// exists: the clean artefact and the append claim are cleared, as
// RetranscribeCapture clears them, so the worker cleans again, takes a fresh
// claim, finds the marker and replaces. The transcript keys stay, which is
// what keeps the transcription out of it.
func ResetForRegenerate(c *model.CaptureIndex, now time.Time) {
	c.CleanKey = ""
	c.AppendToken, c.AppendClaimedAt, c.AppendedAt = "", 0, 0
	c.Status = model.StatusTranscribed
	c.Error = ""
	c.LastProgressAt = model.FormatTime(now)
}

// RequestRegenerate resets every regenerable recording of noteID and hands
// the note to the worker, returning how many it handed over. Zero means
// nothing qualified and nothing was queued. It calls no provider: the run is
// the worker's (pipeline.RegenerateNote).
func (s *NotesService) RequestRegenerate(ctx context.Context, userID, noteID string) (int, error) {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return 0, err
	}
	if !NoteIsActive(note) {
		return 0, ErrNoteArchived
	}
	if s.worker == nil {
		return 0, ErrCaptureWorkerUnavailable
	}
	now := s.now()
	captures, err := RegenerableCaptures(ctx, s.store, s.objects, userID, note, now)
	if err != nil {
		return 0, err
	}
	if len(captures) == 0 {
		return 0, nil
	}

	ids := make([]string, 0, len(captures))
	for _, c := range captures {
		ResetForRegenerate(&c, now)
		if _, err := s.store.PutCapture(ctx, c); err != nil {
			// The rows reset so far are left at transcribed. That is the
			// state RetranscribeCapture leaves one row in when its hand-off
			// fails, and the same way out: fifteen minutes on, the strip
			// offers Retry, and Retry resumes each from its transcript.
			return 0, fmt.Errorf("failed to reset capture for regeneration: %w", err)
		}
		ids = append(ids, c.ID)
	}
	if err := s.worker.InvokeRegenerateNote(ctx, userID, noteID, ids); err != nil {
		// A refused Invoke is a transient the person will retry in a moment,
		// and a retry would be met with ErrRegenerateInFlight for a quarter
		// of an hour if the rows stayed reset. Put them back, best effort.
		s.restoreAfterFailedHandOff(ctx, userID, captures)
		return 0, fmt.Errorf("failed to hand the note to the worker: %w", err)
	}
	obs.Log(ctx).Info("note regeneration handed to the worker",
		slog.String("note_id", noteID),
		slog.Int("captures", len(ids)))
	obs.Count(ctx, "NoteRegenerateRequested", map[string]string{"Trigger": "user"})
	return len(ids), nil
}

// restoreAfterFailedHandOff writes each capture's pre-reset row back over the
// reset one, under the version the reset left, so the note is where it was
// before the request. A row that cannot be restored is logged and left for
// Retry, as a failed reset leaves it.
func (s *NotesService) restoreAfterFailedHandOff(ctx context.Context, userID string, before []model.CaptureIndex) {
	for _, prior := range before {
		current, err := s.store.GetCapture(ctx, userID, prior.ID)
		if err == nil {
			restored := prior
			restored.Version = current.Version
			_, err = s.store.PutCapture(ctx, restored)
		}
		if err != nil {
			obs.Log(ctx).Warn("could not restore a capture after a failed regeneration hand-off; Retry on its row resumes it",
				slog.String("capture_id", prior.ID),
				slog.String("error", err.Error()))
		}
	}
}
