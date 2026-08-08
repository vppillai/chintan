package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// purgeFixture is a notes service over in-memory storage with one archived note
// carrying one capture, and every object both name.
type purgeFixture struct {
	store   *memory.Store
	objects *memory.Objects
	notes   *NotesService
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	store := memory.NewStore()
	objects := memory.NewObjects()
	return &purgeFixture{store: store, objects: objects, notes: NewNotesService(store, objects)}
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

func (f *purgeFixture) activeNote(t *testing.T, title string) model.NoteIndex {
	t.Helper()
	note, err := f.notes.CreateNote(context.Background(), "user1", title, nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	return note
}

func resultFor(results []PurgeResult, noteID string) (PurgeResult, bool) {
	for _, r := range results {
		if r.NoteID == noteID {
			return r, true
		}
	}
	return PurgeResult{}, false
}

// TestPurgeNotesReportsEachNoteSeparately is the shape of the endpoint. There
// is no transaction spanning DynamoDB and S3, so a batch that reported one
// verdict would be claiming an atomicity the server does not have.
func TestPurgeNotesReportsEachNoteSeparately(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()

	archived := f.archivedNote(t, "a")
	active := f.activeNote(t, "Still in use")

	results, err := f.notes.PurgeNotes(ctx, "user1", []string{archived.ID, "note_missing", active.ID})
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want one per requested note", len(results))
	}

	if r, _ := resultFor(results, archived.ID); r.Status != PurgeStatusPurged {
		t.Errorf("archived note = %+v, want purged", r)
	}
	if r, _ := resultFor(results, "note_missing"); r.Status != PurgeStatusNotFound {
		t.Errorf("missing note = %+v, want not_found", r)
	}
	if r, _ := resultFor(results, active.ID); r.Status != PurgeStatusFailed {
		t.Errorf("active note = %+v, want failed", r)
	}
}

// TestPurgeNotesNeverDeletesAnActiveNote is the refusal that matters. A client
// working from a stale archive listing would otherwise turn "clear my archive"
// into "delete the notes I am still using", and a purge cannot be undone.
func TestPurgeNotesNeverDeletesAnActiveNote(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	active := f.activeNote(t, "Still in use")

	results, err := f.notes.PurgeNotes(ctx, "user1", []string{active.ID})
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusFailed {
		t.Fatalf("status = %q, want failed", results[0].Status)
	}
	if !strings.Contains(results[0].Detail, "not archived") {
		t.Errorf("detail = %q, want it to say the note is not archived", results[0].Detail)
	}

	// Still there, and still readable.
	if _, err := f.store.GetNote(ctx, "user1", active.ID); err != nil {
		t.Fatalf("the active note was deleted anyway: %v", err)
	}
	body, err := f.objects.Get(ctx, active.S3MarkdownKey)
	if err != nil || len(body) == 0 && err != nil {
		t.Fatalf("the active note's body was deleted: %v", err)
	}
}

// TestPurgeNotesUnlinksTheCaptureArtefacts holds the batch to the same cascade
// the single-note purge runs. Deleting the index row and leaving the audio is
// exactly the orphaning this whole area was fixed for once already.
func TestPurgeNotesUnlinksTheCaptureArtefacts(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	archived := f.archivedNote(t, "a")

	if _, err := f.notes.PurgeNotes(ctx, "user1", []string{archived.ID}); err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}

	for _, key := range []string{
		"tenants/user1/captures/c_a/audio.webm",
		archived.S3MarkdownKey,
		archived.S3MetaKey,
	} {
		if _, err := f.objects.Get(ctx, key); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("%s survived the purge (err = %v)", key, err)
		}
	}
	page, err := f.store.ListCapturesByNote(ctx, "user1", archived.ID, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("%d captures still filed against the purged note", len(page.Items))
	}
}

// failingObjects fails to delete one key and passes everything else through.
type failingObjects struct {
	repository.Objects
	failKey string
}

var errDeleteRefused = errors.New("induced object-store failure")

func (o *failingObjects) Delete(ctx context.Context, key string) error {
	if key == o.failKey {
		return errDeleteRefused
	}
	return o.Objects.Delete(ctx, key)
}

// TestPurgeNotesReportsAPartialCascadeAsFailed is the property v1 got wrong: it
// logged every cascade failure, deleted the index row anyway, and left audio in
// the bucket that the UI had already reported as purged. The row has to survive
// so the delete can be retried, and the batch has to say so.
func TestPurgeNotesReportsAPartialCascadeAsFailed(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	archived := f.archivedNote(t, "a")

	audio := "tenants/user1/captures/c_a/audio.webm"
	notes := NewNotesService(f.store, &failingObjects{Objects: f.objects, failKey: audio})

	results, err := notes.PurgeNotes(ctx, "user1", []string{archived.ID})
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusFailed {
		t.Fatalf("status = %q, want failed: the audio is still in the bucket", results[0].Status)
	}
	if strings.Contains(results[0].Detail, errDeleteRefused.Error()) {
		t.Errorf("detail leaks infrastructure text: %q", results[0].Detail)
	}

	// The index row is the only remaining record that the objects exist.
	if _, err := f.store.GetNote(ctx, "user1", archived.ID); err != nil {
		t.Fatalf("the note row was deleted despite the failed cascade: %v", err)
	}
}

// TestPurgeNotesIsSafeToReplay is what makes a retried batch harmless: the
// second run finds the notes already gone and says so, rather than failing.
func TestPurgeNotesIsSafeToReplay(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	archived := f.archivedNote(t, "a")

	first, err := f.notes.PurgeNotes(ctx, "user1", []string{archived.ID})
	if err != nil || first[0].Status != PurgeStatusPurged {
		t.Fatalf("first purge = %+v, %v", first, err)
	}
	second, err := f.notes.PurgeNotes(ctx, "user1", []string{archived.ID})
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if second[0].Status != PurgeStatusNotFound {
		t.Errorf("replay = %q, want not_found", second[0].Status)
	}
}

// TestPurgeNotesBoundsTheBatch keeps the work per request inside the API
// Lambda's ceiling. A purge is a cascade, not a delete.
func TestPurgeNotesBoundsTheBatch(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()

	ids := make([]string, MaxPurgeBatch+1)
	for i := range ids {
		ids[i] = "note_x"
	}
	if _, err := f.notes.PurgeNotes(ctx, "user1", ids); !errors.Is(err, ErrPurgeBatchTooLarge) {
		t.Errorf("err = %v, want ErrPurgeBatchTooLarge", err)
	}
	if _, err := f.notes.PurgeNotes(ctx, "user1", nil); !errors.Is(err, ErrPurgeBatchEmpty) {
		t.Errorf("err = %v, want ErrPurgeBatchEmpty", err)
	}
}

// TestPurgeNotesRefusesAnotherTenantsNote keeps the batch from becoming a way
// to delete across tenants, and from confirming that another tenant's
// identifier exists.
func TestPurgeNotesRefusesAnotherTenantsNote(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	theirs := f.archivedNote(t, "a")

	results, err := f.notes.PurgeNotes(ctx, "user2", []string{theirs.ID})
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusNotFound {
		t.Errorf("status = %q, want not_found — anything else confirms the id exists", results[0].Status)
	}
	if _, err := f.store.GetNote(ctx, "user1", theirs.ID); err != nil {
		t.Fatalf("another tenant's note was deleted: %v", err)
	}
}
