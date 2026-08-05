package meter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The properties under test here are the ones the spend breaker (§10.5.9) depends on,
// and they are all of the form "a failure is loud". A metering path that drops an event,
// overwrites one, or totals a day as zero does not produce a wrong number on a report —
// it raises the effective daily cap, silently, with no log line. So the negative cases
// carry more weight than the positive ones, and each asserts the *reason* rather than
// merely that something failed: a record refused for the wrong reason would satisfy a
// bare error check while leaving the real constraint unverified.

const testTenant keys.TenantID = "t_01HTEST"

// fixedNow is the instant every test's clock is stopped at. Mid-month and mid-day on
// purpose: a boundary value would let a month-partition bug pass by coincidence.
var fixedNow = time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)

// seqIDs issues predictable, sortable identifiers.
//
// The real generator is a ULID source, and a test that cannot predict the ID cannot
// assert which key a record landed under — which is where the month partition lives
// (§6.3), and therefore what makes the breaker's bounded range read correct.
type seqIDs struct{ n int }

func (s *seqIDs) NewID() string {
	s.n++
	return fmt.Sprintf("01TEST%04d", s.n)
}

// constID collides on every call, which is how the write-once property is exercised.
type constID string

func (c constID) NewID() string { return string(c) }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestMeter returns a meter over an in-memory repository with a stopped clock.
func newTestMeter(t *testing.T, at time.Time, ids IDGenerator, ttlMonths int) (*Meter, *repository.Memory) {
	t.Helper()
	repo := repository.NewMemory()
	return New(repo, clock.Fixed{T: at}, ids, discardLogger(), ttlMonths), repo
}

func sttEvent() Event {
	return Event{
		Tenant:     testTenant,
		Unit:       model.UnitSTTSeconds,
		Quantity:   28,
		Provider:   "provider-a",
		CostMicros: 311,
		Op:         "transcribe",
	}
}

// ---------------------------------------------------------------------------
// Key placement
// ---------------------------------------------------------------------------

// The expected key is built with the keys helper rather than written as a literal:
// backend/internal/keys holds the monopoly on key prefixes (I11) and
// check-tenant-keys.sh fails the build on a literal anywhere else — including here.
// Asserting through the helper also means a test cannot disagree with the helper about
// the key shape.
func TestRecordLandsInTheMonthPartitionTakenFromTheClock(t *testing.T) {
	m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)

	if err := m.Record(context.Background(), sttEvent()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	want, err := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), "01TEST0001")
	if err != nil {
		t.Fatalf("building the expected key: %v", err)
	}

	got := repo.Keys()
	if len(got) != 1 {
		t.Fatalf("wrote %d items, want exactly 1", len(got))
	}
	if got[0] != want {
		t.Errorf("record landed under %+v, want %+v", got[0], want)
	}

	// The month must come from the clock, not from the current date the test happens to
	// run on. A meter that read the real clock would place this record elsewhere and the
	// breaker's month-range read would miss it.
	if !strings.Contains(got[0].SK, "2026-08") {
		t.Errorf("sort key %q does not carry the clock's month", got[0].SK)
	}

	item, err := repo.Get(context.Background(), want)
	if err != nil {
		t.Fatalf("reading back the record: %v", err)
	}
	// The unit is in both the key and the attributes: the key so a month read is
	// bounded, the attribute so a total does not have to parse a key apart.
	if item.Attrs["unit"] != string(model.UnitSTTSeconds) {
		t.Errorf("unit attribute is %v, want %q", item.Attrs["unit"], model.UnitSTTSeconds)
	}
	if item.Attrs["provider"] != "provider-a" {
		t.Errorf("provider attribute is %v; §9.2 requires knowing which provider processed what", item.Attrs["provider"])
	}
	if item.Attrs["op"] != "transcribe" {
		t.Errorf("op attribute is %v; shadow spend must be distinguishable (§7.2)", item.Attrs["op"])
	}
	if item.Attrs["ts"] != clock.RFC3339UTC(fixedNow) {
		t.Errorf("ts is %v, want %q", item.Attrs["ts"], clock.RFC3339UTC(fixedNow))
	}

	// §6.3 forbids Usage projecting into the sparse GSI1 index: at on-demand pricing a
	// projected high-volume record is a second copy of the table, paid for on every
	// write.
	if item.GSI1PK != "" || item.GSI1SK != "" {
		t.Errorf("usage record carries GSI1 attributes (%q/%q); high-volume records must not project into the index (§6.3)", item.GSI1PK, item.GSI1SK)
	}
}

// ---------------------------------------------------------------------------
// Write-once
// ---------------------------------------------------------------------------

func TestRecordIsWriteOnceAndDoesNotOverwrite(t *testing.T) {
	// A colliding generator is the realistic failure: the ID is the only unique
	// component of the sort key, so a generator fault is what makes two records share a
	// key. Overwriting would destroy a cost a reconciliation may already have counted.
	m, repo := newTestMeter(t, fixedNow, constID("01TESTDUPE"), 25)

	first := sttEvent()
	first.CostMicros = 1000
	if err := m.Record(context.Background(), first); err != nil {
		t.Fatalf("first Record: %v", err)
	}

	second := sttEvent()
	second.CostMicros = 9999
	err := m.Record(context.Background(), second)
	if err == nil {
		t.Fatal("duplicate key was accepted; usage records are write-once (§6.3)")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate write failed with %v; the caller needs errors.Is(ErrAlreadyExists) to tell a collision from a storage fault", err)
	}

	if n := repo.Len(); n != 1 {
		t.Fatalf("repository holds %d items after a refused duplicate, want 1", n)
	}
	key, err := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), "01TESTDUPE")
	if err != nil {
		t.Fatalf("building key: %v", err)
	}
	item, err := repo.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if item.Attrs["cost_micros"] != int64(1000) {
		t.Errorf("stored cost is %v, want the first record's 1000; the duplicate overwrote it", item.Attrs["cost_micros"])
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestRecordRefusalsNameTheirReasonAndWriteNothing(t *testing.T) {
	base := sttEvent()

	cases := map[string]struct {
		mutate func(*Event)
		want   string
	}{
		"empty tenant": {
			// I11: the refusal has to come from the keys package, so this test and the
			// key helper cannot disagree about what a usable tenant is.
			mutate: func(e *Event) { e.Tenant = "" },
			want:   "tenant_id is empty",
		},
		"whitespace tenant": {
			// A tenant of " " would otherwise build a syntactically valid key in a
			// partition nothing else writes to — harder to notice than an error.
			mutate: func(e *Event) { e.Tenant = "  " },
			want:   "tenant_id is empty",
		},
		"tenant containing the key delimiter": {
			mutate: func(e *Event) { e.Tenant = "t1#TENANT" },
			want:   "not a valid identifier",
		},
		"unknown unit": {
			mutate: func(e *Event) { e.Unit = model.MeterUnit("gpu_hours") },
			want:   "unknown unit",
		},
		"empty unit": {
			// An unset unit must not fall through as a valid record: it would meter a
			// cost against no billable dimension, which is unattributable (I12).
			mutate: func(e *Event) { e.Unit = "" },
			want:   "unknown unit",
		},
		"negative quantity": {
			mutate: func(e *Event) { e.Quantity = -1 },
			want:   "is negative",
		},
		"missing provider": {
			mutate: func(e *Event) { e.Provider = "" },
			want:   "provider is required",
		},
		"missing op": {
			// Without an op, shadow-mode spend is indistinguishable from the active
			// provider's and cannot be switched off knowingly (§7.2).
			mutate: func(e *Event) { e.Op = "" },
			want:   "op is required",
		},
		"negative cost": {
			// A negative cost would reduce a day's total and hand the breaker headroom
			// that does not exist.
			mutate: func(e *Event) { e.CostMicros = -1 },
			want:   "is negative",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)
			ev := base
			c.mutate(&ev)

			err := m.Record(context.Background(), ev)
			if err == nil {
				t.Fatalf("event was accepted; expected refusal mentioning %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal did not mention %q: %v", c.want, err)
			}
			if !strings.HasPrefix(err.Error(), "meter:") {
				t.Errorf("error %q is not prefixed with the package name", err)
			}
			// A refusal that still writes is worse than no validation: the record would
			// be counted by every later total and could never be corrected, since usage
			// records have no update path.
			if n := repo.Len(); n != 0 {
				t.Errorf("a refused event wrote %d items", n)
			}
		})
	}
}

func TestRecordRefusesAnUnusableGeneratedID(t *testing.T) {
	// The ID reaches the sort key, so the key helper is the last line of defence
	// against a generator that returns nothing usable. Without this branch the record
	// would land under a truncated key that a month read still matches, silently
	// merging two records' identities.
	m, repo := newTestMeter(t, fixedNow, constID(""), 25)
	err := m.Record(context.Background(), sttEvent())
	if err == nil {
		t.Fatal("an empty generated ID was accepted")
	}
	if !strings.Contains(err.Error(), "id is empty") {
		t.Errorf("refusal did not name the empty id: %v", err)
	}
	if n := repo.Len(); n != 0 {
		t.Errorf("wrote %d items despite an unusable key", n)
	}
}

func TestRecordPropagatesAStorageFailure(t *testing.T) {
	// §Phase 0 and the breaker both depend on this: metering must not be best-effort
	// telemetry. A dropped event is a provider call that escaped the cap.
	m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)
	boom := errors.New("dynamodb: throughput exceeded")
	repo.FailNext(boom)

	err := m.Record(context.Background(), sttEvent())
	if err == nil {
		t.Fatal("Record swallowed a storage failure; a silently dropped event raises the effective daily cap")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the storage failure", err)
	}
}

// ---------------------------------------------------------------------------
// TTL arithmetic
// ---------------------------------------------------------------------------

func TestTTLIsAnAbsoluteEpochSecondAtRetentionUsageMonths(t *testing.T) {
	// The expected instant is computed independently of the implementation: 25 months
	// after 2026-08-04 is 2028-09-04. Re-deriving it with AddDate would assert the code
	// against itself.
	const ttlMonths = 25
	wantTTL := time.Date(2028, 9, 4, 10, 30, 0, 0, time.UTC).Unix()

	repo := repository.NewMemory()
	spy := &writeSpy{Repository: repo}
	m := New(spy, clock.Fixed{T: fixedNow}, &seqIDs{}, discardLogger(), ttlMonths)
	if err := m.Record(context.Background(), sttEvent()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	key, _ := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), "01TEST0001")
	item, err := repo.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if item.TTL != wantTTL {
		t.Errorf("item TTL is %d, want %d (25 months, §6.3)", item.TTL, wantTTL)
	}

	// The attribute and the item's TTL field must agree. DynamoDB expires on the
	// attribute; a report reading the record would use the attribute too, so a
	// divergence means a record that vanishes earlier or later than its own data says.
	//
	// Asserted on the item as *written* rather than as read back: the DynamoDB adapter
	// consumes the ttl attribute into Item.TTL and drops it from Attrs, so Attrs["ttl"]
	// on a read is a property of the in-memory fake alone. Asserting it there would stay
	// green even if the meter stopped writing the attribute — the same fake/real
	// divergence that hid the cost_micros type problem.
	written := spy.items()
	if len(written) != 1 {
		t.Fatalf("wrote %d items, want 1", len(written))
	}
	if written[0].TTL != wantTTL {
		t.Errorf("written item TTL is %d, want %d", written[0].TTL, wantTTL)
	}
	if written[0].Attrs["ttl"] != wantTTL {
		t.Errorf("written ttl attribute is %v, want %d and equal to the item TTL", written[0].Attrs["ttl"], wantTTL)
	}
	if _, ok := written[0].Attrs["ttl"].(int64); !ok {
		t.Errorf("written ttl attribute has type %T; DynamoDB TTL requires an epoch-second number", written[0].Attrs["ttl"])
	}
}

// writeSpy records what was handed to the repository, so a test can assert on what the
// meter *wrote* instead of on what one particular implementation returns from a read. The
// two differ for attributes the adapter consumes — ttl is one — and a read-side assertion
// over the in-memory fake cannot tell "written correctly" from "not written at all".
type writeSpy struct {
	repository.Repository
	mu      sync.Mutex
	written []repository.Item
}

func (w *writeSpy) PutOnce(ctx context.Context, item repository.Item) error {
	w.mu.Lock()
	w.written = append(w.written, item)
	w.mu.Unlock()
	return w.Repository.PutOnce(ctx, item)
}

func (w *writeSpy) items() []repository.Item {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]repository.Item(nil), w.written...)
}

func TestTTLTracksTheConfiguredRetentionWindow(t *testing.T) {
	// retention.usage_months is config (§7.4), so the arithmetic must follow it rather
	// than a constant compiled in here.
	cases := map[int]int64{
		1:  time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC).Unix(),
		12: time.Date(2027, 8, 4, 10, 30, 0, 0, time.UTC).Unix(),
		25: time.Date(2028, 9, 4, 10, 30, 0, 0, time.UTC).Unix(),
	}
	for months, want := range cases {
		t.Run(fmt.Sprintf("%d months", months), func(t *testing.T) {
			m, repo := newTestMeter(t, fixedNow, &seqIDs{}, months)
			if err := m.Record(context.Background(), sttEvent()); err != nil {
				t.Fatalf("Record: %v", err)
			}
			key, _ := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), "01TEST0001")
			item, _ := repo.Get(context.Background(), key)
			if item.TTL != want {
				t.Errorf("TTL for %d months is %d, want %d", months, item.TTL, want)
			}
		})
	}
}

func TestTTLFromAMonthEndDateNormalisesForward(t *testing.T) {
	// Documented rather than asserted as ideal: Go's AddDate normalises an overflowing
	// day, so 31 January plus one month is 3 March, not 28 February. Harmless for a
	// 25-month retention window — the record lives three days longer than nominal — and
	// worth pinning so nobody later "fixes" it into an off-by-a-month.
	at := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	m, repo := newTestMeter(t, at, &seqIDs{}, 1)
	if err := m.Record(context.Background(), sttEvent()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	key, _ := keys.Usage(testTenant, "2026-01", string(model.UnitSTTSeconds), "01TEST0001")
	item, _ := repo.Get(context.Background(), key)
	want := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC).Unix()
	if item.TTL != want {
		t.Errorf("TTL is %d, want %d (31 Jan + 1 month normalises to 3 Mar)", item.TTL, want)
	}
}

// ---------------------------------------------------------------------------
// Integer money
// ---------------------------------------------------------------------------

func TestCostIsIntegerMicrosEndToEnd(t *testing.T) {
	m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)

	// Values chosen so a float64 round trip would be detectable: three thirds of a
	// dollar must sum to exactly 999999 micros, not 999999.00000000001.
	for i := 0; i < 3; i++ {
		ev := sttEvent()
		ev.CostMicros = 333333
		if err := m.Record(context.Background(), ev); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	for _, k := range repo.Keys() {
		item, err := repo.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("reading %v: %v", k, err)
		}
		// The stored dynamic type matters, not just the value: the totalisers read this
		// attribute, and money that has been through a float64 cannot be summed exactly
		// across thousands of records (§Phase 0 requires agreement with the provider's
		// invoice within 5%, a tolerance that means nothing if the arithmetic drifts).
		if _, ok := item.Attrs["cost_micros"].(int64); !ok {
			t.Fatalf("cost_micros stored as %T, want int64", item.Attrs["cost_micros"])
		}
	}

	total, err := m.DayTotal(context.Background(), testTenant, "2026-08-04")
	if err != nil {
		t.Fatalf("DayTotal: %v", err)
	}
	if total != 999999 {
		t.Errorf("DayTotal is %d, want exactly 999999 micros", total)
	}
}

func TestCostSurvivesAValueFloat64CannotRepresent(t *testing.T) {
	// 2^53+1 is the smallest positive integer a float64 cannot hold. No real cost is
	// this large, but the assertion is about the pipeline being integer rather than
	// about the magnitude: if any stage converts through a float, this value changes.
	const beyondFloat64 int64 = 1<<53 + 1

	m, _ := newTestMeter(t, fixedNow, &seqIDs{}, 25)
	ev := sttEvent()
	ev.CostMicros = beyondFloat64
	if err := m.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	total, err := m.DayTotal(context.Background(), testTenant, "2026-08-04")
	if err != nil {
		t.Fatalf("DayTotal: %v", err)
	}
	if total != beyondFloat64 {
		t.Errorf("DayTotal is %d, want %d; a float64 stage would report %d",
			total, beyondFloat64, int64(float64(beyondFloat64)))
	}
}

// ---------------------------------------------------------------------------
// Windowing — the breaker's input
// ---------------------------------------------------------------------------

func TestDayTotalFiltersByDayWithinTheMonthPartition(t *testing.T) {
	// The sort key partitions by month, not by day (§6.3), so the day is a filter over
	// a month range. The case that matters is a record in the *same month* on a
	// different day: it is inside the range read and must be excluded by the filter.
	repo := repository.NewMemory()
	ids := &seqIDs{}

	type written struct {
		at   time.Time
		cost int64
	}
	records := []written{
		{time.Date(2026, 8, 4, 23, 59, 59, 0, time.UTC), 100}, // last second of the day
		{time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), 200},    // first second of the next
		{time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC), 50},     // first second of the day
		{time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), 400},   // a different partition
	}
	for _, w := range records {
		m := New(repo, clock.Fixed{T: w.at}, ids, discardLogger(), 25)
		ev := sttEvent()
		ev.CostMicros = w.cost
		if err := m.Record(context.Background(), ev); err != nil {
			t.Fatalf("Record at %s: %v", w.at, err)
		}
	}

	// Any meter reads the same records; the window is an argument, not state.
	m := New(repo, clock.Fixed{T: fixedNow}, ids, discardLogger(), 25)

	cases := map[string]struct {
		day  string
		want int64
	}{
		"the day itself, both boundary seconds": {"2026-08-04", 150},
		"the next day in the same partition":    {"2026-08-05", 200},
		"a day in another partition":            {"2026-09-01", 400},
		"a day with no records":                 {"2026-08-06", 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := m.DayTotal(context.Background(), testTenant, c.day)
			if err != nil {
				t.Fatalf("DayTotal(%s): %v", c.day, err)
			}
			if got != c.want {
				t.Errorf("DayTotal(%s) is %d, want %d", c.day, got, c.want)
			}
		})
	}

	// The month total is the same range without the filter, which is what makes the
	// filter's effect visible: 350 in August, 400 in September, and no leakage either
	// way across the partition boundary.
	for month, want := range map[string]int64{"2026-08": 350, "2026-09": 400, "2026-07": 0} {
		got, err := m.MonthTotal(context.Background(), testTenant, month)
		if err != nil {
			t.Fatalf("MonthTotal(%s): %v", month, err)
		}
		if got != want {
			t.Errorf("MonthTotal(%s) is %d, want %d", month, got, want)
		}
	}
}

func TestTotalsAreTenantScoped(t *testing.T) {
	// I11. A total that included another tenant's spend would refuse this tenant's
	// calls on someone else's usage — and would leak the fact of that usage.
	repo := repository.NewMemory()
	ids := &seqIDs{}
	m := New(repo, clock.Fixed{T: fixedNow}, ids, discardLogger(), 25)

	mine := sttEvent()
	mine.CostMicros = 10
	if err := m.Record(context.Background(), mine); err != nil {
		t.Fatalf("Record: %v", err)
	}
	theirs := sttEvent()
	theirs.Tenant = "t_someone_else"
	theirs.CostMicros = 1_000_000
	if err := m.Record(context.Background(), theirs); err != nil {
		t.Fatalf("Record for the other tenant: %v", err)
	}

	got, err := m.DayTotal(context.Background(), testTenant, "2026-08-04")
	if err != nil {
		t.Fatalf("DayTotal: %v", err)
	}
	if got != 10 {
		t.Errorf("DayTotal is %d, want 10; the other tenant's spend leaked in (I11)", got)
	}
}

func TestTotalsRefuseAMalformedWindow(t *testing.T) {
	// A window that resolves to no records must be an error, not zero. Zero is
	// indistinguishable from an unused day, and the breaker reads an unused day as full
	// headroom (§10.5.9) — so a typo'd window would disable the cap.
	m, _ := newTestMeter(t, fixedNow, &seqIDs{}, 25)

	dayCases := map[string]string{
		"empty":            "",
		"month only":       "2026-08",
		"single-digit day": "2026-8-4",
		"non-numeric day":  "2026-08-XX",
		"impossible day":   "2026-02-30",
		"impossible month": "2026-13-01",
	}
	for name, day := range dayCases {
		t.Run("day/"+name, func(t *testing.T) {
			_, err := m.DayTotal(context.Background(), testTenant, day)
			if err == nil {
				t.Errorf("DayTotal(%q) returned a total instead of an error", day)
			} else if !strings.Contains(err.Error(), "yyyy-mm-dd") {
				// The reason, not merely a failure: every one of these inputs errors under
				// *any* strict layout, so a bare error check would keep passing if the
				// layout were changed to one that also rejects the real "2026-08-04".
				t.Errorf("rejection did not name the expected format: %v", err)
			}
		})
	}

	monthCases := map[string]string{
		"empty":            "",
		"full date":        "2026-08-04",
		"impossible month": "2026-13",
		"single digit":     "2026-8",
	}
	for name, month := range monthCases {
		t.Run("month/"+name, func(t *testing.T) {
			_, err := m.MonthTotal(context.Background(), testTenant, month)
			if err == nil {
				t.Errorf("MonthTotal(%q) returned a total instead of an error", month)
			} else if !strings.Contains(err.Error(), "yyyy-mm") {
				t.Errorf("rejection did not name the expected format: %v", err)
			}
		})
	}
}

func TestTotalsRefuseAnEmptyTenant(t *testing.T) {
	// The windows here are valid on purpose, and the assertion is that *tenant_id* is what
	// was rejected. Checking only that an error came back made this test vacuous: a wrong
	// day layout errors before the tenant is ever looked at, so the I11 assertion it exists
	// to make would silently stop being made.
	m, _ := newTestMeter(t, fixedNow, &seqIDs{}, 25)
	for _, tenant := range []keys.TenantID{"", " "} {
		_, err := m.DayTotal(context.Background(), tenant, "2026-08-04")
		if err == nil {
			t.Errorf("DayTotal accepted tenant %q; every query is tenant-scoped (I11)", string(tenant))
		} else if !strings.Contains(err.Error(), "tenant_id") {
			t.Errorf("DayTotal refused tenant %q for another reason: %v", string(tenant), err)
		}
		_, err = m.MonthTotal(context.Background(), tenant, "2026-08")
		if err == nil {
			t.Errorf("MonthTotal accepted tenant %q (I11)", string(tenant))
		} else if !strings.Contains(err.Error(), "tenant_id") {
			t.Errorf("MonthTotal refused tenant %q for another reason: %v", string(tenant), err)
		}
	}
}

func TestTotalsFailRatherThanReturnZeroOnAStorageError(t *testing.T) {
	// This is the property the breaker's fail-closed behaviour rests on. A total of
	// (0, nil) on an unreadable ledger is an uncapped bill; (0, err) is a refused call.
	m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)
	if err := m.Record(context.Background(), sttEvent()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	boom := errors.New("dynamodb: request timed out")

	repo.FailNext(boom)
	if _, err := m.DayTotal(context.Background(), testTenant, "2026-08-04"); !errors.Is(err, boom) {
		t.Errorf("DayTotal returned %v; it must surface the storage failure", err)
	}
	repo.FailNext(boom)
	if _, err := m.MonthTotal(context.Background(), testTenant, "2026-08"); !errors.Is(err, boom) {
		t.Errorf("MonthTotal returned %v; it must surface the storage failure", err)
	}
}

// ---------------------------------------------------------------------------
// Unreadable records
// ---------------------------------------------------------------------------

// putRaw writes a usage-shaped record directly, so the totalisers can be tested against
// attribute shapes Record itself would never produce — a partially written record, or a
// number that a storage round trip returned as a float.
func putRaw(t *testing.T, repo *repository.Memory, id string, attrs map[string]any) {
	t.Helper()
	key, err := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), id)
	if err != nil {
		t.Fatalf("building key: %v", err)
	}
	if err := repo.PutOnce(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
		t.Fatalf("writing raw record: %v", err)
	}
}

func TestTotalsRefuseARecordWhoseCostCannotBeRead(t *testing.T) {
	cases := map[string]struct {
		attrs map[string]any
		want  string
	}{
		"cost absent": {
			// The dangerous shape: a discarded type assertion would count this as
			// free, and a day of them as a free day.
			attrs: map[string]any{"ts": "2026-08-04T10:30:00Z"},
			want:  "no cost_micros",
		},
		"cost as a string": {
			attrs: map[string]any{"ts": "2026-08-04T10:30:00Z", "cost_micros": "311"},
			want:  "not an exact integer number of micros",
		},
		"cost with a fraction": {
			attrs: map[string]any{"ts": "2026-08-04T10:30:00Z", "cost_micros": 1.5},
			want:  "not an exact integer number of micros",
		},
		"ts absent": {
			// Cannot be placed inside or outside the day, so it cannot be filtered
			// out either — omitting it would understate the day.
			attrs: map[string]any{"cost_micros": int64(311)},
			want:  "no usable ts",
		},
		"ts too short to carry a date": {
			attrs: map[string]any{"ts": "2026-08", "cost_micros": int64(311)},
			want:  "no usable ts",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)
			putRaw(t, repo, "01RAW00001", c.attrs)

			total, err := m.DayTotal(context.Background(), testTenant, "2026-08-04")
			if err == nil {
				t.Fatalf("DayTotal returned %d for an unreadable record; expected an error mentioning %q", total, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error did not mention %q: %v", c.want, err)
			}
		})
	}
}

func TestTotalsAcceptACostThatRoundTrippedAsAFloat(t *testing.T) {
	// The two Repository implementations do not agree on the Go type of a whole number,
	// which is what repository.AsInt64 exists for. A direct int64 assertion would pass
	// against the in-memory fake and read as zero against the real table — the worst
	// possible split, because the fake is what CI runs.
	m, repo := newTestMeter(t, fixedNow, &seqIDs{}, 25)
	putRaw(t, repo, "01RAW00001", map[string]any{
		"ts":          "2026-08-04T10:30:00Z",
		"cost_micros": float64(1_500_000),
	})
	putRaw(t, repo, "01RAW00002", map[string]any{
		"ts":          "2026-08-04T11:00:00Z",
		"cost_micros": 250_000, // an untyped int constant, the other plausible shape
	})

	total, err := m.DayTotal(context.Background(), testTenant, "2026-08-04")
	if err != nil {
		t.Fatalf("DayTotal: %v", err)
	}
	if total != 1_750_000 {
		t.Errorf("DayTotal is %d, want 1750000", total)
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewSubstitutesTheDocumentedRetentionDefault(t *testing.T) {
	// Pinning current behaviour rather than endorsing it. config already requires
	// retention.usage_months to be present and positive (§7.4), so this substitution is
	// unreachable from a validated config — but it is reachable from a test or a script
	// that constructs a Meter directly, and a TTL of "now" would expire records
	// immediately and destroy the cost ledger.
	for _, months := range []int{0, -1} {
		m, repo := newTestMeter(t, fixedNow, &seqIDs{}, months)
		if err := m.Record(context.Background(), sttEvent()); err != nil {
			t.Fatalf("Record: %v", err)
		}
		key, _ := keys.Usage(testTenant, "2026-08", string(model.UnitSTTSeconds), "01TEST0001")
		item, _ := repo.Get(context.Background(), key)
		want := time.Date(2028, 9, 4, 10, 30, 0, 0, time.UTC).Unix()
		if item.TTL != want {
			t.Errorf("ttlMonths=%d produced TTL %d, want the documented 25-month default %d", months, item.TTL, want)
		}
	}
}
