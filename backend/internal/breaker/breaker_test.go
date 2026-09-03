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

func (m *memCounter) Add(_ context.Context, day string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	m.calls = append(m.calls, delta)
	m.totals[day] += delta
	return m.totals[day], nil
}

func (m *memCounter) total(day string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals[day]
}

var testPrices = meter.PriceTable{
	meter.Key("groq", "*"):   {meter.UnitAudioSeconds: 10}, // 10 micros per second
	meter.Key("openai", "*"): {meter.UnitInputTokens: 1, meter.UnitOutputTokens: 4},
}

const testDay = "2026-08-07"

func fixedClock() func() time.Time {
	t := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newTestBreaker(c Counter, capMicros int64) *Breaker {
	return New(c, testPrices, capMicros, WithClock(fixedClock()))
}

func estimate(seconds float64) Estimate {
	return Estimate{Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Usage: meter.Quantities{meter.UnitAudioSeconds: seconds}}
}

func seconds(n float64) Result {
	return Result{Usage: meter.Quantities{meter.UnitAudioSeconds: n}}
}

func TestDoRunsAndCountsWithinCap(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 10_000)

	called := false
	res, err := b.Do(context.Background(), estimate(60), func(context.Context) (Result, error) {
		called = true
		return seconds(60), nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !called {
		t.Fatal("provider function was not called")
	}
	if res.Usage[meter.UnitAudioSeconds] != 60 {
		t.Fatalf("result=%+v", res)
	}
	if got := counter.total(testDay); got != 600 {
		t.Fatalf("spend total=%d, want 600", got)
	}
}

// The whole point: the provider is unreachable once the cap is hit.
func TestDoRefusesToCallProviderOverCap(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 500) // 500 micros = 50 seconds of audio

	called := false
	_, err := b.Do(context.Background(), estimate(60), func(context.Context) (Result, error) {
		called = true
		return Result{}, nil
	})

	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err=%v, want ErrSpendCapExceeded", err)
	}
	if called {
		t.Fatal("provider was called despite the cap being exceeded")
	}
	// A rejected call must not hold budget.
	if got := counter.total(testDay); got != 0 {
		t.Fatalf("reservation was not released: total=%d", got)
	}
}

func TestDoReleasesReservationWhenProviderFails(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 10_000)

	boom := errors.New("provider exploded")
	_, err := b.Do(context.Background(), estimate(60), func(context.Context) (Result, error) {
		return Result{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if got := counter.total(testDay); got != 0 {
		t.Fatalf("a failed call consumed budget: total=%d", got)
	}
}

func TestDoReconcilesUnderAndOverEstimates(t *testing.T) {
	for _, tc := range []struct {
		name           string
		estimateSecs   float64
		actual         Result
		wantTotalMicro int64
	}{
		{"actual below estimate", 120, seconds(30), 300},
		{"actual above estimate", 30, seconds(120), 1200},
		{"actual equals estimate", 60, seconds(60), 600},
		{"actual unmeasured falls back to estimate", 60, Result{}, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := newMemCounter()
			b := newTestBreaker(counter, 100_000)

			_, err := b.Do(context.Background(), estimate(tc.estimateSecs), func(context.Context) (Result, error) {
				return tc.actual, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if got := counter.total(testDay); got != tc.wantTotalMicro {
				t.Fatalf("total=%d want %d", got, tc.wantTotalMicro)
			}
		})
	}
}

// A completion is charged for its output tokens as well as its input tokens.
// The 2026-09-03 review found the pipeline reporting only input, so cleanup —
// whose output is about as long as its input and priced four times higher —
// was understated roughly five-fold, and the cap bound at a fraction of the
// dollars the operator set.
func TestDoPricesOutputTokensToo(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 0)

	est := Estimate{Provider: "openai", Model: "m", Op: meter.OpCleanup,
		Usage: meter.Quantities{meter.UnitInputTokens: 1000, meter.UnitOutputTokens: 1000}}
	_, err := b.Do(context.Background(), est, func(context.Context) (Result, error) {
		return Result{Usage: meter.Quantities{meter.UnitInputTokens: 800, meter.UnitOutputTokens: 600}}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// 800 in at 1 + 600 out at 4. Input-only accounting would say 800.
	if got := counter.total(testDay); got != 800+2400 {
		t.Fatalf("total=%d, want 3200: output tokens are not being priced", got)
	}
}

func TestDoAccumulatesAcrossCallsUntilCap(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 1_000) // 100 seconds of audio

	for i := 0; i < 3; i++ {
		if _, err := b.Do(context.Background(), estimate(30), func(context.Context) (Result, error) {
			return seconds(30), nil
		}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// 3 x 300 = 900, still under 1000.
	if got := counter.total(testDay); got != 900 {
		t.Fatalf("total=%d", got)
	}

	_, err := b.Do(context.Background(), estimate(30), func(context.Context) (Result, error) {
		t.Fatal("fourth call should not reach the provider")
		return Result{}, nil
	})
	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// A cap of zero means "count but do not enforce" — correct for a fresh install
// with no spend history to set a cap from.
func TestZeroCapCountsWithoutEnforcing(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 0)

	for i := 0; i < 5; i++ {
		if _, err := b.Do(context.Background(), estimate(3600), func(context.Context) (Result, error) {
			return seconds(3600), nil
		}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := counter.total(testDay); got != 5*36_000 {
		t.Fatalf("total=%d, want every call counted", got)
	}
}

func TestDoFailsWhenCounterUnavailable(t *testing.T) {
	counter := newMemCounter()
	counter.err = errors.New("dynamo down")
	b := newTestBreaker(counter, 1000)

	called := false
	_, err := b.Do(context.Background(), estimate(10), func(context.Context) (Result, error) {
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

func TestConcurrentCallsAccountExactly(t *testing.T) {
	counter := newMemCounter()
	b := newTestBreaker(counter, 0) // no cap, we are checking accounting

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Do(context.Background(), estimate(10), func(context.Context) (Result, error) {
				return seconds(10), nil
			})
		}()
	}
	wg.Wait()

	if got := counter.total(testDay); got != 20*100 {
		t.Fatalf("total=%d, want %d", got, 20*100)
	}
}
