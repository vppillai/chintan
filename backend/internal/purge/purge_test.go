package purge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

const testTenant = "user1"

// fixture is a real NotesService over in-memory storage.
type fixture struct {
	store   *memory.Store
	objects *memory.Objects
	notes   *service.NotesService
	sweeper *Sweeper
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)
	sweeper, err := New(store, notes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fixture{store: store, objects: objects, notes: notes, sweeper: sweeper,
		now: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	sweeper.now = func() time.Time { return f.now }
	return f
}

// archivedNote creates a note with one capture and every object both of them
// name, archived so that its deadline is `deadline`.
func (f *fixture) archivedNote(t *testing.T, tenant, id string, deadline time.Time) (model.NoteIndex, []string) {
	t.Helper()
	ctx := context.Background()

	note, err := f.notes.CreateNote(ctx, tenant, "Roof repair "+id, nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	capture := model.CaptureIndex{
		ID: "cap_" + id, NoteID: note.ID, UserID: tenant, Status: model.StatusAppended, CreatedAt: model.Now(),
		AudioKey:    fmt.Sprintf("tenants/%s/captures/cap_%s/audio.webm", tenant, id),
		RawKey:      fmt.Sprintf("tenants/%s/captures/cap_%s/raw.txt", tenant, id),
		RoutedKey:   fmt.Sprintf("tenants/%s/captures/cap_%s/routed.txt", tenant, id),
		CleanKey:    fmt.Sprintf("tenants/%s/captures/cap_%s/clean.txt", tenant, id),
		SegmentsKey: fmt.Sprintf("tenants/%s/captures/cap_%s/segments.json", tenant, id),
		PeaksKey:    fmt.Sprintf("tenants/%s/captures/cap_%s/peaks.json", tenant, id),
	}
	if _, err := f.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	keys := []string{note.S3MarkdownKey, note.S3MetaKey, capture.AudioKey, capture.RawKey,
		capture.RoutedKey, capture.CleanKey, capture.SegmentsKey, capture.PeaksKey}
	for _, k := range keys {
		if err := f.objects.Put(ctx, k, []byte("x"), "application/octet-stream"); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	stored, err := f.store.GetNote(ctx, tenant, note.ID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if !deadline.IsZero() {
		stored.DeletedAt = model.FormatTime(deadline.Add(-service.ArchiveRetention))
		stored.PurgeAfter = model.FormatTime(deadline)
		stored.PurgeAfterEpoch = deadline.Unix()
	}
	if stored, err = f.store.PutNote(ctx, tenant, stored); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	return stored, keys
}

func (f *fixture) alive(keys []string) []string {
	var alive []string
	for _, k := range keys {
		if _, err := f.objects.Get(context.Background(), k); err == nil {
			alive = append(alive, k)
		}
	}
	sort.Strings(alive)
	return alive
}

// TestSweepPurgesEveryObjectAnExpiredNoteOwnedAndThenItsRow is the leak this
// package closes: the note's own two objects, and the six of every capture
// filed against it, which TTL alone never touched.
func TestSweepPurgesEveryObjectAnExpiredNoteOwnedAndThenItsRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	note, keys := f.archivedNote(t, testTenant, "a", f.now.Add(-time.Hour))

	report, err := f.sweeper.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report != (Report{Expired: 1, Purged: 1}) {
		t.Fatalf("report = %+v, want one expired, one purged", report)
	}
	if alive := f.alive(keys); len(alive) != 0 {
		t.Errorf("these objects outlived the sweep that expired their note: %v", alive)
	}
	if _, err := f.store.GetNote(ctx, testTenant, note.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetNote after sweep: err = %v, want the row gone", err)
	}
	if _, err := f.store.GetCapture(ctx, testTenant, "cap_a"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetCapture after sweep: err = %v, want the capture row gone", err)
	}
}

// A note that is archived but not yet due, and a note that is not archived at
// all, are exactly what the sweep must leave alone: the first is in the user's
// archive with a restore button, the second is live.
func TestSweepLeavesLiveAndNotYetDueNotesAlone(t *testing.T) {
	f := newFixture(t)
	_, liveKeys := f.archivedNote(t, testTenant, "live", time.Time{})
	_, pendingKeys := f.archivedNote(t, testTenant, "pending", f.now.Add(24*time.Hour))
	_, dueKeys := f.archivedNote(t, testTenant, "due", f.now.Add(-24*time.Hour))

	report, err := f.sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Expired != 1 || report.Purged != 1 {
		t.Fatalf("report = %+v, want exactly the due note", report)
	}
	if got := f.alive(liveKeys); len(got) != len(liveKeys) {
		t.Errorf("the live note lost objects: %d of %d remain", len(got), len(liveKeys))
	}
	if got := f.alive(pendingKeys); len(got) != len(pendingKeys) {
		t.Errorf("the archived-but-not-due note lost objects: %d of %d remain", len(got), len(pendingKeys))
	}
	if got := f.alive(dueKeys); len(got) != 0 {
		t.Errorf("the due note kept objects: %v", got)
	}
}

// The sweep is an instance job: it does not know the tenants and must not need
// to.
func TestSweepCrossesTenants(t *testing.T) {
	f := newFixture(t)
	_, aKeys := f.archivedNote(t, "tenant-a", "a", f.now.Add(-time.Hour))
	_, bKeys := f.archivedNote(t, "tenant-b", "b", f.now.Add(-time.Hour))

	report, err := f.sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Purged != 2 {
		t.Fatalf("report = %+v, want both tenants' notes purged", report)
	}
	if alive := f.alive(append(aKeys, bKeys...)); len(alive) != 0 {
		t.Errorf("objects survived: %v", alive)
	}
}

// Weekly means the same note is seen at most once, but a sweep retried by
// Lambda after a partial failure sees notes it already purged the objects of.
// Re-deleting what is gone must be success, or the retry can never finish.
func TestSweepIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.archivedNote(t, testTenant, "a", f.now.Add(-time.Hour))

	if _, err := f.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("first Sweep: %v", err)
	}
	report, err := f.sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if report != (Report{}) {
		t.Fatalf("second sweep report = %+v, want nothing left to do", report)
	}
}

// failingObjects refuses to delete one key, standing in for an S3 fault.
type failingObjects struct {
	repository.Objects
	refuse string
}

func (o *failingObjects) Delete(ctx context.Context, key string) error {
	if key == o.refuse {
		return errors.New("s3: 503 slow down")
	}
	return o.Objects.Delete(ctx, key)
}

// One note that cannot be purged must not stop the others, must not lose its
// row — the row is the only record of what to delete — and must be reported,
// because the error is what makes Lambda retry and, failing that, alarm.
func TestAFailedPurgeIsReportedAndKeepsItsRowForTheNextSweep(t *testing.T) {
	store := memory.NewStore()
	objects := &failingObjects{Objects: memory.NewObjects()}
	notes := service.NewNotesService(store, objects)
	sweeper, err := New(store, notes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fixture{store: store, objects: objects.Objects.(*memory.Objects), notes: notes, sweeper: sweeper,
		now: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	sweeper.now = func() time.Time { return f.now }

	stuck, stuckKeys := f.archivedNote(t, testTenant, "stuck", f.now.Add(-time.Hour))
	objects.refuse = stuckKeys[2] // the capture's audio
	_, fineKeys := f.archivedNote(t, testTenant, "fine", f.now.Add(-time.Hour))

	report, err := sweeper.Sweep(context.Background())
	if err == nil {
		t.Fatal("Sweep returned nil with a note it could not purge; Lambda would never retry it")
	}
	if report.Expired != 2 || report.Purged != 1 || report.Failed != 1 {
		t.Fatalf("report = %+v, want 2 expired, 1 purged, 1 failed", report)
	}
	if alive := f.alive(fineKeys); len(alive) != 0 {
		t.Errorf("the purgeable note was not purged because its neighbour failed: %v", alive)
	}
	if _, err := store.GetNote(context.Background(), testTenant, stuck.ID); err != nil {
		t.Errorf("the failed note's row is gone (%v); nothing can find its objects now", err)
	}
}

func TestNewRefusesAnIncompleteSweeper(t *testing.T) {
	store := memory.NewStore()
	notes := service.NewNotesService(store, memory.NewObjects())
	if _, err := New(nil, notes); err == nil {
		t.Error("New accepted a nil store")
	}
	if _, err := New(store, nil); err == nil {
		t.Error("New accepted a nil cascade")
	}
}
