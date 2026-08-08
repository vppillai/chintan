package breaker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// memCounter is an atomic accumulator, matching the contract the DynamoDB
// implementation must honour.
type memCounter struct {
	mu     sync.Mutex
	totals map[string]int64
	err    error
	calls  []int64
}

func newMemCounter() *memCounter {
	return &memCounter{totals: map[string]int64{}}
}

func (m *memCounter) Add(_ context.Context, tenantID, day string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	m.calls = append(m.calls, delta)
	k := tenantID + "|" + day
	m.totals[k] += delta
	return m.totals[k], nil
}

func (m *memCounter) total(tenantID, day string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals[tenantID+"|"+day]
}

type capturingSink struct {
	mu      sync.Mutex
	records []meter.Usage
}

func (s *capturingSink) Record(_ context.Context, u meter.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, u)
	return nil
}

func (s *capturingSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

var testPrices = meter.PriceTable{
	meter.Key("groq", "*"): {meter.UnitAudioSeconds: 10}, // 10 micros per second
}

func fixedClock() func() time.Time {
	t := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newTestBreaker(c Counter, s meter.Sink, capMicros int64) *Breaker {
	return New(c, s, testPrices, capMicros, WithClock(fixedClock()))
}

func estimate(seconds float64) Estimate {
	return Estimate{Provider: "groq", Model: "whisper", Op: meter.OpTranscribe, Unit: meter.UnitAudioSeconds, Quantity: seconds}
}

func TestDoRunsAndRecordsWithinCap(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 10_000)

	called := false
	res, err := b.Do(context.Background(), "tenant-1", estimate(60), func(context.Context) (Result, error) {
		called = true
		return Result{Unit: meter.UnitAudioSeconds, Quantity: 60}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !called {
		t.Fatal("provider function was not called")
	}
	if res.Quantity != 60 {
		t.Fatalf("result=%+v", res)
	}
	if got := counter.total("tenant-1", "2026-08-07"); got != 600 {
		t.Fatalf("spend total=%d, want 600", got)
	}
	if sink.len() != 1 {
		t.Fatalf("expected exactly one usage record, got %d", sink.len())
	}
	if rec := sink.records[0]; rec.CostMicros != 600 || rec.TenantID != "tenant-1" {
		t.Fatalf("record=%+v", rec)
	}
}

// The whole point: the provider is unreachable once the cap is hit.
func TestDoRefusesToCallProviderOverCap(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 500) // 500 micros = 50 seconds of audio

	called := false
	_, err := b.Do(context.Background(), "tenant-1", estimate(60), func(context.Context) (Result, error) {
		called = true
		return Result{}, nil
	})

	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err=%v, want ErrSpendCapExceeded", err)
	}
	if called {
		t.Fatal("provider was called despite the cap being exceeded")
	}
	if sink.len() != 0 {
		t.Fatal("a rejected call must not record usage")
	}
	// A rejected call must not hold budget.
	if got := counter.total("tenant-1", "2026-08-07"); got != 0 {
		t.Fatalf("reservation was not released: total=%d", got)
	}
}

func TestDoReleasesReservationWhenProviderFails(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 10_000)

	boom := errors.New("provider exploded")
	_, err := b.Do(context.Background(), "tenant-1", estimate(60), func(context.Context) (Result, error) {
		return Result{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if got := counter.total("tenant-1", "2026-08-07"); got != 0 {
		t.Fatalf("a failed call consumed budget: total=%d", got)
	}
	if sink.len() != 0 {
		t.Fatal("a failed call must not record usage")
	}
}

func TestDoReconcilesUnderAndOverEstimates(t *testing.T) {
	for _, tc := range []struct {
		name           string
		estimateSecs   float64
		actualSecs     float64
		wantTotalMicro int64
	}{
		{"actual below estimate", 120, 30, 300},
		{"actual above estimate", 30, 120, 1200},
		{"actual equals estimate", 60, 60, 600},
		{"actual unmeasured falls back to estimate", 60, 0, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter, sink := newMemCounter(), &capturingSink{}
			b := newTestBreaker(counter, sink, 100_000)

			_, err := b.Do(context.Background(), "t", estimate(tc.estimateSecs), func(context.Context) (Result, error) {
				return Result{Unit: meter.UnitAudioSeconds, Quantity: tc.actualSecs}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if got := counter.total("t", "2026-08-07"); got != tc.wantTotalMicro {
				t.Fatalf("total=%d want %d", got, tc.wantTotalMicro)
			}
			if got := sink.records[0].CostMicros; got != tc.wantTotalMicro {
				t.Fatalf("recorded cost=%d want %d", got, tc.wantTotalMicro)
			}
		})
	}
}

func TestDoAccumulatesAcrossCallsUntilCap(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 1_000) // 100 seconds of audio

	for i := 0; i < 3; i++ {
		if _, err := b.Do(context.Background(), "t", estimate(30), func(context.Context) (Result, error) {
			return Result{Unit: meter.UnitAudioSeconds, Quantity: 30}, nil
		}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// 3 x 300 = 900, still under 1000.
	if got := counter.total("t", "2026-08-07"); got != 900 {
		t.Fatalf("total=%d", got)
	}

	_, err := b.Do(context.Background(), "t", estimate(30), func(context.Context) (Result, error) {
		t.Fatal("fourth call should not reach the provider")
		return Result{}, nil
	})
	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// One tenant exhausting its budget must not affect another.
func TestCapIsPerTenant(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 500)

	if _, err := b.Do(context.Background(), "heavy", estimate(60), func(context.Context) (Result, error) {
		return Result{}, nil
	}); !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("heavy tenant: %v", err)
	}

	called := false
	if _, err := b.Do(context.Background(), "light", estimate(10), func(context.Context) (Result, error) {
		called = true
		return Result{Unit: meter.UnitAudioSeconds, Quantity: 10}, nil
	}); err != nil {
		t.Fatalf("light tenant was blocked by another tenant's spend: %v", err)
	}
	if !called {
		t.Fatal("light tenant's call did not run")
	}
}

// A cap of zero means "measure but do not enforce" — correct for a fresh
// install with no spend history to set a cap from.
func TestZeroCapMetersWithoutEnforcing(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 0)

	for i := 0; i < 5; i++ {
		if _, err := b.Do(context.Background(), "t", estimate(3600), func(context.Context) (Result, error) {
			return Result{Unit: meter.UnitAudioSeconds, Quantity: 3600}, nil
		}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if sink.len() != 5 {
		t.Fatalf("expected 5 usage records, got %d", sink.len())
	}
}

func TestDoFailsWhenCounterUnavailable(t *testing.T) {
	counter := newMemCounter()
	counter.err = errors.New("dynamo down")
	b := newTestBreaker(counter, &capturingSink{}, 1000)

	called := false
	_, err := b.Do(context.Background(), "t", estimate(10), func(context.Context) (Result, error) {
		called = true
		return Result{}, nil
	})
	if err == nil {
		t.Fatal("expected an error when the counter is unavailable")
	}
	if called {
		t.Fatal("provider was called without a successful reservation")
	}
}

// A sink failure must not fail a call the provider already completed and billed.
func TestSinkFailureDoesNotFailTheCall(t *testing.T) {
	b := New(newMemCounter(), failingSink{}, testPrices, 10_000, WithClock(fixedClock()))
	res, err := b.Do(context.Background(), "t", estimate(10), func(context.Context) (Result, error) {
		return Result{Unit: meter.UnitAudioSeconds, Quantity: 10}, nil
	})
	if err != nil {
		t.Fatalf("a metering failure must not fail the call: %v", err)
	}
	if res.Quantity != 10 {
		t.Fatalf("result=%+v", res)
	}
}

type failingSink struct{}

func (failingSink) Record(context.Context, meter.Usage) error { return errors.New("sink down") }

func TestConcurrentCallsAccountExactly(t *testing.T) {
	counter, sink := newMemCounter(), &capturingSink{}
	b := newTestBreaker(counter, sink, 0) // no cap, we are checking accounting

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Do(context.Background(), "t", estimate(10), func(context.Context) (Result, error) {
				return Result{Unit: meter.UnitAudioSeconds, Quantity: 10}, nil
			})
		}()
	}
	wg.Wait()

	if got := counter.total("t", "2026-08-07"); got != 20*100 {
		t.Fatalf("total=%d, want %d", got, 20*100)
	}
	if sink.len() != 20 {
		t.Fatalf("records=%d", sink.len())
	}
}
