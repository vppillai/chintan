package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A gate that was never wired must not refuse anything. This is the shape of a
// fresh install, and a default-closed gate there means no capture ever records.
func TestSpendGateIsOpenWithNoCounterWired(t *testing.T) {
	for name, gate := range map[string]*SpendGate{
		"nil gate":    nil,
		"nil counter": NewSpendGate(nil, 1_000_000),
	} {
		capped, err := gate.Capped(context.Background())
		if err != nil {
			t.Errorf("%s: Capped: %v", name, err)
		}
		if capped {
			t.Errorf("%s: Capped = true, want false: there is no counter to have reached a cap", name)
		}
	}
}

// A cap of zero is counting without enforcement, which is the documented
// instance default. The counter must still be left alone rather than read and
// ignored.
func TestSpendGateIsOpenWhenNoCapIsSet(t *testing.T) {
	counter := &stubCounter{total: 999_999_999}
	gate := NewSpendGate(counter, 0)

	capped, err := gate.Capped(context.Background())
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
	for _, tc := range []struct {
		name  string
		total int64
		want  bool
	}{
		{"well under", 10, false},
		{"one short", 999, false},
		{"exactly at", 1000, true},
		{"over", 1500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := NewSpendGate(&stubCounter{total: tc.total}, 1000)
			capped, err := gate.Capped(context.Background())
			if err != nil {
				t.Fatalf("Capped: %v", err)
			}
			if capped != tc.want {
				t.Fatalf("Capped = %v at %d of 1000, want %v", capped, tc.total, tc.want)
			}
		})
	}
}

// A counter that cannot be read must not become a closed door. The breaker
// still refuses the provider call, so the worst case is a capture that fails
// later with a clear status rather than an outage at the upload.
func TestSpendGateSurfacesACounterFailureRatherThanClosingTheDoor(t *testing.T) {
	boom := errors.New("dynamodb throttled")
	gate := NewSpendGate(&stubCounter{err: boom}, 1000)

	capped, err := gate.Capped(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the counter's failure surfaced", err)
	}
	if capped {
		t.Fatal("a failed read was reported as capped")
	}
}

// The counter is partitioned per day, in UTC. A gate reading a local day would
// move the budget reset by hours and, west of UTC, read a row the worker never
// writes.
func TestSpendGateReadsTodaysCounterInUTC(t *testing.T) {
	counter := &stubCounter{total: 1}
	gate := NewSpendGate(counter, 1_000_000)
	// 00:30 on the 9th in UTC is still the 8th in every US timezone.
	gate.now = func() time.Time {
		return time.Date(2026, 8, 9, 0, 30, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	}

	if _, err := gate.Capped(context.Background()); err != nil {
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
	gate := NewSpendGate(counter, 1000)
	ctx := context.Background()

	for range 3 {
		if _, err := gate.Capped(ctx); err != nil {
			t.Fatalf("Capped: %v", err)
		}
	}
	if counter.total != 400 {
		t.Fatalf("counter total = %d after three reads, want 400: the gate's read must add nothing", counter.total)
	}
}
