package storagesnap

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// fixture is the task over the in-memory usage rows and a real
// StorageService on the in-memory store, with a fixed clock.
type fixture struct {
	store *memory.Store
	rows  *memory.Usage
	snap  *Snapshotter
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	store := memory.NewStore()
	rows := memory.NewUsage()
	snap, err := New(rows, service.NewStorageService(store))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snap.now = func() time.Time { return now }
	return &fixture{store: store, rows: rows, snap: snap}
}

// tenantWithUsage gives the tenant a usage row in the month of day, which is
// what puts them on the task's list.
func (f *fixture) tenantWithUsage(t *testing.T, tenantID, day string) {
	t.Helper()
	if err := f.rows.CountRequest(context.Background(), tenantID, day); err != nil {
		t.Fatalf("CountRequest: %v", err)
	}
}

func (f *fixture) recording(t *testing.T, tenantID, id string, bytes int64) {
	t.Helper()
	if _, err := f.store.PutCapture(context.Background(), model.CaptureIndex{
		ID: id, UserID: tenantID, NoteID: "n1", Status: model.StatusAppended, AudioBytes: bytes,
		CreatedAt: "2026-09-01T00:00:00.000000000Z",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	if _, err := f.store.GetNote(context.Background(), tenantID, "n1"); err != nil {
		if _, err := f.store.PutNote(context.Background(), tenantID, model.NoteIndex{ID: "n1", Title: "Roof"}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	}
}

func TestRunAddsTodaysFootprintToEveryTenantTheUsageRowsName(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	// Named by this month's rows, by last month's, and by a row a year back.
	f.tenantWithUsage(t, "tenant-a", "2026-09-02")
	f.tenantWithUsage(t, "tenant-b", "2026-08-30")
	f.tenantWithUsage(t, "tenant-c", "2025-09-15")
	// Not named: the row is fourteen months old.
	f.tenantWithUsage(t, "tenant-old", "2025-08-15")
	f.recording(t, "tenant-a", "c1", 9_000_000)
	f.recording(t, "tenant-a", "c2", 1_000_000)
	f.recording(t, "tenant-b", "c1", 500)
	f.recording(t, "tenant-c", "c1", 7)
	f.recording(t, "tenant-old", "c1", 4_000_000)

	res, err := f.snap.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Day != "2026-09-05" || res.Tenants != 3 || res.Added != 3 || res.Skipped != 0 {
		t.Errorf("result = %+v", res)
	}

	month, err := f.rows.Month(context.Background(), "tenant-a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if month.StorageByteDays != 10_000_000 || month.StorageNoteDays != 1 {
		t.Errorf("tenant-a storage days = %d bytes, %d notes; want today's footprint added once", month.StorageByteDays, month.StorageNoteDays)
	}
	if n := len(month.Days); n != 2 || month.Days[1].Date != "2026-09-05" || month.Days[1].StorageByteDays != 10_000_000 {
		t.Errorf("tenant-a days = %+v", month.Days)
	}
	// The one the rows did not name was not measured.
	old, _ := f.rows.Month(context.Background(), "tenant-old", "2026-09")
	if old.StorageByteDays != 0 {
		t.Errorf("tenant-old was snapshotted (%d byte-days) without a usage row in thirteen months", old.StorageByteDays)
	}
	// And a tenant snapshotted today is named by this month's rows tomorrow,
	// whether or not they use the app again.
	named, _ := f.rows.TenantsWithUsage(context.Background(), []string{"2026-09"})
	if !slices.Contains(named, "tenant-c") {
		t.Errorf("tenants named by this month = %v, want tenant-c kept in the set by its snapshot", named)
	}
}

// A second run on the same day — Lambda's retry, or a rule that fired twice —
// must not double the month.
func TestRunIsIdempotentWithinADay(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.tenantWithUsage(t, "tenant-a", "2026-09-02")
	f.recording(t, "tenant-a", "c1", 9_000_000)

	if _, err := f.snap.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := f.snap.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Added != 0 || res.Skipped != 1 {
		t.Errorf("second run = %+v, want everything skipped", res)
	}
	month, _ := f.rows.Month(context.Background(), "tenant-a", "2026-09")
	if month.StorageByteDays != 9_000_000 {
		t.Errorf("byte-days = %d after two runs, want one day's worth", month.StorageByteDays)
	}

	// The next day adds again.
	f.snap.now = func() time.Time { return now.AddDate(0, 0, 1) }
	if _, err := f.snap.Run(context.Background()); err != nil {
		t.Fatalf("next day's Run: %v", err)
	}
	month, _ = f.rows.Month(context.Background(), "tenant-a", "2026-09")
	if month.StorageByteDays != 18_000_000 {
		t.Errorf("byte-days = %d after two days, want two days' worth", month.StorageByteDays)
	}
}

func TestRunLeavesATenantWithNothingStoredAlone(t *testing.T) {
	f := newFixture(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	f.tenantWithUsage(t, "tenant-empty", "2026-09-02")

	res, err := f.snap.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Empty != 1 || res.Added != 0 {
		t.Errorf("result = %+v", res)
	}
	month, _ := f.rows.Month(context.Background(), "tenant-empty", "2026-09")
	if len(month.Days) != 1 || month.Days[0].StorageByteDays != 0 {
		t.Errorf("days = %+v, want no snapshot row for a tenant with nothing stored", month.Days)
	}
}

// failingStore fails the write for one tenant and passes the rest through.
type failingStore struct {
	*memory.Usage
	failFor string
}

var errInduced = errors.New("induced dynamodb fault")

func (s failingStore) AddStorageDay(ctx context.Context, snap usage.StorageSnapshot) (bool, error) {
	if snap.TenantID == s.failFor {
		return false, errInduced
	}
	return s.Usage.AddStorageDay(ctx, snap)
}

// One tenant's fault neither stops the others nor is swallowed: the run
// finishes and returns the error, so Lambda retries a task that is safe to
// retry.
func TestRunFinishesTheOthersAndReturnsTheFault(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.tenantWithUsage(t, "tenant-a", "2026-09-02")
	f.tenantWithUsage(t, "tenant-b", "2026-09-02")
	f.recording(t, "tenant-a", "c1", 1)
	f.recording(t, "tenant-b", "c1", 2)
	snap, err := New(failingStore{Usage: f.rows, failFor: "tenant-a"}, service.NewStorageService(f.store))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snap.now = func() time.Time { return now }

	res, err := snap.Run(context.Background())
	if !errors.Is(err, errInduced) {
		t.Fatalf("err = %v, want the induced fault returned for the retry", err)
	}
	if res.Added != 1 {
		t.Errorf("result = %+v, want the other tenant still recorded", res)
	}
}

func TestNewRequiresBothDependencies(t *testing.T) {
	if _, err := New(nil, service.NewStorageService(memory.NewStore())); err == nil {
		t.Error("New accepted a nil store")
	}
	if _, err := New(memory.NewUsage(), nil); err == nil {
		t.Error("New accepted a nil storage service")
	}
}

func TestMonthsBackCountsFromTheFirstOfTheMonth(t *testing.T) {
	got := monthsBack(time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC), 3)
	want := []string{"2026-03", "2026-02", "2026-01"}
	if !slices.Equal(got, want) {
		t.Errorf("monthsBack = %v, want %v (the 31st must not skip February)", got, want)
	}
	if n := len(monthsBack(time.Now(), lookbackMonths)); n != 13 {
		t.Errorf("lookback = %d months, want 13", n)
	}
}
