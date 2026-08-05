package breaker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Almost every test here is a negative one, because the only failure this package has
// that costs money is failing *open*. A breaker that permits a call it should have
// refused produces no error, no log line, and a provider invoice a month later — so the
// assertions are overwhelmingly "the call did not happen, and for the stated reason".

const testTenant keys.TenantID = "t_01HTEST"

// fixedNow is mid-month and mid-day on purpose: a midnight value would let a
// window-arithmetic bug pass by coincidence.
var fixedNow = time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)

// Ledger is satisfied by the real meter. Asserted at compile time so a change to either
// method's signature breaks this package's build rather than leaving the breaker with a
// ledger nothing can supply.
var _ Ledger = (*meter.Meter)(nil)

type ledgerKey struct {
	tenant keys.TenantID
	day    string
}

// fakeLedger stands in for the usage ledger. The interface exists for this: a repository
// fake cannot simulate a read that fails, a read that never happens, or a read that
// hangs, and all three are properties under test.
//
// **Record moves the day's total**, so this fake models the ledger catching up. The
// previous version never did, which is exactly why it could not see a completed call
// whose cost was in neither `pending` nor `spent` — the fake made the bug untestable.
type fakeLedger struct {
	mu        sync.Mutex
	creditDay string
	spent     map[ledgerKey]int64
	reads     []string
	recorded  []meter.Event
	readErr   error
	recordErr error

	// hold blocks DayTotal for one tenant until its channel is closed, which is how a
	// slow ledger read is simulated. Waited on with f.mu released, so a blocked read for
	// one tenant does not block another tenant's read *inside the fake* — otherwise the
	// fake's own lock would masquerade as the breaker blocking.
	hold map[keys.TenantID]chan struct{}

	// onRecord runs inside Record before the ledger moves, so a test can observe the
	// breaker's state at the instant the cost is being written.
	onRecord func()
}

func newFakeLedger(spentToday int64) *fakeLedger {
	l := &fakeLedger{
		creditDay: clock.Date(fixedNow),
		spent:     map[ledgerKey]int64{},
		hold:      map[keys.TenantID]chan struct{}{},
	}
	l.spent[ledgerKey{testTenant, l.creditDay}] = spentToday
	return l
}

func (f *fakeLedger) DayTotal(_ context.Context, tenant keys.TenantID, day string) (int64, error) {
	f.mu.Lock()
	f.reads = append(f.reads, day)
	hold := f.hold[tenant]
	err := f.readErr
	f.mu.Unlock()

	if hold != nil {
		<-hold
	}
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spent[ledgerKey{tenant, day}], nil
}

func (f *fakeLedger) Record(_ context.Context, ev meter.Event) error {
	if f.onRecord != nil {
		f.onRecord()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, ev)
	f.spent[ledgerKey{ev.Tenant, f.creditDay}] += ev.CostMicros
	return nil
}

func (f *fakeLedger) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

func (f *fakeLedger) totalFor(tenant keys.TenantID, day string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spent[ledgerKey{tenant, day}]
}

func (f *fakeLedger) records() []meter.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]meter.Event(nil), f.recorded...)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestBreaker builds a breaker over a ledger holding `spentToday` micros on the fixed
// clock's day.
func newTestBreaker(t *testing.T, capMicros, spentToday int64) (*Breaker, *fakeLedger) {
	t.Helper()
	l := newFakeLedger(spentToday)
	b, err := New(l, clock.Fixed{T: fixedNow}, discardLogger(), capMicros)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, l
}

// mustNotRun is the closure every refusal test guards with. Asserting on a bool the
// caller sets is weaker: a test that forgets to check it silently passes.
func mustNotRun(t *testing.T) func(context.Context) (Cost, error) {
	t.Helper()
	return func(context.Context) (Cost, error) {
		t.Error("the provider call ran despite a refusal; the breaker failed open")
		return Cost{}, nil
	}
}

// costOf is a complete cost report for one STT call. Complete on purpose: a partially
// populated Cost is a different case (see the unmetered tests), and a test that reported
// one by accident would be asserting the wrong path.
func costOf(micros int64) Cost {
	return Cost{
		Unit:       model.UnitSTTSeconds,
		Quantity:   28,
		Provider:   "provider-a",
		CostMicros: micros,
		Op:         "transcribe",
	}
}

// succeeds is a guarded call that completed and cost what it says.
func succeeds(micros int64) func(context.Context) (Cost, error) {
	return func(context.Context) (Cost, error) { return costOf(micros), nil }
}

// movingClock is a clock a test can advance in place, which clock.Fixed cannot be
// (Advance returns a new value). The unmetered retention lives inside the Breaker, so the
// day has to roll over underneath the same Breaker for it to be observable.
type movingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.UTC()
}

func (c *movingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewRefusesAnUnusableConfiguration(t *testing.T) {
	ledger := newFakeLedger(0)
	clk := clock.Fixed{T: fixedNow}

	cases := map[string]struct {
		ledger Ledger
		clk    clock.Clock
		cap    int64
		want   string
	}{
		"no ledger": {nil, clk, 1000, "ledger is required"},
		"no clock":  {ledger, nil, 1000, "clock is required"},
		// The dangerous one. A zero cap has two plausible readings — "unlimited" and
		// "nothing permitted" — and both are wrong: an absent cap must fail the deploy.
		"zero cap":     {ledger, clk, 0, "must be positive"},
		"negative cap": {ledger, clk, -1, "must be positive"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := New(c.ledger, c.clk, discardLogger(), c.cap)
			if err == nil {
				t.Fatalf("New was accepted; expected refusal mentioning %q", c.want)
			}
			if b != nil {
				t.Error("New returned a breaker alongside an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal did not mention %q: %v", c.want, err)
			}
			if !strings.HasPrefix(err.Error(), "breaker:") {
				t.Errorf("error %q is not prefixed with the package name", err)
			}
		})
	}
}

func TestNewToleratesANilLogger(t *testing.T) {
	// A nil logger must not turn a refusal into a panic: a panic on the provider-call
	// path reads as a provider fault, and that misreading is what leads someone to
	// disable the breaker.
	b, err := New(newFakeLedger(0), clock.Fixed{T: fixedNow}, nil, 1000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.Do(context.Background(), testTenant, 2000, mustNotRun(t)); !Refused(err) {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Money conversion
// ---------------------------------------------------------------------------

func TestMicrosFromUSDConvertsTheConfiguredCaps(t *testing.T) {
	// §10.7: dev's cap is deliberately lower than prod's — 0.50 against 2.00 — because
	// "dev is where a runaway loop gets written". Both must convert exactly.
	cases := map[float64]int64{
		0.50:     500_000,
		2.00:     2_000_000,
		0.000001: 1,
		0.07:     70_000, // does not represent exactly in binary; rounding, not truncation
		1.999999: 1_999_999,
		// The largest accepted cap must still convert exactly rather than sitting one
		// rounding error away from the guard that admits it.
		maxCapUSD: 9_000_000_000_000_000_000,
	}
	for usd, want := range cases {
		got, err := MicrosFromUSD(usd)
		if err != nil {
			t.Errorf("MicrosFromUSD(%v): %v", usd, err)
			continue
		}
		if got != want {
			t.Errorf("MicrosFromUSD(%v) is %d, want %d", usd, got, want)
		}
	}
}

func TestMicrosFromUSDRefusesANonCap(t *testing.T) {
	for _, usd := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := MicrosFromUSD(usd); err == nil {
			t.Errorf("MicrosFromUSD(%v) was accepted; a cap that is not a positive finite amount must fail the deploy", usd)
		}
	}
}

func TestMicrosFromUSDRefusesAnAmountThatWouldSaturateInt64(t *testing.T) {
	// Converting an out-of-range float64 to int64 is implementation-defined in Go: arm64
	// — which §10.1 makes mandatory for the Lambdas — saturates to MaxInt64, amd64 wraps
	// to MinInt64. Unchecked, `daily_spend_usd: 2.0e15` gave production a nil error and a
	// cap of ~$9.2 trillion that never fires, while the identical config failed the
	// deploy on an x86 dev box. So this test asserts a *refusal*, which is architecture
	// independent, rather than the value the conversion would have produced.
	// 1e13 is just over the representable range, 9.3e12 just over the guard, 2.0e15 the
	// mistyped-exponent case the finding names, and the rest are far beyond.
	for _, usd := range []float64{1e13, 9.3e12, 2.0e15, 1e19, 1e300, math.MaxFloat64} {
		got, err := MicrosFromUSD(usd)
		if err == nil {
			t.Errorf("MicrosFromUSD(%v) returned %d with no error; on arm64 that is an effectively unlimited cap and New would accept it", usd, got)
			continue
		}
		if got != 0 {
			t.Errorf("MicrosFromUSD(%v) returned %d alongside its error", usd, got)
		}
		if !strings.Contains(err.Error(), "representable") {
			t.Errorf("refusal of %v did not name the reason: %v", usd, err)
		}
	}
}

func TestNoUSDCapEverConvertsToASaturatedValue(t *testing.T) {
	// The property behind the test above, stated directly: whatever a config file holds,
	// MicrosFromUSD never hands New a saturated bound. MaxInt64 micros would be a cap
	// that cannot be reached; MinInt64 would be refused by New on x86 and is the value
	// FormatUSD used to render as "--9223372036854.-775808".
	for _, usd := range []float64{0.5, 2, 1e6, maxCapUSD, 1e13, 1e300, -1e300, math.NaN()} {
		got, err := MicrosFromUSD(usd)
		if err != nil {
			continue
		}
		if got == math.MaxInt64 || got == math.MinInt64 {
			t.Errorf("MicrosFromUSD(%v) is %d, a saturated int64", usd, got)
		}
		if got <= 0 {
			t.Errorf("MicrosFromUSD(%v) is %d; an accepted cap must be positive", usd, got)
		}
	}
}

func TestFormatUSDIsExact(t *testing.T) {
	// Rendered with integer arithmetic: formatting money through a float is exactly what
	// storing micros exists to avoid, and these strings are reconciled against a
	// provider invoice.
	cases := map[int64]string{
		0:          "0.000000",
		1:          "0.000001",
		500_000:    "0.500000",
		2_000_000:  "2.000000",
		-1_500_000: "-1.500000",
		// The extremes of int64. MinInt64 negated as an int64 stays negative, which
		// produced "--9223372036854.-775808" — a malformed money string in an
		// operator-facing message, reachable from a cost model computing int64(someFloat).
		math.MaxInt64: "9223372036854.775807",
		math.MinInt64: "-9223372036854.775808",
	}
	for micros, want := range cases {
		if got := FormatUSD(micros); got != want {
			t.Errorf("FormatUSD(%d) is %q, want %q", micros, got, want)
		}
	}
}

func TestARefusalMessageIsWellFormedMoneyAtTheExtremes(t *testing.T) {
	// The rendering bug only matters because these values reach a real message: a caller
	// whose cost model computes int64(someFloat) produces exactly MinInt64 on amd64 when
	// that float is NaN or out of range, and the refusal it gets is what an operator reads.
	b, _ := newTestBreaker(t, 1_000, 0)
	for _, estimate := range []int64{math.MinInt64, math.MaxInt64} {
		err := b.Do(context.Background(), testTenant, estimate, mustNotRun(t))
		if err == nil {
			t.Fatalf("estimate %d was accepted", estimate)
		}
		msg := err.Error()
		if strings.Contains(msg, "--") || strings.Contains(msg, ".-") {
			t.Errorf("refusal message renders a malformed amount: %s", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// The cap itself — §Phase 0 acceptance, "verified with a low test cap"
// ---------------------------------------------------------------------------

func TestDoRefusesOnceTheCapWouldBeCrossed(t *testing.T) {
	const capMicros = 1_000 // a deliberately low test cap (§Phase 0 acceptance)

	cases := map[string]struct {
		spent    int64
		estimate int64
		allow    bool
	}{
		"well inside the cap":          {100, 100, true},
		"exactly reaching the cap":     {900, 100, true},
		"one micro over the cap":       {900, 101, false},
		"already at the cap":           {1_000, 1, false},
		"already past the cap":         {5_000, 1, false},
		"a single call larger than it": {0, 1_001, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, _ := newTestBreaker(t, capMicros, c.spent)

			ran := false
			err := b.Do(context.Background(), testTenant, c.estimate, func(context.Context) (Cost, error) {
				ran = true
				return costOf(c.estimate), nil
			})

			if c.allow {
				if err != nil {
					t.Fatalf("call was refused: %v", err)
				}
				if !ran {
					t.Error("call was permitted but never ran")
				}
				return
			}

			if ran {
				t.Error("the provider call ran despite the cap being exceeded")
			}
			if !errors.Is(err, ErrCapExceeded) {
				t.Fatalf("error %v is not ErrCapExceeded; a caller must be able to tell a refusal it cannot retry today from a provider fault it should", err)
			}
			if !Refused(err) {
				t.Error("Refused did not recognise the error")
			}
			if errors.Is(err, ErrSpendUnknown) {
				t.Error("a cap refusal also matched ErrSpendUnknown; the retry decision depends on the two being distinct")
			}
		})
	}
}

func TestTheWindowIsTheClocksDayAndTomorrowResetsIt(t *testing.T) {
	// The cap is per day, so the only thing that clears an exceeded cap is the day
	// changing — which is also why ErrCapExceeded must not be retried before then.
	ledger := newFakeLedger(0)
	ledger.spent[ledgerKey{testTenant, "2026-08-04"}] = 1_000
	ledger.spent[ledgerKey{testTenant, "2026-08-05"}] = 0

	today := clock.Fixed{T: fixedNow}
	b, err := New(ledger, today, discardLogger(), 1_000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.Do(context.Background(), testTenant, 1, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("expected today's spend to refuse the call, got %v", err)
	}

	tomorrow, err := New(ledger, today.Advance(24*time.Hour), discardLogger(), 1_000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ran := false
	if err := tomorrow.Do(context.Background(), testTenant, 1, func(context.Context) (Cost, error) {
		ran = true
		return costOf(1), nil
	}); err != nil {
		t.Fatalf("tomorrow's call was refused: %v", err)
	}
	if !ran {
		t.Error("tomorrow's call did not run")
	}
	if ledger.reads[0] != "2026-08-04" || ledger.reads[1] != "2026-08-05" {
		t.Errorf("windows read were %v, want the clock's two days", ledger.reads)
	}
}

// ---------------------------------------------------------------------------
// Fail closed — the single most important property in the package
// ---------------------------------------------------------------------------

func TestDoRefusesWhenTheLedgerCannotBeRead(t *testing.T) {
	// An open breaker on an unreadable ledger is an uncapped bill, and whatever is wrong
	// with storage is unlikely to be limited to one read.
	b, ledger := newTestBreaker(t, 1_000_000, 0)
	boom := errors.New("dynamodb: request timed out")
	ledger.readErr = boom

	err := b.Do(context.Background(), testTenant, 1, mustNotRun(t))
	if err == nil {
		t.Fatal("the call was permitted on an unreadable ledger")
	}
	if !errors.Is(err, ErrSpendUnknown) {
		t.Errorf("error %v is not ErrSpendUnknown", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the storage failure; an operator needs the cause", err)
	}
	if errors.Is(err, ErrCapExceeded) {
		// This one is retryable and the cap refusal is not. Conflating them either
		// wedges a pipeline until midnight or spins a tight retry loop.
		t.Error("an unreadable ledger reported itself as a cap refusal")
	}
	if !Refused(err) {
		t.Error("Refused did not recognise the error")
	}
}

func TestDoRefusesOnAnExpiredContextWithoutConsultingTheLedger(t *testing.T) {
	// The in-memory repository ignores the context, and a caching layer might too, so a
	// spend figure could come back for an already-dead deadline and be trusted.
	// "Cannot determine spend" has to be independent of the storage layer's context
	// discipline.
	cases := map[string]func() (context.Context, context.CancelFunc){
		"cancelled": func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		},
		"deadline passed": func() (context.Context, context.CancelFunc) {
			// A zero timeout is already expired, so this does not depend on the wall
			// clock — and the wall clock is the one clock a context deadline uses.
			return context.WithTimeout(context.Background(), 0)
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			b, ledger := newTestBreaker(t, 1_000_000, 0)
			ctx, cancel := mk()
			defer cancel()

			err := b.Do(ctx, testTenant, 1, mustNotRun(t))
			if !errors.Is(err, ErrSpendUnknown) {
				t.Fatalf("error %v is not ErrSpendUnknown", err)
			}
			if ledger.readCount() != 0 {
				t.Error("the ledger was read on a dead context; the refusal must not depend on the reader noticing")
			}
		})
	}
}

func TestDoRefusesAnUnusableTenant(t *testing.T) {
	// I11: without a tenant there is no ledger to read, so this is a refusal and not a
	// pass. The reason comes from the keys helper so this package cannot disagree with
	// it about what a usable tenant is.
	for _, tenant := range []keys.TenantID{"", " ", "\t", "t1#TENANT"} {
		b, ledger := newTestBreaker(t, 1_000_000, 0)
		err := b.Do(context.Background(), tenant, 1, mustNotRun(t))
		if !Refused(err) {
			t.Errorf("tenant %q was accepted: %v", string(tenant), err)
			continue
		}
		if !errors.Is(err, ErrSpendUnknown) {
			t.Errorf("tenant %q refused with %v, want ErrSpendUnknown", string(tenant), err)
		}
		if ledger.readCount() != 0 {
			t.Errorf("tenant %q reached the ledger", string(tenant))
		}
	}
}

func TestDoRefusesWithoutAPositiveEstimate(t *testing.T) {
	// With no estimate the breaker could only refuse *after* the cap was breached, so
	// every day would end at the cap plus one call — the cap becomes a floor.
	for _, estimate := range []int64{0, -1, math.MinInt64} {
		b, ledger := newTestBreaker(t, 1_000_000, 0)
		err := b.Do(context.Background(), testTenant, estimate, mustNotRun(t))
		if !errors.Is(err, ErrNoEstimate) {
			t.Errorf("estimate %d refused with %v, want ErrNoEstimate", estimate, err)
		}
		if !Refused(err) {
			t.Errorf("estimate %d: Refused did not recognise the error", estimate)
		}
		if ledger.readCount() != 0 {
			t.Errorf("estimate %d reached the ledger", estimate)
		}
	}
}

func TestDoRefusesAnEstimateThatWouldOverflowTheProjection(t *testing.T) {
	// spent + pending + unmetered + estimate would wrap to a negative number and compare
	// as being under the cap. A fail-open path reachable from an integer overflow is not
	// theoretical in the one function whose job is to refuse.
	b, _ := newTestBreaker(t, 1_000, 1)
	err := b.Do(context.Background(), testTenant, math.MaxInt64, mustNotRun(t))
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("an absurd estimate was not refused: %v", err)
	}
}

func TestDoRefusesANegativeLedgerTotal(t *testing.T) {
	// The meter refuses a negative cost, so a negative total means corrupted records or
	// arithmetic that has wrapped. An untrustworthy ledger reads as unknown, not as
	// headroom.
	b, _ := newTestBreaker(t, 1_000, -5_000)
	err := b.Do(context.Background(), testTenant, 1, mustNotRun(t))
	if !errors.Is(err, ErrSpendUnknown) {
		t.Fatalf("a negative day total was treated as headroom: %v", err)
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("refusal did not name the cause: %v", err)
	}
}

func TestDoRefusesWithNoCallToGuard(t *testing.T) {
	// Refused(err) is the caller's "did the provider call happen" test, and with a nil
	// closure it plainly did not. A plain fmt.Errorf here landed a programming defect in
	// the caller's *other* branch — the one documented as "transient provider fault,
	// retry" — where it retries for ever against a call that can never be made.
	b, _ := newTestBreaker(t, 1_000, 0)
	err := b.Do(context.Background(), testTenant, 1, nil)
	if err == nil {
		t.Fatal("Do accepted a nil call")
	}
	if !Refused(err) {
		t.Errorf("Refused(%v) is false; every error returned in place of running the call must be a *Refusal", err)
	}
	if !errors.Is(err, ErrNoCall) {
		t.Errorf("error %v is not ErrNoCall", err)
	}
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a *Refusal", err)
	}
	if r.Day != "2026-08-04" {
		t.Errorf("refusal day is %q, want the clock's day; a refusal logged with an empty day cannot be correlated with the ledger", r.Day)
	}
	if r.RetryableToday() {
		t.Error("a nil closure was reported as retryable today; the identical call reproduces it exactly")
	}
}

func TestRefusedIsTrueExactlyWhenTheCallDidNotHappen(t *testing.T) {
	// The package's headline contract, asserted as the biconditional it claims to be. A
	// caller branches on Refused to decide between "give up" and "retry the provider", so
	// a breaker error on the wrong side of it is either an infinite retry loop or a lost
	// capture.
	providerErr := errors.New("stt: 504 gateway timeout")

	cases := map[string]struct {
		setup       func(t *testing.T) (*Breaker, *fakeLedger)
		estimate    int64
		fn          func(ran *bool) func(context.Context) (Cost, error)
		wantRefused bool
	}{
		"cap exceeded": {
			setup:    func(t *testing.T) (*Breaker, *fakeLedger) { return newTestBreaker(t, 1_000, 1_000) },
			estimate: 1,
		},
		"ledger unreadable": {
			setup: func(t *testing.T) (*Breaker, *fakeLedger) {
				b, l := newTestBreaker(t, 1_000, 0)
				l.readErr = errors.New("dynamodb: throttled")
				return b, l
			},
			estimate: 1,
		},
		"no estimate": {
			setup:    func(t *testing.T) (*Breaker, *fakeLedger) { return newTestBreaker(t, 1_000, 0) },
			estimate: 0,
		},
		"provider failed": {
			setup:    func(t *testing.T) (*Breaker, *fakeLedger) { return newTestBreaker(t, 1_000, 0) },
			estimate: 1,
			fn: func(ran *bool) func(context.Context) (Cost, error) {
				return func(context.Context) (Cost, error) { *ran = true; return Cost{}, providerErr }
			},
		},
		"cost could not be recorded": {
			setup: func(t *testing.T) (*Breaker, *fakeLedger) {
				b, l := newTestBreaker(t, 1_000, 0)
				l.recordErr = errors.New("dynamodb: throughput exceeded")
				return b, l
			},
			estimate: 1,
			fn: func(ran *bool) func(context.Context) (Cost, error) {
				return func(context.Context) (Cost, error) { *ran = true; return costOf(1), nil }
			},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, _ := c.setup(t)
			ran := false
			fn := func(context.Context) (Cost, error) { ran = true; return costOf(1), nil }
			if c.fn != nil {
				fn = c.fn(&ran)
			}
			err := b.Do(context.Background(), testTenant, c.estimate, fn)
			if err == nil {
				t.Fatal("expected an error")
			}
			if Refused(err) != !ran {
				t.Errorf("Refused(err)=%v but the call ran=%v; the two must be exact opposites (err: %v)",
					Refused(err), ran, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Distinguishing a refusal from a provider failure
// ---------------------------------------------------------------------------

func TestProviderErrorsPassThroughUntouched(t *testing.T) {
	// Retrying a provider timeout is correct; retrying a breaker refusal is pointless
	// until tomorrow. The caller can only make that decision if the two are
	// distinguishable, which means the provider's error must arrive unwrapped and must
	// not satisfy Refused.
	b, _ := newTestBreaker(t, 1_000_000, 0)
	providerErr := errors.New("stt: 504 gateway timeout")

	err := b.Do(context.Background(), testTenant, 100, func(context.Context) (Cost, error) {
		return Cost{}, providerErr
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("provider error was not returned: %v", err)
	}
	if Refused(err) {
		t.Error("a provider failure was reported as a breaker refusal; a caller would stop retrying until tomorrow")
	}
	if errors.Is(err, ErrCapExceeded) || errors.Is(err, ErrSpendUnknown) {
		t.Error("a provider failure matched a breaker sentinel")
	}
	if errors.Is(err, ErrUnmetered) {
		t.Error("a call that reached no provider reported its cost as unmetered; there was nothing to bill")
	}
}

func TestRefusalCarriesTheFiguresForTheUserAlert(t *testing.T) {
	// §10.5.9 requires the user be alerted. This package produces no copy (§7.3 —
	// user-visible strings come from branding), so it has to hand the surface the
	// numbers.
	b, _ := newTestBreaker(t, 1_000, 950)
	err := b.Do(context.Background(), testTenant, 100, mustNotRun(t))

	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error is not a *Refusal: %v", err)
	}
	if r.Tenant != testTenant {
		t.Errorf("refusal tenant is %q, want %q", r.Tenant, testTenant)
	}
	if r.Day != "2026-08-04" {
		t.Errorf("refusal day is %q, want the clock's day", r.Day)
	}
	if r.CapMicros != 1_000 || r.SpentMicros != 950 || r.EstimateMicros != 100 {
		t.Errorf("refusal figures are cap=%d spent=%d estimate=%d, want 1000/950/100",
			r.CapMicros, r.SpentMicros, r.EstimateMicros)
	}
	if r.AvailableMicros != 50 {
		t.Errorf("available is %d, want 50", r.AvailableMicros)
	}
	// The message has to be reconcilable by a human, which means exact decimal amounts
	// rather than raw micros or a float.
	for _, want := range []string{"0.000950", "0.001000", "2026-08-04", string(testTenant)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not contain %q", err, want)
		}
	}
}

// ---------------------------------------------------------------------------
// The operational record — §10.1 has no alarms, so this WARN line is the signal
// ---------------------------------------------------------------------------

// logLines runs fn and returns the log lines it produced, parsed.
func logLines(t *testing.T, buf *bytes.Buffer, fn func()) []map[string]any {
	t.Helper()
	buf.Reset()
	fn()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func newLoggingBreaker(t *testing.T, capMicros int64) (*Breaker, *fakeLedger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ledger := newFakeLedger(0)
	b, err := New(ledger, clock.Fixed{T: fixedNow}, log, capMicros)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, ledger, buf
}

func TestTheRefusalLogLineCarriesTheCause(t *testing.T) {
	// Under a DynamoDB throttle every capture is refused and every line shows
	// spent_micros=0 against a non-zero cap. Without the cause an operator cannot tell a
	// throttle from an IAM denial, a missing table, an expired context or a corrupt
	// record — and "refused, spent 0" reads as the breaker itself being broken, which is
	// the misreading that gets it switched off. There are no alarms or SNS topics in this
	// deployment (§10.1), so this line is the whole operational signal.
	b, ledger, buf := newLoggingBreaker(t, 1_000_000)
	ledger.readErr = errors.New("dynamodb: ProvisionedThroughputExceededException")

	lines := logLines(t, buf, func() {
		_ = b.Do(context.Background(), testTenant, 1, mustNotRun(t))
	})
	if len(lines) != 1 {
		t.Fatalf("emitted %d log lines, want exactly 1: %v", len(lines), lines)
	}
	line := lines[0]

	cause, ok := line["cause"].(string)
	if !ok {
		t.Fatalf("the refusal line has no cause attribute: %v", line)
	}
	if !strings.Contains(cause, "ProvisionedThroughputExceededException") {
		t.Errorf("cause is %q; the underlying storage fault never reached the log", cause)
	}
	if line["retryable_today"] != true {
		t.Errorf("retryable_today is %v; an unreadable ledger may recover within the day", line["retryable_today"])
	}
	if line["day"] != "2026-08-04" {
		t.Errorf("day is %v, want the clock's day", line["day"])
	}
	if line["level"] != "WARN" {
		t.Errorf("level is %v, want WARN — a breaker doing its job is not a fault", line["level"])
	}
}

func TestTheRefusalLogLineReportsRetryabilityByReason(t *testing.T) {
	// "not ErrCapExceeded" answered true for a missing estimate and for an unusable
	// tenant, both of which the identical call reproduces exactly — an operator or a
	// retry policy reading that field would loop for ever.
	cases := map[string]struct {
		estimate int64
		tenant   keys.TenantID
		spent    int64
		readErr  error
		want     bool
	}{
		"no estimate":       {0, testTenant, 0, nil, false},
		"unusable tenant":   {1, "", 0, nil, false},
		"cap exceeded":      {1, testTenant, 1_000, nil, false},
		"unreadable ledger": {1, testTenant, 0, errors.New("dynamodb: throttled"), true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, ledger, buf := newLoggingBreaker(t, 1_000)
			ledger.spent[ledgerKey{testTenant, clock.Date(fixedNow)}] = c.spent
			ledger.readErr = c.readErr

			lines := logLines(t, buf, func() {
				_ = b.Do(context.Background(), c.tenant, c.estimate, mustNotRun(t))
			})
			if len(lines) != 1 {
				t.Fatalf("emitted %d log lines, want 1: %v", len(lines), lines)
			}
			if got := lines[0]["retryable_today"]; got != c.want {
				t.Errorf("retryable_today is %v, want %v", got, c.want)
			}
			// Every refusal is correlatable with the ledger an operator would go and read,
			// including the ones decided before the ledger is consulted.
			if lines[0]["day"] != "2026-08-04" {
				t.Errorf("day is %v, want the clock's day", lines[0]["day"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Settlement — the window between "call finished" and "cost is in the ledger"
// ---------------------------------------------------------------------------

func TestACompletedCallsCostIsInTheLedgerBeforeItsReservationIsReleased(t *testing.T) {
	// The defect this closes, reproducible with no concurrency at all. When the metering
	// write was the caller's job it happened *after* Do returned, so a completed call's
	// cost sat in neither `pending` nor `spent`: against a 1,000-micro cap, Do(600)
	// returned, Check reported available=1000, and Do(600) was admitted again — 1,200
	// micros of admitted spend against a 1,000 cap, single-threaded.
	b, ledger := newTestBreaker(t, 1_000, 0)

	if err := b.Do(context.Background(), testTenant, 600, succeeds(600)); err != nil {
		t.Fatalf("the first call was refused: %v", err)
	}

	if got := ledger.totalFor(testTenant, clock.Date(fixedNow)); got != 600 {
		t.Fatalf("the ledger holds %d micros after the call returned, want 600", got)
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.SpentMicros != 600 || d.AvailableMicros != 400 {
		t.Errorf("after one 600-micro call: spent=%d available=%d, want 600 and 400", d.SpentMicros, d.AvailableMicros)
	}

	if err := b.Do(context.Background(), testTenant, 600, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("a second 600-micro call was admitted against a 1,000 cap: %v", err)
	}
}

func TestTheCostIsRecordedBeforeTheReservationDrops(t *testing.T) {
	// The ordering, asserted from inside the write. Releasing first and recording second
	// would reopen the window for exactly the duration of the write — ~5-10ms of
	// DynamoDB PutOnce, which is all an SQS batch needs.
	b, ledger := newTestBreaker(t, 1_000, 0)

	var pendingDuringWrite int64
	var checkErr error
	ledger.onRecord = func() {
		// Check takes no admission gate, so this cannot deadlock against the call being
		// settled — which is itself one of the reasons Check does not take one.
		d, err := b.Check(context.Background(), testTenant, 1)
		pendingDuringWrite, checkErr = d.PendingMicros, err
	}

	if err := b.Do(context.Background(), testTenant, 600, succeeds(600)); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if checkErr != nil {
		t.Fatalf("Check during the write: %v", checkErr)
	}
	if pendingDuringWrite != 600 {
		t.Errorf("in-flight micros during the usage write are %d, want 600 — the reservation must outlive the write", pendingDuringWrite)
	}
}

func TestAFailedButBilledCallIsStillMetered(t *testing.T) {
	// A client-side timeout does not cancel the work at the other end, so a failed call
	// may still appear on the invoice. Reported cost is metered whatever the call's
	// outcome, or the spend escapes the cap entirely.
	b, ledger := newTestBreaker(t, 1_000, 0)
	providerErr := errors.New("stt: connection reset after the request was accepted")

	err := b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
		return costOf(600), providerErr
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error is %v, want the provider's", err)
	}
	if Refused(err) {
		t.Error("a failed provider call was reported as a refusal")
	}
	if got := ledger.totalFor(testTenant, clock.Date(fixedNow)); got != 600 {
		t.Errorf("the ledger holds %d micros, want 600 — a billed call that failed must still be metered (I12)", got)
	}
	recs := ledger.records()
	if len(recs) != 1 {
		t.Fatalf("wrote %d usage records, want 1", len(recs))
	}
	if recs[0].Tenant != testTenant {
		t.Errorf("usage record tenant is %q, want %q (I11)", recs[0].Tenant, testTenant)
	}
	if recs[0].Op != "transcribe" || recs[0].Provider != "provider-a" || recs[0].Unit != model.UnitSTTSeconds {
		t.Errorf("usage record lost the cost basis: %+v", recs[0])
	}
}

func TestACallThatReachedNoProviderMetersNothing(t *testing.T) {
	// Cost{} means nothing was billed. Metering a zero-cost event here would put a
	// record in the ledger for a call that never happened, and reserving against it would
	// shrink the day's budget for no spend.
	b, ledger := newTestBreaker(t, 1_000, 0)
	providerErr := errors.New("stt: dial tcp: connection refused")

	err := b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
		return Cost{}, providerErr
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error is %v, want the provider's", err)
	}
	if len(ledger.records()) != 0 {
		t.Errorf("wrote %d usage records for a call that reached no provider", len(ledger.records()))
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.PendingMicros != 0 || d.UnmeteredMicros != 0 || d.AvailableMicros != 1_000 {
		t.Errorf("after a call that was never billed: pending=%d unmetered=%d available=%d, want 0/0/1000",
			d.PendingMicros, d.UnmeteredMicros, d.AvailableMicros)
	}
}

func TestASuccessfulCallThatReportsNoCostIsUnmetered(t *testing.T) {
	// A guarded call that succeeded certainly cost something. Accepting Cost{} silently
	// would mean the cap never sees that spend, which is the same hole as not metering at
	// all — so it is an error, and the estimate is charged to the day.
	b, ledger := newTestBreaker(t, 1_000, 0)

	err := b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
		return Cost{}, nil
	})
	if !errors.Is(err, ErrUnmetered) {
		t.Fatalf("error is %v, want ErrUnmetered", err)
	}
	if Refused(err) {
		t.Error("Refused is true for a call that ran; the biconditional in the package comment breaks")
	}
	if len(ledger.records()) != 0 {
		t.Errorf("wrote %d usage records with no cost to write", len(ledger.records()))
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.UnmeteredMicros != 600 || d.AvailableMicros != 400 {
		t.Errorf("unmetered=%d available=%d, want 600 and 400 — spend the ledger will never show must still consume budget",
			d.UnmeteredMicros, d.AvailableMicros)
	}
}

func TestAnEstimateStaysChargedWhenTheUsageWriteFails(t *testing.T) {
	// The call happened and the ledger will never show it. Releasing the reservation here
	// would hand that spend back as headroom — the same fail-open the reservation exists
	// to prevent, just on the settlement side.
	b, ledger := newTestBreaker(t, 1_000, 0)
	boom := errors.New("dynamodb: throughput exceeded")
	ledger.recordErr = boom

	err := b.Do(context.Background(), testTenant, 600, succeeds(600))
	if !errors.Is(err, ErrUnmetered) {
		t.Fatalf("error is %v, want ErrUnmetered", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the storage failure; an operator needs the cause", err)
	}
	if Refused(err) {
		t.Error("Refused is true for a call that ran")
	}

	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.SpentMicros != 0 {
		t.Errorf("the ledger reports %d micros; the write failed, so it must report none", d.SpentMicros)
	}
	if d.UnmeteredMicros != 600 || d.AvailableMicros != 400 {
		t.Errorf("unmetered=%d available=%d, want 600 and 400", d.UnmeteredMicros, d.AvailableMicros)
	}

	// And the retained amount is enforced, not merely reported.
	ledger.recordErr = nil
	if err := b.Do(context.Background(), testTenant, 600, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("a second 600-micro call was admitted after 600 micros of unmetered spend: %v", err)
	}
}

func TestAPanickingCallDoesNotHandItsBudgetBack(t *testing.T) {
	// A panicking adapter is a call whose cost nobody knows and nobody metered. Releasing
	// the reservation on the way past would give that spend back as headroom, which is the
	// fail-open direction; charging it to the day is the other one.
	b, _ := newTestBreaker(t, 1_000, 0)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate; a caller must still see its own defect")
			}
		}()
		_ = b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
			panic("stt adapter: nil pointer dereference")
		})
	}()

	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.PendingMicros != 0 {
		t.Errorf("%d micros are still in flight after the call unwound", d.PendingMicros)
	}
	if d.UnmeteredMicros != 600 || d.AvailableMicros != 400 {
		t.Errorf("unmetered=%d available=%d, want 600 and 400", d.UnmeteredMicros, d.AvailableMicros)
	}
}

func TestUnmeteredSpendIsChargedToItsOwnDayOnly(t *testing.T) {
	// The cap is a UTC day (overshoot case 4). An unmetered cost is retained against the
	// day it happened on; carrying it forward would shrink every future day's budget for
	// the life of the container, which is a different failure and not a safer one.
	clk := &movingClock{t: fixedNow}
	ledger := newFakeLedger(0)
	ledger.recordErr = errors.New("dynamodb: throughput exceeded")
	b, err := New(ledger, clk, discardLogger(), 1_000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := b.Do(context.Background(), testTenant, 600, succeeds(600)); !errors.Is(err, ErrUnmetered) {
		t.Fatalf("error is %v, want ErrUnmetered", err)
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.UnmeteredMicros != 600 {
		t.Fatalf("unmetered is %d on the day it happened, want 600", d.UnmeteredMicros)
	}

	clk.advance(24 * time.Hour)
	d, err = b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check on the next day: %v", err)
	}
	if d.Day != "2026-08-05" {
		t.Fatalf("the window is %q, want the advanced clock's day", d.Day)
	}
	if d.UnmeteredMicros != 0 || d.AvailableMicros != 1_000 {
		t.Errorf("the next day starts with unmetered=%d available=%d, want 0 and the full cap",
			d.UnmeteredMicros, d.AvailableMicros)
	}
}

// ---------------------------------------------------------------------------
// Concurrency within one process
// ---------------------------------------------------------------------------

func TestConcurrentCallsInOneProcessCannotJointlyExceedTheCap(t *testing.T) {
	// §0.7.5: "the daily breaker is per-tenant, not per-agent." A metering record is
	// written only after the call returns, so without an in-flight reservation two
	// concurrent calls both read the same spend and both pass. This is the part of that
	// gap this package can close; the cross-process part is documented, not fixed.
	b, ledger := newTestBreaker(t, 1_000, 0)

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
			close(started)
			<-release
			return costOf(600), nil
		})
	}()
	<-started

	// The ledger still reads zero — the first call has not finished, so nothing has been
	// metered. Only the reservation stands between these two and 1,200 micros of spend
	// against a 1,000 cap.
	err := b.Do(context.Background(), testTenant, 600, mustNotRun(t))
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("a concurrent call was admitted alongside an in-flight one: %v", err)
	}
	var r *Refusal
	if errors.As(err, &r) && r.PendingMicros != 600 {
		t.Errorf("refusal reports %d micros in flight, want 600", r.PendingMicros)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("the first call failed: %v", err)
	}

	// The reservation is gone but the cost has taken its place in the ledger, so the
	// remaining budget is 400 and not the whole cap. This is the pair of assertions that
	// distinguishes "the reservation was released" from "the spend was forgotten".
	if got := ledger.totalFor(testTenant, clock.Date(fixedNow)); got != 600 {
		t.Errorf("the ledger holds %d micros, want 600", got)
	}
	if err := b.Do(context.Background(), testTenant, 401, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Errorf("a 401-micro call was admitted against 400 micros of remaining budget: %v", err)
	}
	ran := false
	if err := b.Do(context.Background(), testTenant, 400, func(context.Context) (Cost, error) {
		ran = true
		return costOf(400), nil
	}); err != nil {
		t.Fatalf("a call inside the remaining budget was refused: %v", err)
	}
	if !ran {
		t.Error("the call did not run")
	}
}

func TestTheReservationIsReleasedWhenTheCallFails(t *testing.T) {
	// A failed provider call that was not billed must give its reservation back, or a
	// provider outage shrinks the day's budget by the estimate of every attempt.
	b, _ := newTestBreaker(t, 1_000, 0)

	if err := b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
		return Cost{}, errors.New("stt: connection reset")
	}); err == nil {
		t.Fatal("expected the provider error")
	}

	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.PendingMicros != 0 {
		t.Errorf("%d micros are still reserved after a failed call", d.PendingMicros)
	}
}

func TestReservationsDoNotLeakAcrossTenants(t *testing.T) {
	// I11 applies to in-memory state as much as to stored records: one tenant's in-flight
	// call must not consume another's budget.
	b, _ := newTestBreaker(t, 1_000, 0)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.Do(context.Background(), testTenant, 900, func(context.Context) (Cost, error) {
			close(started)
			<-release
			return costOf(900), nil
		})
	}()
	<-started

	ran := false
	if err := b.Do(context.Background(), "t_other", 900, func(context.Context) (Cost, error) {
		ran = true
		return costOf(900), nil
	}); err != nil {
		t.Errorf("the other tenant was refused on this tenant's in-flight call: %v", err)
	}
	if !ran {
		t.Error("the other tenant's call did not run")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first call: %v", err)
	}
}

func TestAdmittedSpendNeverExceedsTheCapUnderConcurrency(t *testing.T) {
	// The race detector's target, and the end-to-end form of the settlement property:
	// twenty callers each estimating and costing a fifth of the cap. Every admitted call
	// meters its cost before releasing its reservation, so the *metered total* — not just
	// the count of concurrent reservations — has to land inside the cap.
	const capMicros = 1_000
	const each = capMicros / 5
	b, ledger := newTestBreaker(t, capMicros, 0)

	var mu sync.Mutex
	var inFlight, maxInFlight, admitted, refused int

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.Do(context.Background(), testTenant, each, func(context.Context) (Cost, error) {
				mu.Lock()
				inFlight++
				if inFlight > maxInFlight {
					maxInFlight = inFlight
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inFlight--
				mu.Unlock()
				return costOf(each), nil
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				admitted++
			} else if errors.Is(err, ErrCapExceeded) {
				refused++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if maxInFlight > 5 {
		t.Errorf("%d calls were in flight at once against a cap of five reservations", maxInFlight)
	}
	if got := ledger.totalFor(testTenant, clock.Date(fixedNow)); got > capMicros {
		t.Errorf("the day metered %d micros against a cap of %d; concurrent calls jointly exceeded it", got, capMicros)
	}
	if admitted > 5 {
		t.Errorf("%d calls were admitted; %d micros each against a %d cap allows at most 5", admitted, each, capMicros)
	}
	if refused == 0 {
		t.Error("no call was refused; twenty calls at a fifth of the cap each cannot all fit")
	}
	// The cap may now be exactly reached, so a refusal here is expected; the Decision is
	// returned either way and it is the reservations that are under test.
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil && !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("Check: %v", err)
	}
	if d.PendingMicros != 0 {
		t.Errorf("%d micros remain reserved after every call returned", d.PendingMicros)
	}
	if d.UnmeteredMicros != 0 {
		t.Errorf("%d micros were retained as unmetered although every write succeeded", d.UnmeteredMicros)
	}
}

// ---------------------------------------------------------------------------
// A slow ledger must refuse, not block
// ---------------------------------------------------------------------------

func TestWaitingForAdmissionHonoursTheContext(t *testing.T) {
	// The mutex the admission gate replaced was not context-aware, so a caller waiting
	// behind a 20-second DynamoDB retry chain ignored its own deadline: no *Refusal, no
	// WARN line, and a Lambda timeout that presents as an unexplained worker failure
	// rather than as "spend could not be determined". Blocking is not refusing.
	b, ledger, buf := newLoggingBreaker(t, 1_000_000)
	hold := make(chan struct{})
	ledger.hold[testTenant] = hold

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- b.Do(context.Background(), testTenant, 1, succeeds(1))
	}()
	// The first call is inside DayTotal, holding the gate, once the read is recorded.
	for ledger.readCount() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lines := logLines(t, buf, func() {
		err := b.Do(ctx, testTenant, 1, mustNotRun(t))
		if !Refused(err) {
			t.Errorf("waiting for admission on a dead context returned %v, want a *Refusal", err)
		}
		if !errors.Is(err, ErrSpendUnknown) {
			t.Errorf("error %v is not ErrSpendUnknown", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v does not carry the context's own failure", err)
		}
		if !strings.Contains(err.Error(), "waiting for admission") {
			t.Errorf("error %v does not say the wait was what failed; an operator cannot tell it from a failed read", err)
		}
	})
	if len(lines) != 1 {
		t.Fatalf("emitted %d log lines, want exactly 1 — a blocked admission with no log line is the failure this replaces", len(lines))
	}
	if lines[0]["retryable_today"] != true {
		t.Errorf("retryable_today is %v; a contended gate may clear within the day", lines[0]["retryable_today"])
	}

	close(hold)
	if err := <-firstDone; err != nil {
		t.Fatalf("the first call failed: %v", err)
	}
}

func TestOneTenantsSlowLedgerDoesNotStallAnother(t *testing.T) {
	// The gate is per tenant, not process-global. With one lock for the process, a single
	// tenant's slow read stalls every other tenant's admission — and in a Lambda serving
	// an SQS batch that is the whole batch.
	b, ledger := newTestBreaker(t, 1_000_000, 0)
	hold := make(chan struct{})
	ledger.hold[testTenant] = hold

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- b.Do(context.Background(), testTenant, 1, succeeds(1))
	}()
	for ledger.readCount() == 0 {
		time.Sleep(time.Millisecond)
	}

	otherDone := make(chan error, 1)
	go func() {
		otherDone <- b.Do(context.Background(), "t_other", 1, succeeds(1))
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Errorf("the other tenant's call was refused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the other tenant's admission was still blocked behind this tenant's ledger read")
	}

	close(hold)
	if err := <-firstDone; err != nil {
		t.Fatalf("the first call failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Check is a forecast, not a permission
// ---------------------------------------------------------------------------

func TestCheckReservesNothing(t *testing.T) {
	// Documented behaviour, and the reason Do exists: Check is for the pre-flight
	// refusal at the request boundary, where the provider call happens later in a
	// worker. If it reserved, the reservation would never be released.
	b, _ := newTestBreaker(t, 1_000, 0)

	for i := 0; i < 3; i++ {
		d, err := b.Check(context.Background(), testTenant, 900)
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if d.PendingMicros != 0 {
			t.Errorf("Check %d reserved %d micros", i, d.PendingMicros)
		}
	}
}

func TestCheckReportsTheBudget(t *testing.T) {
	b, _ := newTestBreaker(t, 1_000, 250)
	d, err := b.Check(context.Background(), testTenant, 100)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := Decision{
		Day:             "2026-08-04",
		CapMicros:       1_000,
		SpentMicros:     250,
		PendingMicros:   0,
		UnmeteredMicros: 0,
		EstimateMicros:  100,
		AvailableMicros: 750,
	}
	if d != want {
		t.Errorf("decision is %+v, want %+v", d, want)
	}
}

func TestCheckFailsClosedToo(t *testing.T) {
	// The pre-flight path must not be the lenient one. A handler that admitted a request
	// because the ledger was unreadable would hand the worker a call the worker then
	// refuses — the failure surfaces as a stuck capture rather than as a refusal the
	// user was told about.
	b, ledger := newTestBreaker(t, 1_000_000, 0)
	ledger.readErr = errors.New("dynamodb: throttled")
	if _, err := b.Check(context.Background(), testTenant, 1); !errors.Is(err, ErrSpendUnknown) {
		t.Fatalf("Check permitted a call on an unreadable ledger: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Against the real meter
// ---------------------------------------------------------------------------

type discardIDs struct {
	mu sync.Mutex
	n  int
}

func (d *discardIDs) NewID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	return fmt.Sprintf("01BRK%05d", d.n)
}

func TestRefusesAgainstSpendComputedFromRealUsageRecords(t *testing.T) {
	// §10.5.9 says the spend is computed "from the Usage records (I12)", so the wiring
	// that matters is meter.DayTotal over a real repository — not just the fake ledger
	// the rest of these tests use.
	repo := repository.NewMemory()
	clk := clock.Fixed{T: fixedNow}
	m := meter.New(repo, clk, &discardIDs{}, discardLogger(), 25)

	// Four records on the clock's day, and one on the next — the next day's spend must
	// not count against today's cap.
	for i := 0; i < 4; i++ {
		if err := m.Record(context.Background(), meter.Event{
			Tenant:     testTenant,
			Unit:       model.UnitSTTSeconds,
			Quantity:   28,
			Provider:   "provider-a",
			CostMicros: 200_000,
			Op:         "transcribe",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	tomorrow := meter.New(repo, clk.Advance(24*time.Hour), &discardIDs{}, discardLogger(), 25)
	if err := tomorrow.Record(context.Background(), meter.Event{
		Tenant:     testTenant,
		Unit:       model.UnitLLMInputTokens,
		Quantity:   5000,
		Provider:   "provider-b",
		CostMicros: 900_000,
		Op:         "cleanup",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// A low test cap, as §Phase 0 acceptance requires: 0.85 USD against 0.80 already
	// spent leaves 0.05, so a 0.10 call is refused and a 0.05 one is not.
	capMicros, err := MicrosFromUSD(0.85)
	if err != nil {
		t.Fatalf("MicrosFromUSD: %v", err)
	}
	b, err := New(m, clk, discardLogger(), capMicros)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := b.Do(context.Background(), testTenant, 100_000, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("a call over the remaining budget was not refused: %v", err)
	}

	ran := false
	if err := b.Do(context.Background(), testTenant, 50_000, func(context.Context) (Cost, error) {
		ran = true
		return costOf(50_000), nil
	}); err != nil {
		t.Fatalf("a call inside the remaining budget was refused: %v", err)
	}
	if !ran {
		t.Error("the permitted call did not run")
	}

	// The admitted call's cost went through the real meter into the real repository, so
	// the cap is now reached and the next call — however small — is refused. This is the
	// end-to-end form of the settlement property: nothing in the caller wrote that record.
	if err := b.Do(context.Background(), testTenant, 1, mustNotRun(t)); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("the cap was not reached after the admitted call was metered: %v", err)
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("Check reports headroom at the cap: %v", err)
	}
	if d.SpentMicros != 850_000 {
		t.Errorf("today's spend reads as %d micros, want 850000 — 0.80 of records plus the 0.05 call Do metered", d.SpentMicros)
	}
}

func TestRefusesWhenTheRealLedgerIsUnreadable(t *testing.T) {
	// End-to-end fail-closed: the repository fails, the meter surfaces it, the breaker
	// refuses. Every layer has to hold for the property to hold.
	repo := repository.NewMemory()
	clk := clock.Fixed{T: fixedNow}
	m := meter.New(repo, clk, ids.NewGenerator(clk), discardLogger(), 25)
	b, err := New(m, clk, discardLogger(), 1_000_000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	repo.FailNext(errors.New("dynamodb: internal server error"))
	if err := b.Do(context.Background(), testTenant, 1, mustNotRun(t)); !errors.Is(err, ErrSpendUnknown) {
		t.Fatalf("the call was permitted on an unreadable ledger: %v", err)
	}
}

func TestARealUsageWriteFailureLeavesTheEstimateCharged(t *testing.T) {
	// The settlement failure against the real meter rather than the fake: the read
	// succeeds, the write does not, and the estimate has to stay charged to the day or the
	// spend that just happened becomes headroom.
	repo := repository.NewMemory()
	clk := clock.Fixed{T: fixedNow}
	m := meter.New(repo, clk, ids.NewGenerator(clk), discardLogger(), 25)
	b, err := New(m, clk, discardLogger(), 1_000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The ledger read comes first and must succeed, so the failure is armed to land on
	// the usage write.
	if _, err := b.Check(context.Background(), testTenant, 1); err != nil {
		t.Fatalf("Check: %v", err)
	}
	boom := errors.New("dynamodb: conditional check failed")
	ran := false
	err = b.Do(context.Background(), testTenant, 600, func(context.Context) (Cost, error) {
		ran = true
		// Armed from inside the closure: the failure has to land on the usage write that
		// settlement performs, not on the ledger read that admitted the call.
		repo.FailNext(boom)
		return costOf(600), nil
	})
	if !ran {
		t.Fatal("the call did not run; the failure was armed for the usage write, not the read")
	}
	if !errors.Is(err, ErrUnmetered) || !errors.Is(err, boom) {
		t.Fatalf("error is %v, want ErrUnmetered wrapping the storage failure", err)
	}
	d, err := b.Check(context.Background(), testTenant, 1)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.SpentMicros != 0 {
		t.Errorf("the ledger holds %d micros; the write failed", d.SpentMicros)
	}
	if d.UnmeteredMicros != 600 {
		t.Errorf("unmetered is %d, want 600", d.UnmeteredMicros)
	}
}
