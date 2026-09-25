package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// movingVersionStore is a store on which another writer lands between
// ReorderPins's read of a note and its write: for the ids it is told to move,
// each PutNote first writes the row as it stands (moving the version, as the
// worker's append stamp does) and then hands the caller's stale copy on, which
// conflicts. moves says how many times a given id moves; a negative count
// moves it every time.
type movingVersionStore struct {
	repository.Store
	moves map[string]int
}

func (s *movingVersionStore) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	if left := s.moves[n.ID]; left != 0 {
		if left > 0 {
			s.moves[n.ID] = left - 1
		}
		fresh, err := s.Store.GetNote(ctx, tenantID, n.ID)
		if err != nil {
			return model.NoteIndex{}, err
		}
		if _, err := s.Store.PutNote(ctx, tenantID, fresh); err != nil {
			return model.NoteIndex{}, err
		}
	}
	return s.Store.PutNote(ctx, tenantID, n)
}

// A version that moves under a note while its rank is being written — a body
// save or the worker's stamp landing on a pinned note mid-drag — is re-read
// and the rank written again, so the order lands whole; before, the loop
// stopped at the first conflict with the earlier notes moved and the later
// ones not (review 2026-09-24 R4-9). A note that keeps moving is the conflict
// it always was.
func TestReorderPinsRidesOutAVersionMove(t *testing.T) {
	cases := map[string]struct {
		moves   map[string]int
		wantErr error
	}{
		"one note moves once":   {moves: map[string]int{"b": 1}},
		"every note moves once": {moves: map[string]int{"a": 1, "b": 1, "c": 1}},
		"a note keeps moving":   {moves: map[string]int{"b": -1}, wantErr: repository.ErrVersionConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memory.NewStore()
			objects := memory.NewObjects()
			plain := NewNotesService(store, objects)
			ids := map[string]string{}
			for _, title := range []string{"a", "b", "c"} {
				n, err := plain.CreateNote(ctx, "u1", title, nil)
				if err != nil {
					t.Fatalf("CreateNote: %v", err)
				}
				pinned := true
				if _, err := plain.UpdateNote(ctx, "u1", n.ID, NoteUpdates{Pinned: &pinned}); err != nil {
					t.Fatalf("pin %s: %v", title, err)
				}
				ids[title] = n.ID
			}
			moves := map[string]int{}
			for title, n := range tc.moves {
				moves[ids[title]] = n
			}
			racing := NewNotesService(&movingVersionStore{Store: store, moves: moves}, objects)

			order := []string{ids["c"], ids["a"], ids["b"]}
			out, err := racing.ReorderPins(ctx, "u1", order)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReorderPins = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			for i, id := range order {
				want := int64(i) * model.PinRankStep
				if out[i].ID != id || out[i].PinRank != want {
					t.Errorf("out[%d] = %s rank %d, want %s rank %d", i, out[i].ID, out[i].PinRank, id, want)
				}
				stored, err := store.GetNote(ctx, "u1", id)
				if err != nil {
					t.Fatal(err)
				}
				if stored.PinRank != want {
					t.Errorf("stored %s rank = %d, want %d", id, stored.PinRank, want)
				}
			}
		})
	}
}
