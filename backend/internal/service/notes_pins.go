package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Pin errors. The sentences reach the user as written.
var (
	// ErrPinLimit refuses the pin that would be one past model.MaxPinnedNotes.
	ErrPinLimit = errors.New("you can pin up to fifty notes")
	// ErrPinReorderInvalid refuses a reorder naming a note that is not the
	// caller's, not pinned, or named twice.
	ErrPinReorderInvalid = errors.New("every id must be one of your pinned notes")
	// ErrPinBatchSize refuses a reorder of no notes or of more than can be
	// pinned.
	ErrPinBatchSize = errors.New("ids must name between 1 and 50 notes")
)

// countPinned is how many of the tenant's active notes are pinned, and the
// rank a new pin takes to land last: one step past the highest in use, which
// is count × PinRankStep while the ranks are compact and more once an unpin
// has left a gap. Count × step would then equal an existing rank, and the
// order's tie-break (the more recently pinned first) would put the new pin
// above that note instead of below it. Zero when nothing is pinned.
func (s *NotesService) countPinned(ctx context.Context, userID string) (count int, nextRank int64, err error) {
	notes, _, err := s.store.DrainNotes(ctx, userID, repository.DrainOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to list notes: %w", err)
	}
	for _, n := range notes {
		if n.Pinned() {
			count++
			nextRank = max(nextRank, n.PinRank+model.PinRankStep)
		}
	}
	return count, nextRank, nil
}

// ReorderPins writes the listed notes' pin_rank as their position in ids,
// PinRankStep apart, and returns them in that order. Every id must name one
// of the caller's pinned, active notes, once; anything else refuses the whole
// request, since a partial reorder is not an order anyone asked for. One
// request per drag.
//
// Each note is read whole before it is written: a listed note carries no
// search_text or cleaned_body, and PutNote writes the row it is given, so a
// note put from a list projection would lose both. A note already at its
// rank is not rewritten, so a drag that moves one note writes one row.
func (s *NotesService) ReorderPins(ctx context.Context, userID string, ids []string) ([]model.NoteIndex, error) {
	if len(ids) == 0 || len(ids) > model.MaxPinnedNotes {
		return nil, ErrPinBatchSize
	}
	seen := make(map[string]bool, len(ids))
	notes := make([]model.NoteIndex, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, ErrPinReorderInvalid
		}
		seen[id] = true
		note, err := s.store.GetNote(ctx, userID, id)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrPinReorderInvalid
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get note: %w", err)
		}
		if !note.Pinned() || !NoteIsActive(note) {
			return nil, ErrPinReorderInvalid
		}
		notes = append(notes, note)
	}
	out := make([]model.NoteIndex, 0, len(notes))
	for i, note := range notes {
		stored, err := s.putPinRank(ctx, userID, note, int64(i)*model.PinRankStep)
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	return out, nil
}

// putPinRank writes rank onto note, re-reading it and writing again when its
// version moved underneath. A body save or the worker's append stamp landing
// on a pinned note mid-drag is not a reason to stop part-way through the
// order — the rank is independent of whatever else changed — and stopping
// left the earlier notes moved and the later ones not, behind a 409 the
// client's rollback did not match (review 2026-09-24 R4-9). A note that keeps
// moving is still that conflict; one unpinned or archived meanwhile is refused
// as the validation before the loop would have refused it. A note already at
// its rank is not rewritten, so a drag that moves one note writes one row.
//
// The row a conflict hands back is the one now stored (putCarryingStamp read
// it to tell a stamp from a moved version), so the next attempt starts from
// it rather than reading again. A returned row whose version did not move is
// the exception: it is the copy just offered, carrying the rank as if it had
// landed, because the read inside failed or a second writer got in behind it;
// the winner is unknown, and the conflict is reported as it stands.
func (s *NotesService) putPinRank(ctx context.Context, userID string, note model.NoteIndex, rank int64) (model.NoteIndex, error) {
	var err error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		if attempt > 0 && (!note.Pinned() || !NoteIsActive(note)) {
			return model.NoteIndex{}, ErrPinReorderInvalid
		}
		if note.PinRank == rank {
			return note, nil
		}
		note.PinRank = rank
		var stored model.NoteIndex
		stored, err = s.putCarryingStamp(ctx, userID, note)
		if !errors.Is(err, repository.ErrVersionConflict) {
			return stored, err
		}
		if stored.Version == note.Version {
			return model.NoteIndex{}, err
		}
		note = stored
	}
	return model.NoteIndex{}, err
}
