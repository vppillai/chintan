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

// purgeFixture is a notes service over the fake table and in-memory objects
// with one archived note carrying one capture, and every object both name.
type purgeFixture struct {
	table   *dynamofake.Fake
	store   *repository.DynamoStore
	objects *memory.Objects
	notes   *NotesService
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	table := dynamofake.New()
	store := repository.NewDynamoStore(table, "chintan-test")
	objects := memory.NewObjects()
	return &purgeFixture{table: table, store: store, objects: objects, notes: NewNotesService(store, objects)}
}

// putLegacyCapture stores a capture the way a row written before August 2026
// sits on DynamoDB: readable by its key and by the base-table walk (GetCapture,
// ListCaptures, ListUnindexedCaptures) but carrying no GSI1 keys, so
// ListCapturesByNote never returns it. It is how a test lays down the shape
// that "delete forever" left behind in production; nothing in the application
// writes such a row.
func (f *purgeFixture) putLegacyCapture(t *testing.T, c model.CaptureIndex) {
	t.Helper()
	if _, err := f.store.PutCapture(context.Background(), c); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	f.table.Strip("USER#"+c.UserID, "CAPTURE#"+c.ID, "gsi1pk", "gsi1sk")
}

// archivedNote creates a note, gives it a capture with an audio object, and
// archives it.
func (f *purgeFixture) archivedNote(t *testing.T, id string) model.NoteIndex {
	t.Helper()
	ctx := context.Background()

	note, err := f.notes.CreateNote(ctx, "user1", "Note "+id, nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	audio := "tenants/user1/captures/c_" + id + "/audio.webm"
	if _, err := f.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_" + id, UserID: "user1", NoteID: note.ID,
		Status: model.StatusAppended, CreatedAt: model.Now(), AudioKey: audio,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	if err := f.objects.Put(ctx, audio, []byte("x"), "audio/webm"); err != nil {
		t.Fatalf("Put audio: %v", err)
	}
	archived, err := f.notes.ArchiveNote(ctx, "user1", note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}
	return archived
}

// TestPermanentDeleteUnlinksACaptureTheNoteIndexCannotSee is the production
// defect of 2026-09-05: "delete forever" listed each note's captures through
// GSI1, a capture row written in August 2026 carries no index keys, so thirteen
// filed captures survived the purge of every note and kept answering the
// library's receipts. The delete has to find such a row from the base table.
func TestPermanentDeleteUnlinksACaptureTheNoteIndexCannotSee(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	archived := f.archivedNote(t, "a")

	audio := "tenants/user1/captures/c_legacy/audio.webm"
	f.putLegacyCapture(t, model.CaptureIndex{
		ID: "c_legacy", UserID: "user1", NoteID: archived.ID,
		Status: model.StatusAppended, CreatedAt: "2026-08-07T09:00:00Z", AudioKey: audio,
	})
	if err := f.objects.Put(ctx, audio, []byte("x"), "audio/webm"); err != nil {
		t.Fatalf("Put audio: %v", err)
	}
	if page, _ := f.store.ListCapturesByNote(ctx, "user1", archived.ID, repository.ListOptions{}); len(page.Items) != 1 {
		t.Fatalf("the index lists %d captures, want only the modern one; the legacy row must be invisible to it", len(page.Items))
	}

	if err := f.notes.PermanentlyDeleteNote(ctx, "user1", archived.ID); err != nil {
		t.Fatalf("PermanentlyDeleteNote: %v", err)
	}
	if _, err := f.objects.Get(ctx, audio); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the legacy capture's audio survived the delete (err = %v)", err)
	}
	if _, err := f.store.GetCapture(ctx, "user1", "c_legacy"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the legacy capture row survived the delete (err = %v)", err)
	}
	// And a legacy capture filed into a different note is left alone.
	other := f.archivedNote(t, "b")
	f.putLegacyCapture(t, model.CaptureIndex{
		ID: "c_other", UserID: "user1", NoteID: other.ID, Status: model.StatusAppended, CreatedAt: "2026-08-07T09:00:00Z",
	})
	if err := f.notes.PermanentlyDeleteNote(ctx, "user1", f.archivedNote(t, "c").ID); err != nil {
		t.Fatalf("PermanentlyDeleteNote: %v", err)
	}
	if _, err := f.store.GetCapture(ctx, "user1", "c_other"); err != nil {
		t.Errorf("deleting one note removed another note's legacy capture: %v", err)
	}
}
