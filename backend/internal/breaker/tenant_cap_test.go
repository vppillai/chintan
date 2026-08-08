package breaker

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// stubCaps is a tenant cap lookup. It stands in for the settings record the
// user edits in the UI.
type stubCaps struct {
	mu   sync.Mutex
	caps map[string]int64
	err  error

	calls int
}

func (s *stubCaps) DailyCapMicros(_ context.Context, tenantID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	return s.caps[tenantID], nil
}

func (s *stubCaps) lookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// The cap the user set is the cap that has to stop the provider call. Honouring
// it only at POST /v1/captures leaves the whole expensive half of the work —
// transcription and two LLM calls — running against a number the tenant never
// chose.
func TestTenantCapRefusesTheProviderCall(t *testing.T) {
	counter := newMemCounter()
	caps := &stubCaps{caps: map[string]int64{"tenant-a": 50}}
	// The instance-wide cap is 0: the default, "record but never enforce".
	b := New(counter, nil, testPrices, 0, WithClock(fixedClock()), WithCapResolver(caps))

	called := false
	_, err := b.Do(context.Background(), "tenant-a", Estimate{
		Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Unit: meter.UnitAudioSeconds, Quantity: 20, // 200 micros, four times the cap
	}, func(context.Context) (Result, error) {
		called = true
		return Result{Unit: meter.UnitAudioSeconds, Quantity: 20}, nil
	})

	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err = %v, want ErrSpendCapExceeded: the tenant's own cap never reached the enforcement point", err)
	}
	if called {
		t.Fatal("the provider was called despite the tenant's daily cap being exceeded")
	}
	if caps.lookups() == 0 {
		t.Fatal("the breaker never asked what the tenant's cap is")
	}
	if got := counter.total("tenant-a", "2026-08-07"); got != 0 {
		t.Fatalf("day total = %d, want 0: a refused call must not hold the reservation", got)
	}
}

// A tenant under their own cap is not stopped by it.
func TestTenantCapAllowsACallWithinBudget(t *testing.T) {
	counter := newMemCounter()
	caps := &stubCaps{caps: map[string]int64{"tenant-a": 10_000}}
	b := New(counter, nil, testPrices, 0, WithClock(fixedClock()), WithCapResolver(caps))

	called := false
	if _, err := b.Do(context.Background(), "tenant-a", Estimate{
		Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Unit: meter.UnitAudioSeconds, Quantity: 20,
	}, func(context.Context) (Result, error) {
		called = true
		return Result{Unit: meter.UnitAudioSeconds, Quantity: 20}, nil
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !called {
		t.Fatal("a call well inside the tenant's cap was refused")
	}
}

// The instance-wide cap is the operator's ceiling. A tenant must not be able to
// raise their own limit past what the instance will pay for.
func TestInstanceCapStillBoundsAMoreGenerousTenantCap(t *testing.T) {
	counter := newMemCounter()
	caps := &stubCaps{caps: map[string]int64{"tenant-a": 1_000_000}}
	b := New(counter, nil, testPrices, 50, WithClock(fixedClock()), WithCapResolver(caps))

	called := false
	_, err := b.Do(context.Background(), "tenant-a", Estimate{
		Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Unit: meter.UnitAudioSeconds, Quantity: 20,
	}, func(context.Context) (Result, error) {
		called = true
		return Result{}, nil
	})
	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err = %v, want ErrSpendCapExceeded from the instance ceiling", err)
	}
	if called {
		t.Fatal("the instance-wide ceiling was not applied")
	}
}

// A tenant that has set no cap of their own falls back to the instance cap
// rather than to "no cap at all".
func TestAnUnsetTenantCapFallsBackToTheInstanceCap(t *testing.T) {
	counter := newMemCounter()
	caps := &stubCaps{caps: map[string]int64{}}
	b := New(counter, nil, testPrices, 50, WithClock(fixedClock()), WithCapResolver(caps))

	_, err := b.Do(context.Background(), "tenant-a", Estimate{
		Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Unit: meter.UnitAudioSeconds, Quantity: 20,
	}, func(context.Context) (Result, error) { return Result{}, nil })
	if !errors.Is(err, ErrSpendCapExceeded) {
		t.Fatalf("err = %v, want the instance cap to still apply when the tenant has set none", err)
	}
}

// A settings read that fails must not become an outage of the paid pipeline.
// The instance ceiling still applies, and the failure is reported rather than
// swallowed.
func TestAFailedCapLookupFallsBackToTheInstanceCap(t *testing.T) {
	counter := newMemCounter()
	caps := &stubCaps{err: errors.New("dynamodb throttled")}
	b := New(counter, nil, testPrices, 1_000_000, WithClock(fixedClock()), WithCapResolver(caps))

	called := false
	if _, err := b.Do(context.Background(), "tenant-a", Estimate{
		Provider: "groq", Model: "whisper", Op: meter.OpTranscribe,
		Unit: meter.UnitAudioSeconds, Quantity: 20,
	}, func(context.Context) (Result, error) {
		called = true
		return Result{}, nil
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !called {
		t.Fatal("a transient settings read failure stopped a call the instance cap allows")
	}
}
