package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// afterRead runs fn once, the first time a caller has read the note: inside
// its read-modify-write window, holding the version it read. That is the one
// instant PutNote's stamp pin can fire for a write that never touched the
// stamp, and the instant CleanedPanel's saveNow()+regenerate() pair hits every
// time (review 2026-09-21, T12 re-review).
type afterRead struct {
	repository.Store
	fn    func(ctx context.Context, tenantID string, note model.NoteIndex)
	fired bool
}

func (s *afterRead) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	//nolint:staticcheck // s.Store.X is this wrapper's "call the real store" idiom.
	note, err := s.Store.GetNote(ctx, tenantID, noteID)
	if err == nil && !s.fired {
		s.fired = true
		s.fn(ctx, tenantID, note)
	}
	return note, err
}

// The three service writes that read a row and put it back whole, each driven
// from the copy it read.
var wholeRowWrites = []struct {
	name     string
	archived bool // the note is archived before the write, so RestoreNote has something to do
	write    func(ctx context.Context, n *NotesService, note model.NoteIndex) (model.NoteIndex, error)
	landed   func(model.NoteIndex) bool
}{
	{
		name: "UpdateNote",
		write: func(ctx context.Context, n *NotesService, note model.NoteIndex) (model.NoteIndex, error) {
			v, title := note.Version, "Renamed while the tap landed"
			return n.UpdateNote(ctx, "u", note.ID, NoteUpdates{Title: &title, ExpectedVersion: &v})
		},
		landed: func(n model.NoteIndex) bool { return n.Title == "Renamed while the tap landed" },
	},
	{
		name: "ArchiveNote",
		write: func(ctx context.Context, n *NotesService, note model.NoteIndex) (model.NoteIndex, error) {
			return n.ArchiveNote(ctx, "u", note.ID)
		},
		landed: func(n model.NoteIndex) bool { return !NoteIsActive(n) },
	},
	{
		name:     "RestoreNote",
		archived: true,
		write: func(ctx context.Context, n *NotesService, note model.NoteIndex) (model.NoteIndex, error) {
			return n.RestoreNote(ctx, "u", note.ID)
		},
		landed: NoteIsActive,
	},
}

// A Clean tap that lands between a save's read and its write does not conflict
// with the save — the tap wants the stamp, the save wants everything else — so
// the save goes through carrying the stamp, and nobody is shown a conflict
// prompt between two identical copies of the text.
func TestAWholeRowWriteThatCrossesACleanTapCarriesTheStamp(t *testing.T) {
	const at = "2026-09-21T10:00:00.000000000Z"
	for _, tc := range wholeRowWrites {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := memory.NewStore()
			objects := memory.NewObjects()
			note, err := NewNotesService(base, objects).CreateNote(ctx, "u", "Roof", nil)
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			if tc.archived {
				if note, err = NewNotesService(base, objects).ArchiveNote(ctx, "u", note.ID); err != nil {
					t.Fatalf("ArchiveNote: %v", err)
				}
			}
			tap := &afterRead{Store: base, fn: func(ctx context.Context, tenantID string, read model.NoteIndex) {
				if _, err := base.StampCleanRequest(ctx, tenantID, read.ID, model.NoteCleanPolished, at, read.Version); err != nil {
					t.Fatalf("StampCleanRequest: %v", err)
				}
			}}

			got, err := tc.write(ctx, NewNotesService(tap, objects), note)
			if err != nil {
				t.Fatalf("%s across a Clean tap: %v; want the write to land", tc.name, err)
			}
			if !tap.fired {
				t.Fatal("the tap never fired; the test did not exercise the window")
			}
			stored, err := base.GetNote(ctx, "u", note.ID)
			if err != nil {
				t.Fatalf("GetNote: %v", err)
			}
			if stored.CleanedRequestedAt != at || stored.CleanedRequestedMode != model.NoteCleanPolished {
				t.Errorf("the write erased the Clean tap: stamp %q mode %q", stored.CleanedRequestedAt, stored.CleanedRequestedMode)
			}
			if !tc.landed(stored) {
				t.Errorf("the write did not land: %+v", stored)
			}
			if stored.Version != note.Version+1 || got.Version != stored.Version {
				t.Errorf("version stored %d returned %d; want %d", stored.Version, got.Version, note.Version+1)
			}
		})
	}
}

// A whole-row write from somebody else in the same window is a real conflict,
// still reported as one — and the row handed back with it is the one now
// stored, so the 409 names the version the client must reconcile against.
func TestAWholeRowWriteThatLosesToAnotherWriteReportsTheStoredVersion(t *testing.T) {
	for _, tc := range wholeRowWrites {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := memory.NewStore()
			objects := memory.NewObjects()
			note, err := NewNotesService(base, objects).CreateNote(ctx, "u", "Roof", nil)
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			if tc.archived {
				if note, err = NewNotesService(base, objects).ArchiveNote(ctx, "u", note.ID); err != nil {
					t.Fatalf("ArchiveNote: %v", err)
				}
			}
			other := &afterRead{Store: base, fn: func(ctx context.Context, tenantID string, read model.NoteIndex) {
				read.Tags = []string{"from-another-tab"}
				if _, err := base.PutNote(ctx, tenantID, read); err != nil {
					t.Fatalf("PutNote(other): %v", err)
				}
			}}

			got, err := tc.write(ctx, NewNotesService(other, objects), note)
			if !errors.Is(err, repository.ErrVersionConflict) {
				t.Fatalf("%s against another write: err = %v; want ErrVersionConflict", tc.name, err)
			}
			if got.Version != note.Version+1 {
				t.Errorf("conflict carried version %d; want the stored %d", got.Version, note.Version+1)
			}
			stored, err := base.GetNote(ctx, "u", note.ID)
			if err != nil {
				t.Fatalf("GetNote: %v", err)
			}
			if len(stored.Tags) != 1 || stored.Tags[0] != "from-another-tab" {
				t.Errorf("the losing write overwrote the winner: %+v", stored)
			}
		})
	}
}
