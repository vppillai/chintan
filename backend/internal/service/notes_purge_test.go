package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// TestPurgeNotesReportsAPartialCascadeAsFailed: logging a cascade failure and
// deleting the index row anyway leaves audio in the bucket that the UI has
// already reported as purged. The row has to survive so the delete can be
// retried, and the batch has to say so.
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

// TestPurgeUnlinksACaptureTheNoteIndexCannotSee is the production defect of
// 2026-09-05: "delete forever" listed each note's captures through GSI1, a
// capture row written in August 2026 carries no index keys, so thirteen filed
// captures survived the purge of every note and kept answering the library's
// receipts. The purge has to find such a row from the base table.
func TestPurgeUnlinksACaptureTheNoteIndexCannotSee(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	archived := f.archivedNote(t, "a")

	audio := "tenants/user1/captures/c_legacy/audio.webm"
	f.store.PutLegacyCapture(model.CaptureIndex{
		ID: "c_legacy", UserID: "user1", NoteID: archived.ID,
		Status: model.StatusAppended, CreatedAt: "2026-08-07T09:00:00Z", AudioKey: audio,
	})
	if err := f.objects.Put(ctx, audio, []byte("x"), "audio/webm"); err != nil {
		t.Fatalf("Put audio: %v", err)
	}
	if page, _ := f.store.ListCapturesByNote(ctx, "user1", archived.ID, repository.ListOptions{}); len(page.Items) != 1 {
		t.Fatalf("the index lists %d captures, want only the modern one; the legacy row must be invisible to it", len(page.Items))
	}

	results, err := f.notes.PurgeNotes(ctx, "user1", []string{archived.ID})
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusPurged {
		t.Fatalf("result = %+v, want purged", results[0])
	}
	if _, err := f.objects.Get(ctx, audio); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the legacy capture's audio survived the purge (err = %v)", err)
	}
	if _, err := f.store.GetCapture(ctx, "user1", "c_legacy"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the legacy capture row survived the purge (err = %v)", err)
	}
	// And a legacy capture filed into a different note is left alone.
	other := f.archivedNote(t, "b")
	f.store.PutLegacyCapture(model.CaptureIndex{
		ID: "c_other", UserID: "user1", NoteID: other.ID, Status: model.StatusAppended, CreatedAt: "2026-08-07T09:00:00Z",
	})
	if _, err := f.notes.PurgeNotes(ctx, "user1", []string{f.archivedNote(t, "c").ID}); err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if _, err := f.store.GetCapture(ctx, "user1", "c_other"); err != nil {
		t.Errorf("purging one note removed another note's legacy capture: %v", err)
	}
}

// countingUnindexed counts the base-table reads of the captures the note index
// cannot see. A batch purge made one per note: a hundred reads of the tenant's
// whole capture partition for one "clear all".
type countingUnindexed struct {
	repository.Store
	calls int
}

func (s *countingUnindexed) ListUnindexedCaptures(ctx context.Context, tenantID string) ([]model.CaptureIndex, error) {
	s.calls++
	return s.Store.ListUnindexedCaptures(ctx, tenantID)
}

func TestPurgeNotesListsTheUnindexedCapturesOncePerBatch(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	counting := &countingUnindexed{Store: f.store}
	notes := NewNotesService(counting, f.objects)

	ids := []string{f.archivedNote(t, "a").ID, f.archivedNote(t, "b").ID, f.archivedNote(t, "c").ID}
	// One of them owns a capture only the base table can see.
	legacyAudio := "tenants/user1/captures/c_legacy/audio.webm"
	f.store.PutLegacyCapture(model.CaptureIndex{
		ID: "c_legacy", UserID: "user1", NoteID: ids[1], Status: model.StatusAppended,
		CreatedAt: model.Now(), AudioKey: legacyAudio,
	})
	if err := f.objects.Put(ctx, legacyAudio, []byte("x"), "audio/webm"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := notes.PurgeNotes(ctx, "user1", ids)
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	for _, r := range results {
		if r.Status != PurgeStatusPurged {
			t.Fatalf("result %+v, want purged", r)
		}
	}
	if counting.calls != 1 {
		t.Fatalf("ListUnindexedCaptures was called %d times for a batch of 3, want 1", counting.calls)
	}
	if _, err := f.objects.Get(ctx, legacyAudio); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the legacy capture's audio survived the batch: err = %v", err)
	}
	if _, err := f.store.GetCapture(ctx, "user1", "c_legacy"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the legacy capture's row survived the batch: err = %v", err)
	}
}

// slowObjects advances a clock on every delete, so a test can spend the batch's
// time budget without spending the time.
type slowObjects struct {
	repository.Objects
	advance func()
}

func (o *slowObjects) Delete(ctx context.Context, key string) error {
	o.advance()
	return o.Objects.Delete(ctx, key)
}

// A batch that cannot finish inside the API function's time stops starting
// notes and reports the rest as failed, "not attempted", for the client to send
// again. Before, it ran into the gateway's 504 with the client's idempotency
// key still claimed, and the same-key retries answered 409 for a minute.
func TestPurgeNotesStopsAtItsTimeBudgetAndReportsTheRestForRetry(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	// Each note's cascade deletes three objects (audio, body, meta); every
	// delete costs four seconds of the clock, so the first note finishes at
	// 12 s, the second is started (12 s < 20 s) and finishes at 24 s, and the
	// third is never begun.
	slow := &slowObjects{Objects: f.objects, advance: func() { now = now.Add(4 * time.Second) }}
	notes := NewNotesService(f.store, slow).WithClock(func() time.Time { return now })
	ids := []string{f.archivedNote(t, "a").ID, f.archivedNote(t, "b").ID, f.archivedNote(t, "c").ID}

	results, err := notes.PurgeNotes(ctx, "user1", ids)
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusPurged || results[1].Status != PurgeStatusPurged {
		t.Fatalf("the notes started inside the budget = %+v, want purged", results[:2])
	}
	if results[2].Status != PurgeStatusFailed || !strings.HasPrefix(results[2].Detail, "not attempted") {
		t.Fatalf("the note past the budget = %+v, want failed and not attempted", results[2])
	}
	// Untouched: its row and its audio are exactly where a retry expects them.
	if _, err := f.store.GetNote(ctx, "user1", ids[2]); err != nil {
		t.Fatalf("the unattempted note's row is gone: %v", err)
	}
	if _, err := f.objects.Get(ctx, "tenants/user1/captures/c_c/audio.webm"); err != nil {
		t.Fatalf("the unattempted note's audio is gone: %v", err)
	}

	// The retry, with time, finishes the job.
	now = now.Add(time.Hour)
	again, err := notes.PurgeNotes(ctx, "user1", ids[2:])
	if err != nil || again[0].Status != PurgeStatusPurged {
		t.Fatalf("retry = %+v, %v; want purged", again, err)
	}
}

// The request's own deadline, when nearer than the budget, is what the batch
// works against — less the margin the response needs — so a client that gave
// the call five seconds is answered in five seconds with the rest for retry,
// not cut off by its own timeout.
func TestPurgeNotesHonoursANearerRequestDeadline(t *testing.T) {
	f := newPurgeFixture(t)
	now := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	slow := &slowObjects{Objects: f.objects, advance: func() { now = now.Add(time.Second) }}
	notes := NewNotesService(f.store, slow).WithClock(func() time.Time { return now })
	ids := []string{f.archivedNote(t, "a").ID, f.archivedNote(t, "b").ID}

	// Six seconds on the request: four are kept back, so the budget is about
	// two seconds — the first note (three seconds of deletes) is started and
	// finished, the second is not begun.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	results, err := notes.PurgeNotes(ctx, "user1", ids)
	if err != nil {
		t.Fatalf("PurgeNotes: %v", err)
	}
	if results[0].Status != PurgeStatusPurged {
		t.Fatalf("first note = %+v, want purged", results[0])
	}
	if results[1].Status != PurgeStatusFailed || !strings.HasPrefix(results[1].Detail, "not attempted") {
		t.Fatalf("second note = %+v, want failed and not attempted under a six-second request", results[1])
	}
}
