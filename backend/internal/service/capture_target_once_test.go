package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// failTargetWriteOnce fails the capture row write that records a chosen
// target, once: the API dying, or answering a 5xx, after the note was made.
type failTargetWriteOnce struct {
	repository.Store
	fired bool
}

var errInducedTargetCrash = errors.New("induced crash after the note was created")

func (s *failTargetWriteOnce) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	if c.NoteID != "" && !s.fired {
		s.fired = true
		return model.CaptureIndex{}, errInducedTargetCrash
	}
	return s.Store.PutCapture(ctx, c)
}

// A new-title target that crashes between making the note and writing the
// row, then is repeated, ends with one note: the capture's own id names it
// (RoutedNoteID, as routing does since R7-21), so the repeat finds the note
// the first attempt made. Until R9 PR9-26 each attempt made a fresh-id note
// and the first one stayed behind, empty.
func TestSetCaptureTargetNewTitleIsIdempotentAcrossACrash(t *testing.T) {
	ctx := context.Background()
	store := &failTargetWriteOnce{Store: dynamofake.NewStore()}
	objects := memory.NewObjects()
	svc := NewCaptureService(store, objects).
		WithNoteCreator(NewNotesService(store, objects)).
		WithInvoker(&stubInvoker{})
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), RawKey: "tenants/user1/captures/c_1/raw.txt",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetCaptureTarget(ctx, "user1", "c_1", "", "Groceries"); !errors.Is(err, errInducedTargetCrash) {
		t.Fatalf("first attempt err = %v, want the induced crash", err)
	}
	got, err := svc.SetCaptureTarget(ctx, "user1", "c_1", "", "Groceries")
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if got.NoteID != RoutedNoteID("c_1") || !got.CreatedNote {
		t.Fatalf("repeat targeted %q (created=%v), want %q", got.NoteID, got.CreatedNote, RoutedNoteID("c_1"))
	}
	page, err := store.ListNotes(ctx, "user1", repository.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Groceries" {
		t.Fatalf("notes after the repeat = %+v, want exactly the one titled Groceries", page.Items)
	}
}

// A capture is at needs_target most often because its own routed note was
// archived before a retry (route.go); the title typed then must not be a
// dead end, and an active note under the routed id with another title must
// not replace the title typed. Both get a fresh-id note.
func TestSetCaptureTargetNewTitleFallsBackWhenTheRoutedIDIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		own   func(n model.NoteIndex) model.NoteIndex
		notes int
	}{
		{"archived own note", func(n model.NoteIndex) model.NoteIndex { n.DeletedAt = model.Now(); return n }, 1},
		{"active own note under another title", func(n model.NoteIndex) model.NoteIndex { n.Title = "Half-made"; return n }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := dynamofake.NewStore()
			objects := memory.NewObjects()
			notes := NewNotesService(store, objects)
			svc := NewCaptureService(store, objects).WithNoteCreator(notes).WithInvoker(&stubInvoker{})
			if _, err := store.PutCapture(ctx, model.CaptureIndex{
				ID: "c_1", UserID: "user1", Status: model.StatusNeedsTarget,
				CreatedAt: model.Now(), RawKey: "tenants/user1/captures/c_1/raw.txt",
			}); err != nil {
				t.Fatal(err)
			}
			own, err := notes.CreateNoteOnce(ctx, "user1", model.NoteIndex{ID: RoutedNoteID("c_1"), Title: "Groceries"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutNote(ctx, "user1", tc.own(own)); err != nil {
				t.Fatal(err)
			}

			got, err := svc.SetCaptureTarget(ctx, "user1", "c_1", "", "Groceries")
			if err != nil {
				t.Fatalf("SetCaptureTarget: %v", err)
			}
			if got.NoteID == RoutedNoteID("c_1") || !got.CreatedNote {
				t.Fatalf("filed into %q (created=%v), want a fresh note", got.NoteID, got.CreatedNote)
			}
			filed, err := store.GetNote(ctx, "user1", got.NoteID)
			if err != nil || !NoteIsActive(filed) || filed.Title != "Groceries" {
				t.Fatalf("filed note = %+v, %v; want an active note titled Groceries", filed, err)
			}
			page, err := store.ListNotes(ctx, "user1", repository.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != tc.notes {
				t.Fatalf("%d active notes, want %d: %+v", len(page.Items), tc.notes, page.Items)
			}
		})
	}
}
