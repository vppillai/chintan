package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// A gate that was never wired must not refuse anything. This is the shape of a
// fresh install, and a default-closed gate there means no capture ever records.
func TestSpendGateIsOpenWithNoCounterWired(t *testing.T) {
	for name, gate := range map[string]*SpendGate{
		"nil gate":    nil,
		"nil counter": NewSpendGate(nil, nil, 1_000_000),
	} {
		capped, err := gate.Capped(context.Background(), "user1")
		if err != nil {
			t.Errorf("%s: Capped: %v", name, err)
		}
		if capped {
			t.Errorf("%s: Capped = true, want false: there is no counter to have reached a cap", name)
		}
	}
}

// A cap of zero is metering without enforcement, which is the documented
// instance default. The counter must still be left alone rather than read and
// ignored.
func TestSpendGateIsOpenWhenNoCapIsSet(t *testing.T) {
	counter := &stubCounter{total: 999_999_999}
	gate := NewSpendGate(counter, NewSettingsService(memory.NewStore()), 0)

	capped, err := gate.Capped(context.Background(), "user1")
	if err != nil {
		t.Fatalf("Capped: %v", err)
	}
	if capped {
		t.Fatalf("Capped = true with no cap configured; the counter reads %d but nothing is being enforced", counter.total)
	}
	if len(counter.days) != 0 {
		t.Errorf("the counter was read %d times with no cap to compare against", len(counter.days))
	}
}

func TestSpendGateClosesOnceTheCounterReachesTheCap(t *testing.T) {
	settings := NewSettingsService(memory.NewStore())

	for _, tc := range []struct {
		name  string
		total int64
		want  bool
	}{
		{"below the cap", 999, false},
		{"exactly at the cap", 1000, true},
		{"past the cap", 1001, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := NewSpendGate(&stubCounter{total: tc.total}, settings, 1000)

			capped, err := gate.Capped(context.Background(), "user1")
			if err != nil {
				t.Fatalf("Capped: %v", err)
			}
			if capped != tc.want {
				t.Fatalf("Capped = %v with total %d against a cap of 1000, want %v", capped, tc.total, tc.want)
			}
		})
	}
}

// The tenant's own budget wins over the instance default. Without this a tenant
// who set a tighter cap than the operator's keeps spending up to the
// operator's, which is the opposite of what they asked for.
func TestSpendGateLetsTheTenantsOwnCapOverrideTheInstanceDefault(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	if err := store.PutSettings(ctx, "user1", model.Settings{DailySpendCapMicros: 1000}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	// The instance default is a thousand times looser, so a capped answer here
	// can only have come from the tenant's own record.
	gate := NewSpendGate(&stubCounter{total: 1500}, NewSettingsService(store), 1_000_000)

	capped, err := gate.Capped(ctx, "user1")
	if err != nil {
		t.Fatalf("Capped: %v", err)
	}
	if !capped {
		t.Fatal("Capped = false: 1500 micros is past the tenant's own 1000 cap, so the instance default of 1000000 was used instead")
	}
}

func TestSpendGateFallsBackToTheInstanceDefaultWhenTheTenantSetNone(t *testing.T) {
	gate := NewSpendGate(&stubCounter{total: 1500}, NewSettingsService(memory.NewStore()), 1000)

	capped, err := gate.Capped(context.Background(), "user1")
	if err != nil {
		t.Fatalf("Capped: %v", err)
	}
	if !capped {
		t.Fatal("Capped = false: the tenant set no cap of their own, so the instance default of 1000 applies to a total of 1500")
	}
}

// A counter that cannot be read must not become a closed door. The breaker
// still refuses the provider call, so the worst case is a capture that fails
// later with a clear status rather than every capture refused up front.
func TestSpendGateSurfacesACounterFailureRatherThanClosingTheDoor(t *testing.T) {
	boom := errors.New("dynamodb: ThrottlingException on chintan-spend")
	gate := NewSpendGate(&stubCounter{err: boom}, NewSettingsService(memory.NewStore()), 1000)

	capped, err := gate.Capped(context.Background(), "user1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the counter's failure to reach the caller", err)
	}
	if capped {
		t.Fatal("Capped = true on a counter read failure; an unreadable counter must not refuse every capture")
	}
}

func TestSpendGateSurfacesASettingsFailure(t *testing.T) {
	boom := errors.New("dynamodb: dial tcp: connection refused")
	settings := NewSettingsService(errSettingsStore{Store: memory.NewStore(), err: boom})
	counter := &stubCounter{total: 5}
	gate := NewSpendGate(counter, settings, 1000)

	capped, err := gate.Capped(context.Background(), "user1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the settings failure to reach the caller", err)
	}
	if capped {
		t.Fatal("Capped = true when the tenant's cap could not be read")
	}
	if len(counter.days) != 0 {
		t.Errorf("the counter was read despite the cap being unknown: %v", counter.days)
	}
}

// The counter is partitioned per tenant per day, in UTC. A gate reading a local
// day would move a tenant's budget reset by hours and, west of UTC, read a
// partition the worker never writes.
func TestSpendGateReadsTodaysCounterInUTC(t *testing.T) {
	counter := &stubCounter{total: 1}
	gate := NewSpendGate(counter, NewSettingsService(memory.NewStore()), 1_000_000)
	// 00:30 on the 9th in UTC is still the 8th in every US timezone.
	gate.now = func() time.Time {
		return time.Date(2026, 8, 9, 0, 30, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	}

	if _, err := gate.Capped(context.Background(), "user1"); err != nil {
		t.Fatalf("Capped: %v", err)
	}
	if len(counter.days) != 1 || counter.days[0] != "2026-08-09" {
		t.Fatalf("counter days = %v, want one read of the UTC day 2026-08-09", counter.days)
	}
}

// The read is an ADD of zero: the counter's only operation is atomic, so
// reading through it means no second code path can disagree with what the
// breaker enforces.
func TestSpendGateReadsTheCounterWithoutSpendingAgainstIt(t *testing.T) {
	counter := &stubCounter{total: 400}
	gate := NewSpendGate(counter, NewSettingsService(memory.NewStore()), 1000)
	ctx := context.Background()

	for range 3 {
		if _, err := gate.Capped(ctx, "user1"); err != nil {
			t.Fatalf("Capped: %v", err)
		}
	}
	if counter.total != 400 {
		t.Fatalf("counter total = %d after three reads, want 400: the gate's read must add nothing", counter.total)
	}
}
