// Package breaker is the per-tenant daily spend circuit breaker (§10.5.9).
//
// §10.5.9: "Compute spend from the Usage records (I12) before each provider call;
// refuse and alert the user above the configured daily cap. **This is what converts an
// unbounded worst case into a known number** — passbook bounds abuse with concurrency
// and throttle limits, but neither of those caps third-party API spend. Fail closed."
//
// The reason this exists rather than relying on the infrastructure controls: Lambda
// reserved concurrency and the API Gateway throttle bound how many requests reach the
// system, and nothing else bounds what each one spends at a third party. A runaway loop
// that stays inside the throttle can still bill an unbounded amount. §10.7 puts the
// target at ~$1/month, so the cap is small in absolute terms and a single stuck loop
// clears it in minutes.
//
// # Fail closed, structurally
//
// **Refused(err) is true if and only if the provider call did not happen.** That
// biconditional is the whole contract a caller needs: a refusal always means "the call
// was not made and must not be made", and any error that is not a refusal means the call
// did happen (or was attempted) and its outcome is the caller's to interpret. There is
// deliberately no error class that means "something went wrong, carry on": an open
// breaker on an unreadable ledger is an uncapped bill, which is worse than a refused
// capture the user can retry (the audio is buffered locally until upload is confirmed,
// I2, so a refusal loses nothing).
//
// That is why Do, not Check, is the sanctioned path. Do owns the closure, so the
// provider call cannot be reached unless the check passed — a caller cannot write
// `if err != nil { log.Warn(...) }` and then call the provider anyway, which is the
// shape a fail-closed check degrades into under deadline pressure.
//
// # Why Do writes the metering record
//
// The in-flight reservation exists to close the window between "this process admitted a
// call" and "that call's cost is visible to DayTotal". Releasing the reservation when fn
// returns does not close that window, it only moves it: a call's cost enters the ledger
// when its Usage record is written, and if that write is the caller's job it happens
// *after* Do has returned — so between fn returning and the caller metering, the
// completed call's cost is in neither `pending` nor `spent`. That is not a concurrency
// subtlety; it is reproducible single-threaded. Against a 1,000-micro cap on an empty
// ledger, Do(600) is admitted and returns, Check then reports available=1000, and
// Do(600) is admitted again — 1,200 micros against a 1,000 cap in one goroutine.
//
// So Do performs the metering write itself, from the Cost the closure reports, and
// releases the reservation only once that write has returned. Repository.QueryPrefix is a
// strongly consistent read of the base table, so the next admission's DayTotal sees the
// record the moment the reservation drops. The in-process window is therefore closed by
// construction rather than by an instruction to the caller — which is the argument this
// whole package rests on, since an ordering requirement that lives in a doc comment is
// exactly the guardrail that fails when it matters.
//
// It also makes I12 structural on the provider path: a guarded call cannot complete
// without emitting a metering event. The residual instruction — an adapter inside fn must
// report its cost rather than calling Meter.Record itself, or the day is counted twice —
// errs toward refusing early rather than toward an uncapped bill, and is recorded in
// meter's doc comment as well as here.
//
// # The estimate problem, and the overshoot this cannot prevent
//
// A call's cost is not known until it completes, so the check has to compare the cap
// against a *projection*: today's metered spend, plus what this process already has in
// flight, plus the caller's estimate of this call. The estimate is mandatory and must be
// positive. With no estimate the breaker could only refuse after the cap had already
// been breached, which makes the cap a floor — every day ends at cap plus one call —
// rather than a ceiling. Config carries what the estimates are computed from:
// `cost_per_hour_usd` and `min_billed_seconds` per STT provider (§7.1), and the token
// prices per LLM entry.
//
// What is therefore *not* guaranteed, stated plainly rather than implied away:
//
//  1. **Under-estimates overshoot.** A day can exceed the cap by the sum of the amounts
//     by which admitted calls cost more than they claimed. Bounded only by the caller's
//     cost model, not by this package.
//  2. **Concurrency across processes overshoots.** The in-flight reservation is
//     per-process memory, so a call in flight in another Lambda instance is invisible
//     both in the ledger and here. §10.5's `ReservedConcurrentExecutions: 5` bounds
//     this: the worst case is roughly the cap plus four concurrent calls' worth of
//     spend. §0.7.5 makes the same point from the other side — "the daily breaker is
//     per-tenant, not per-agent." An exact bound needs an atomic counter (a DynamoDB
//     conditional increment), and the settled Repository interface offers no such
//     operation, so this is documented rather than claimed.
//  3. **A cost that cannot be metered is charged to the day as its estimate.** If the
//     usage write fails, or a successful call reports no cost at all, the spend happened
//     and the ledger will never show it. The estimate stays reserved against that UTC day
//     rather than being released, so the escaped spend still consumes budget. The price
//     is a double count if a retry later meters the real cost; that direction of error
//     refuses a call that could have been admitted, and the other direction is an
//     uncapped bill.
//  4. **The window is a UTC day** (clock.Date). The cap resets at 00:00 UTC, not at the
//     user's local midnight, so an evening session in a positive-offset zone is billed
//     against the following day's budget. Chosen because the Usage sort key partitions
//     by UTC month (§6.3) and a local-midnight window would need records from two month
//     partitions on the boundary day. The consequence is a reset time that surprises,
//     not a cap that leaks.
//
// # What this package does not do
//
// The only record it writes is the Usage record for a call it admitted (see above). It
// writes no audit record (I13): Usage records are cost telemetry, not user content — the
// audit obligation belongs to the handler that reads the transcript, not to the meter
// that counted its seconds.
//
// It also produces no user-facing copy. §10.5.9 requires the user be alerted; the
// figures for that message are on the Refusal, and rendering them is the caller's job
// with strings resolved from `branding` (§7.3).
package breaker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
)

// settleTimeout bounds the detached ledger write. Independent of the caller's deadline,
// because the spend has already happened and must be recorded regardless — but bounded, so
// a settle cannot hang a handler that is otherwise finished.
const settleTimeout = 5 * time.Second

// Sentinel reasons. Compared with errors.Is, so the caller's retry decision is a branch
// on the reason rather than on a string.
var (
	// ErrCapExceeded means today's projected spend is at or over the configured cap.
	//
	// **Retrying before tomorrow is pointless** — nothing reduces a day's metered
	// spend, since Usage records are write-once (§6.3). This is the distinction that
	// matters to a caller: a provider timeout should be retried, and this must not be,
	// or the retry loop becomes a tight loop against a permanent refusal.
	ErrCapExceeded = errors.New("breaker: daily spend cap reached")

	// ErrSpendUnknown means today's spend could not be determined.
	//
	// Refusing here is the single most important behaviour in this package: an
	// unreadable ledger is exactly when an open breaker costs the most, because
	// whatever is wrong with storage is unlikely to be limited to one read. Unlike
	// ErrCapExceeded this *is* retryable — the underlying fault may be transient — but
	// the call must not proceed in the meantime.
	ErrSpendUnknown = errors.New("breaker: today's spend could not be determined")

	// ErrNoEstimate means the caller supplied no positive cost estimate.
	//
	// A programming defect rather than a policy outcome, but still a refusal: without
	// an estimate the breaker can only detect a breach after it has happened, which is
	// the "cap becomes a floor" failure described in the package comment.
	ErrNoEstimate = errors.New("breaker: a positive cost estimate is required")

	// ErrNoCall means Do was handed a nil closure.
	//
	// A refusal, not a bare error, because Refused(err) is the caller's "did the
	// provider call happen" test and fn plainly did not run. Returning a plain
	// fmt.Errorf here put a programming defect into the caller's *other* branch — the
	// one documented as "transient provider fault, retry" — where it retries for ever.
	ErrNoCall = errors.New("breaker: no provider call was supplied to guard")

	// ErrUnmetered means a call ran but its cost did not reach the ledger.
	//
	// Never a Refusal: the provider call happened, so Refused must stay false or the
	// biconditional in the package comment breaks. The cap has lost sight of this
	// spend, which is why the estimate stays reserved for the rest of the day
	// (overshoot case 3) and why the caller must treat this as a failed stage rather
	// than as a successful call with a bookkeeping wrinkle.
	ErrUnmetered = errors.New("breaker: the cost of a completed provider call was not recorded")
)

// Cost is what a guarded provider call reports back: the billable operation it actually
// performed (I12), which Do writes to the ledger before releasing the reservation.
//
// Deliberately not meter.Event: it has no Tenant field, so a closure cannot meter one
// tenant's spend against another tenant's ledger (I11). Do supplies the tenant from its
// own argument, which is the tenant whose cap was just checked.
type Cost struct {
	Unit       model.MeterUnit
	Quantity   float64
	Provider   string
	CostMicros int64
	Op         string
}

// reported distinguishes "this call had a cost" from "the caller reported nothing".
//
// Any populated field counts, including a zero CostMicros alongside a unit and a
// provider: a call that really was free still has a cost basis worth recording, and
// treating it as unreported would charge the estimate to the day (overshoot case 3).
func (c Cost) reported() bool {
	return c != Cost{}
}

// Ledger is the usage ledger the cap is computed from and settled into. *meter.Meter
// satisfies it.
//
// Both methods on one interface, deliberately: the record has to land in the same ledger
// the total is read from, and two separately injected dependencies allow a wiring in
// which it does not — which would look exactly like a working breaker while the cap saw
// none of the spend. Declared here rather than imported wholesale so this package depends
// on the two methods it uses, and so the fail-closed tests have a seam a repository fake
// cannot provide (a repository cannot simulate a read that hangs).
type Ledger interface {
	DayTotal(ctx context.Context, tenant keys.TenantID, day string) (int64, error)
	Record(ctx context.Context, ev meter.Event) error
}

// Decision is the arithmetic behind one verdict, in integer micros throughout.
//
// Money is never a float here for the same reason model.Usage.CostMicros is not: these
// figures are summed across records and rendered to a user, and binary rounding error
// in a spend cap shows up as a breaker that fires a fraction early or late with no
// reproducible cause.
type Decision struct {
	// Day is the UTC day the window covers, yyyy-mm-dd.
	Day string

	CapMicros   int64
	SpentMicros int64

	// PendingMicros is the total estimate of calls this *process* has admitted and not
	// yet settled. Not in the ledger yet — see overshoot case (2) in the package
	// comment.
	PendingMicros int64

	// UnmeteredMicros is the total estimate of calls this process admitted whose cost
	// never reached the ledger — overshoot case (3). Held against this Day only, so it
	// clears when the window rolls over rather than shrinking every future day's cap.
	UnmeteredMicros int64

	EstimateMicros int64

	// AvailableMicros is cap − spent − pending − unmetered, floored at zero.
	AvailableMicros int64
}

// Refusal is the error every refused call returns. It carries the figures so the caller
// can alert the user (§10.5.9) without a second query.
type Refusal struct {
	Decision
	Tenant keys.TenantID

	// Reason is one of the sentinels above.
	Reason error

	// Cause is the underlying fault for ErrSpendUnknown, or nil.
	//
	// Safe to include in Error() and to log directly: it is a storage or validation
	// error over metering records, which hold a unit, a quantity and a cost and no
	// transcript text — so §9.2's "provider errors may echo input" hazard does not apply
	// on this path. An error crossing a *provider* boundary would need
	// logging.ErrorAttr.
	Cause error

	// retryable records whether retrying before tomorrow could succeed, decided where the
	// refusal is decided because the reason alone does not always say: an unreadable
	// ledger and an unusable tenant both refuse with ErrSpendUnknown, and only the first
	// can clear on its own. Unexported so the default is the safe direction — a site that
	// forgets it reports "do not retry", which loses a call rather than spinning a retry
	// loop against a permanent failure.
	retryable bool
}

func (r *Refusal) Error() string {
	msg := fmt.Sprintf("%v for tenant %s on %s: spent %s + in-flight %s + unmetered %s + estimate %s against cap %s (USD)",
		r.Reason, r.Tenant, r.Day,
		FormatUSD(r.SpentMicros), FormatUSD(r.PendingMicros), FormatUSD(r.UnmeteredMicros),
		FormatUSD(r.EstimateMicros), FormatUSD(r.CapMicros))
	if r.Cause != nil {
		msg += ": " + r.Cause.Error()
	}
	return msg
}

// Unwrap exposes both the reason and the cause, so errors.Is(err, ErrSpendUnknown) and
// errors.Is(err, context.DeadlineExceeded) both work on the same value. A caller
// deciding whether to retry needs the first; an operator diagnosing needs the second.
func (r *Refusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{r.Reason}
	}
	return []error{r.Reason, r.Cause}
}

// RetryableToday reports whether retrying this call before tomorrow could ever succeed.
//
// Only a transient inability to read the ledger can: nothing reduces a day's metered spend
// (Usage records are write-once, §6.3), a corrupt total stays corrupt for the same reason,
// and a missing estimate, an unusable tenant or a nil closure are programming defects that
// the identical call reproduces exactly. It was computed as "not ErrCapExceeded", which
// answered true for all of those — turning a permanent failure into a retry loop, and
// telling an operator the opposite of the truth on the one line §10.1 gives them.
func (r *Refusal) RetryableToday() bool { return r.retryable }

// Refused reports whether err is this package refusing to let a call proceed.
//
// Matches on the type, not on the sentinel list: a reason added later is then covered
// automatically. Getting that wrong the other way — enumerating sentinels and forgetting
// one — is a fail-open bug, since the caller's "not a refusal" branch calls the provider.
func Refused(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// tenantState is one tenant's admission gate and in-process reservations.
//
// Per tenant rather than process-global: with one lock for the whole process, one
// tenant's slow ledger read stalls every other tenant's admission, and the stall is
// invisible — no refusal, no log line.
type tenantState struct {
	// gate serialises read-then-reserve for this tenant. Reading the ledger and
	// reserving against it must be one step, or two goroutines both observe the same
	// spend and both admit, which is the over-admission the reservation exists to
	// prevent.
	//
	// A 1-buffered channel rather than a sync.Mutex because acquisition has to honour
	// the caller's context. A mutex is not context-aware, so a waiter ignores its own
	// deadline: an SQS batch of ten records behind one 20-second DynamoDB retry chain
	// becomes a Lambda timeout with nine callers that produced neither a *Refusal nor a
	// WARN line. Blocking is not refusing, and the incident then presents as an
	// unexplained worker timeout instead of "spend could not be determined".
	gate chan struct{}

	// refs counts the Do and Check calls currently holding this entry, so it can be
	// dropped when the last one leaves. A container serving many tenants would otherwise
	// keep one gate per tenant it has ever seen.
	refs int

	pending int64

	// unmeteredDay scopes unmeteredMicros to one UTC window; a stale day is discarded on
	// the next evaluation rather than charged to it.
	unmeteredDay    string
	unmeteredMicros int64
}

// Breaker refuses provider calls once a tenant's daily spend cap is reached.
type Breaker struct {
	ledger    Ledger
	clk       clock.Clock
	log       *slog.Logger
	capMicros int64

	// mu guards the tenants map and the counters inside each entry. Held only for map
	// and arithmetic operations — never across the ledger read, which is what the
	// per-tenant gate covers.
	mu      sync.Mutex
	tenants map[keys.TenantID]*tenantState
}

// New builds a Breaker. capMicros is limits.daily_spend_usd (§7.4) in micros.
//
// A non-positive cap is an error rather than a value with a meaning. Both readings are
// dangerous: treated as "unlimited" it silently removes the only control on provider
// spend, and treated as "nothing permitted" it disables capture for a reason no log line
// would explain. An unset cap must fail the deploy, which is also what config's
// validator requires of the key itself (§Phase 0).
func New(ledger Ledger, clk clock.Clock, log *slog.Logger, capMicros int64) (*Breaker, error) {
	if ledger == nil {
		return nil, fmt.Errorf("breaker: a ledger is required; there is no mode in which the cap is unchecked (§10.5.9)")
	}
	if clk == nil {
		return nil, fmt.Errorf("breaker: a clock is required; the window is a day and nothing calls time.Now() directly")
	}
	if capMicros <= 0 {
		return nil, fmt.Errorf("breaker: daily cap is %d micros; it must be positive, and an absent cap must fail the deploy rather than read as unlimited (§10.5.9)", capMicros)
	}
	if log == nil {
		// A nil logger must not turn a refusal into a panic. A panic here would surface
		// as a crash on the provider-call path and be diagnosed as a provider fault —
		// the one misreading that leads someone to disable the breaker.
		log = slog.Default()
	}
	return &Breaker{
		ledger:    ledger,
		clk:       clk,
		log:       log,
		capMicros: capMicros,
		tenants:   make(map[keys.TenantID]*tenantState),
	}, nil
}

// microsPerUSD is the scale between the config's dollars and this package's integers.
const microsPerUSD = 1_000_000

// maxCapUSD is the largest amount that converts to micros inside int64: math.MaxInt64
// micros is ~9.22e12 USD, so 9e12 is comfortably under it.
//
// The bound is checked on the dollar input rather than on the converted result because
// converting an out-of-range float64 to int64 is implementation-defined in Go. arm64
// saturates to math.MaxInt64 and amd64 wraps to math.MinInt64, and §10.1 makes
// `Architectures: [arm64]` mandatory for the Lambdas — so the unchecked conversion gave
// production a nil error and a $9.2-trillion cap that never fires, while the same config
// failed the deploy on an x86 developer box. A CI run on amd64 proved nothing about it.
const maxCapUSD = 9_000_000_000_000.0

// MicrosFromUSD converts a configured dollar amount to integer micros.
//
// The single place limits.daily_spend_usd stops being a float. It is a float in the
// config file because that is how an operator writes money; every comparison and sum
// downstream is integer, so the conversion happens once, at the boundary, and is
// rounded rather than truncated — truncation would quietly shift a cap of 0.50 to
// 0.499999 on a value that does not represent exactly in binary.
func MicrosFromUSD(usd float64) (int64, error) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("breaker: daily cap %v is not a finite amount", usd)
	}
	if usd <= 0 {
		return 0, fmt.Errorf("breaker: daily cap %v must be positive (§10.5.9)", usd)
	}
	if usd > maxCapUSD {
		return 0, fmt.Errorf("breaker: daily cap %v USD exceeds the largest representable cap (%v USD); a value this size is a mistyped exponent, and converting it would saturate to an effectively unlimited cap on arm64 (§10.1)", usd, maxCapUSD)
	}
	return int64(math.Round(usd * microsPerUSD)), nil
}

// FormatUSD renders micros as an exact decimal dollar string.
//
// Integer arithmetic, not %f on a converted float: formatting money through a float is
// precisely what storing micros avoids, and this string appears in refusal messages an
// operator reconciles against a provider invoice (§Phase 0 requires agreement within
// 5%).
func FormatUSD(micros int64) string {
	sign := ""
	mag := uint64(micros)
	if micros < 0 {
		sign = "-"
		// Negated as an unsigned value. `micros = -micros` leaves math.MinInt64
		// negative in two's complement, so the integer part and the fractional part each
		// rendered their own minus sign: FormatUSD(math.MinInt64) produced
		// "--9223372036854.-775808" inside an operator-facing refusal message. Reachable
		// from a cost model that computes an estimate as int64(someFloat), which is
		// exactly math.MinInt64 on amd64 when that float is NaN or out of range.
		mag = -mag
	}
	return fmt.Sprintf("%s%d.%06d", sign, mag/microsPerUSD, mag%microsPerUSD)
}

// Do runs fn only if the tenant's projected spend for today stays within the cap, and
// records what the call cost.
//
// **This is the sanctioned path for every provider call.** estimateMicros is the
// caller's projected cost for this one call and must be positive.
//
// fn is called if and only if Do's own checks pass; any *Refusal returned means fn was
// never invoked, and conversely an error that is not a *Refusal means fn ran.
//
// fn returns the Cost it actually incurred, which Do writes to the ledger (I12) before
// releasing the reservation — see "Why Do writes the metering record". Two cases are
// worth stating:
//
//   - **A failed call that was still billed must report its cost.** A client-side
//     timeout does not cancel the work at the other end. Report the cost and the error
//     together; Do meters the cost and returns the error.
//   - **A call that never reached the provider reports Cost{}.** Nothing was billed, so
//     nothing is metered and the reservation is simply released.
//
// A successful call that reports Cost{} is a defect, not a free call: the cap would lose
// sight of the spend. Do returns ErrUnmetered and charges the estimate to the day
// (overshoot case 3).
//
// fn's own error is returned as it came, and it is the one error here that crossed a
// provider boundary: several APIs echo the offending input back, so a caller logging it
// must go through logging.ErrorAttr (§9.2). This package never logs it — the WARN line
// covers refusals only, and a refusal means fn never ran.
func (b *Breaker) Do(ctx context.Context, tenant keys.TenantID, estimateMicros int64, fn func(context.Context) (Cost, error)) error {
	if fn == nil {
		d := Decision{Day: clock.Date(b.clk.Now()), CapMicros: b.capMicros, EstimateMicros: estimateMicros}
		return b.refuse(ctx, tenant, d, ErrNoCall, nil, false)
	}

	st := b.attach(tenant)
	defer b.detach(tenant, st)

	if err := b.enterGate(ctx, st); err != nil {
		// Waiting for admission ran out of context. A refusal with a cause, rather than a
		// silent block until the caller's own timeout fires with nothing logged.
		d := Decision{Day: clock.Date(b.clk.Now()), CapMicros: b.capMicros, EstimateMicros: estimateMicros}
		return b.refuse(ctx, tenant, d, ErrSpendUnknown, fmt.Errorf("breaker: waiting for admission: %w", err), true)
	}

	d, err := b.evaluate(ctx, st, tenant, estimateMicros)
	if err != nil {
		b.leaveGate(st)
		b.logRefusal(ctx, err)
		return err
	}
	b.mu.Lock()
	st.pending += estimateMicros
	b.mu.Unlock()
	b.leaveGate(st)

	settled := false
	defer func() {
		if settled {
			return
		}
		// fn panicked. The provider may well have been billed, and nothing metered it, so
		// the estimate is charged to the day rather than handed back as headroom — the same
		// choice as a failed usage write (overshoot case 3). The panic keeps propagating;
		// this only decides which direction the accounting errs in on the way past.
		b.release(st, estimateMicros, d.Day, true)
	}()

	cost, callErr := fn(ctx)
	settleErr := b.settle(ctx, tenant, st, d.Day, estimateMicros, cost, callErr)
	settled = true

	switch {
	case callErr != nil && settleErr != nil:
		// Joined rather than wrapped one inside the other: the caller's retry decision
		// may depend on either, and errors.Is finds both in a join.
		return errors.Join(callErr, settleErr)
	case callErr != nil:
		return callErr
	default:
		return settleErr
	}
}

// settle writes the completed call's cost to the ledger and then releases its
// reservation, in that order.
//
// The order is the point: QueryPrefix is strongly consistent, so once Record has returned
// the next admission's DayTotal already includes this cost, and the reservation can drop
// without opening a gap. Releasing first would recreate exactly the window this fixes.
func (b *Breaker) settle(ctx context.Context, tenant keys.TenantID, st *tenantState, day string, estimateMicros int64, cost Cost, callErr error) error {
	if !cost.reported() {
		if callErr != nil {
			// Nothing reached the provider, so there is nothing to bill.
			b.release(st, estimateMicros, day, false)
			return nil
		}
		b.release(st, estimateMicros, day, true)
		return fmt.Errorf("%w: the call succeeded but reported no cost, so the cap cannot account for it (I12, §10.5.9)", ErrUnmetered)
	}

	// The tenant comes from Do's argument, never from the closure: the ledger written is
	// the ledger whose cap was checked (I11).
	ev := meter.Event{
		Tenant:     tenant,
		Unit:       cost.Unit,
		Quantity:   cost.Quantity,
		Provider:   cost.Provider,
		CostMicros: cost.CostMicros,
		Op:         cost.Op,
	}
	// The ledger write is DETACHED from the caller's context, and that is the whole point.
	//
	// A client-side timeout does not cancel the work at the other end: the provider call
	// already happened and the money is already spent. Settling with the caller's ctx meant
	// that on real storage — which fails any operation on a dead context — the usage record
	// was never written in exactly the case this function's own doc singles out. Spend that
	// is not recorded is spend the cap cannot see (I12, §10.5.9), so a run of timeouts would
	// quietly raise the effective daily limit with no log line to explain it.
	//
	// context.WithoutCancel keeps the caller's values — the correlation id, so the record is
	// still traceable to the request that caused it — while dropping its cancellation. The
	// timeout is bounded separately so a settle cannot hang a handler forever.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if err := b.ledger.Record(settleCtx, ev); err != nil {
		b.release(st, estimateMicros, day, true)
		return fmt.Errorf("%w: writing the usage record: %w", ErrUnmetered, err)
	}
	b.release(st, estimateMicros, day, false)
	return nil
}

// Check reports the verdict without reserving anything.
//
// For the pre-flight refusal at the request boundary, where the provider call happens
// later in a worker: rejecting an upload before it is enqueued is better than accepting
// it and refusing it silently downstream. **It is a forecast, not a permission** — it
// reserves nothing, so the worker must still go through Do.
//
// It also takes no admission gate, for the same reason: with nothing to reserve there is
// no read-then-reserve to make atomic, so a spend report cannot be blocked by an
// in-flight admission.
//
// Also the read path for a spend report: on success the Decision carries spent, pending,
// unmetered, cap and available.
func (b *Breaker) Check(ctx context.Context, tenant keys.TenantID, estimateMicros int64) (Decision, error) {
	st := b.attach(tenant)
	defer b.detach(tenant, st)

	d, err := b.evaluate(ctx, st, tenant, estimateMicros)
	if err != nil {
		b.logRefusal(ctx, err)
		return d, err
	}
	return d, nil
}

// evaluate computes the verdict. For Do the caller holds the tenant's gate, which is what
// makes read-then-reserve atomic; Check calls it without one because it reserves nothing.
func (b *Breaker) evaluate(ctx context.Context, st *tenantState, tenant keys.TenantID, estimateMicros int64) (Decision, error) {
	// The day is resolved first so that *every* Refusal carries its window, including the
	// ones decided before the ledger is consulted. A refusal logged with day="" cannot be
	// correlated with the ledger an operator would go and read.
	d := Decision{Day: clock.Date(b.clk.Now()), CapMicros: b.capMicros, EstimateMicros: estimateMicros}

	// Validate the tenant before anything else. An empty tenant means there is no
	// ledger to read, so it is a refusal and not a pass — and keys.Tenant is the
	// authority on what a usable tenant is (I11), rather than a second rule here that
	// could disagree with it.
	if _, err := keys.Tenant(tenant); err != nil {
		// Not retryable: the same tenant value fails identically every time.
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrSpendUnknown, Cause: err}
	}

	if estimateMicros <= 0 {
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrNoEstimate}
	}

	// Check the context before the read, not only its error afterwards. A repository
	// that ignores the context — the in-memory fake does, and a caching layer might —
	// would return a spend figure on an already-expired deadline, and that figure would
	// be trusted. The check has to be independent of the storage layer's context
	// discipline for "cannot determine spend" to actually mean it.
	if err := ctx.Err(); err != nil {
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrSpendUnknown, Cause: err, retryable: true}
	}

	// Reservations are snapshotted *before* the ledger read, and the ordering is
	// load-bearing. A settling call writes its usage record and only then drops its
	// reservation; reading pending afterwards could observe a call that has released its
	// reservation while reading a spend total taken before its record landed — the cost
	// in neither figure, and the same over-admission from the other side. Taking pending
	// first means a concurrent settle is counted twice at worst, which refuses rather
	// than admits.
	d.PendingMicros, d.UnmeteredMicros = b.reserved(st, d.Day)

	spent, err := b.ledger.DayTotal(ctx, tenant, d.Day)
	if err != nil {
		// The one refusal worth retrying within the day: the storage fault may be
		// transient, while the call must not proceed in the meantime.
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrSpendUnknown, Cause: err, retryable: true}
	}
	if spent < 0 {
		// meter refuses a negative cost, so a negative total means corrupted records or
		// arithmetic that has wrapped. Either way the ledger is not trustworthy, and an
		// untrustworthy ledger reads as unknown rather than as headroom.
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrSpendUnknown,
			Cause: fmt.Errorf("breaker: day total for %s is negative (%d micros)", d.Day, spent)}
	}
	d.SpentMicros = spent

	// Subtract stepwise and clamp rather than summing spent+pending+unmetered+estimate
	// and comparing: a caller passing an absurd estimate would overflow the sum to a
	// negative number, and the comparison would then pass. A fail-open path reachable
	// from an integer overflow is not a theoretical concern in the one function whose
	// job is to refuse.
	avail := b.capMicros
	for _, claimed := range [3]int64{spent, d.PendingMicros, d.UnmeteredMicros} {
		if claimed >= avail {
			avail = 0
			break
		}
		avail -= claimed
	}
	d.AvailableMicros = avail

	if estimateMicros > avail {
		return d, &Refusal{Decision: d, Tenant: tenant, Reason: ErrCapExceeded}
	}
	return d, nil
}

// attach returns this tenant's state, creating it if needed, and marks it in use.
func (b *Breaker) attach(tenant keys.TenantID) *tenantState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.tenants[tenant]
	if st == nil {
		st = &tenantState{gate: make(chan struct{}, 1)}
		b.tenants[tenant] = st
	}
	st.refs++
	return st
}

// detach drops the entry once nothing references it and it holds no reservation, so the
// map does not grow by one gate per tenant the container has ever served.
func (b *Breaker) detach(tenant keys.TenantID, st *tenantState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st.refs--
	if st.refs <= 0 && st.pending == 0 && st.unmeteredMicros == 0 {
		delete(b.tenants, tenant)
	}
}

// enterGate takes this tenant's admission gate, or gives up when the context does.
func (b *Breaker) enterGate(ctx context.Context, st *tenantState) error {
	select {
	case st.gate <- struct{}{}:
		return nil
	default:
	}
	// Only now is there contention worth waiting on. Attempted without blocking first
	// because a select with two ready cases picks at random: with a free gate and an
	// already-cancelled context, half of such calls would report a wait that never
	// happened instead of the expired-context refusal evaluate produces.
	select {
	case st.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Breaker) leaveGate(st *tenantState) { <-st.gate }

// reserved snapshots this tenant's in-flight and unmetered micros for one day.
//
// A retention recorded against a different day is discarded here rather than counted: the
// cap is per UTC day, and an unmetered cost from yesterday must not shrink today's budget.
func (b *Breaker) reserved(st *tenantState, day string) (pending, unmetered int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st.unmeteredDay != "" && st.unmeteredDay != day {
		st.unmeteredDay = ""
		st.unmeteredMicros = 0
	}
	return st.pending, st.unmeteredMicros
}

// release drops a completed call's reservation. When retain is true the estimate is moved
// to the day's unmetered total instead of being given back — the spend happened and the
// ledger will never show it (overshoot case 3).
func (b *Breaker) release(st *tenantState, estimateMicros int64, day string, retain bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st.pending -= estimateMicros
	if st.pending < 0 {
		// Cannot happen — the same amount is added on admission and subtracted here — but
		// a negative pending would read as headroom, and this file's job is to not do
		// that.
		st.pending = 0
	}
	if !retain {
		return
	}
	if st.unmeteredDay != day {
		st.unmeteredDay = day
		st.unmeteredMicros = 0
	}
	if st.unmeteredMicros > math.MaxInt64-estimateMicros {
		// Saturate rather than wrap. A wrapped total is negative, and a negative
		// deduction from the cap is extra headroom.
		st.unmeteredMicros = math.MaxInt64
		return
	}
	st.unmeteredMicros += estimateMicros
}

// refuse builds, logs and returns a Refusal for the paths that decide one outside
// evaluate, so that every refusal reaches the log with the same fields.
func (b *Breaker) refuse(ctx context.Context, tenant keys.TenantID, d Decision, reason, cause error, retryable bool) error {
	r := &Refusal{Decision: d, Tenant: tenant, Reason: reason, Cause: cause, retryable: retryable}
	b.logRefusal(ctx, r)
	return r
}

// logRefusal records the refusal for an operator.
//
// WARN, not ERROR: a breaker doing its job is not a fault. There are no CloudWatch
// alarms or SNS topics in this deployment (§10.1), so this line is the operational
// record rather than a page — and the figures are what make it actionable, since
// "refused" without the numbers cannot be told apart from a misconfigured cap.
//
// Every field logged is an identifier, an integer, or an error from storage or from
// validation. No user content is involved on this path (§9.2), and the tenant ID is safe
// to log — a refusal that cannot be attributed to a tenant is not actionable.
func (b *Breaker) logRefusal(ctx context.Context, err error) {
	var r *Refusal
	if !errors.As(err, &r) {
		return
	}
	log := logging.FromContext(ctx, b.log)
	if logging.TenantID(ctx) == "" {
		// Attach the tenant only when the context has not already supplied it —
		// duplicating a key in a JSON log line makes the field's value depend on which
		// occurrence the consumer keeps.
		log = log.With(slog.String("tenant_id", string(r.Tenant)))
	}
	attrs := []any{
		slog.String("reason", r.Reason.Error()),
		slog.String("day", r.Day),
		slog.Int64("cap_micros", r.CapMicros),
		slog.Int64("spent_micros", r.SpentMicros),
		slog.Int64("pending_micros", r.PendingMicros),
		slog.Int64("unmetered_micros", r.UnmeteredMicros),
		slog.Int64("estimate_micros", r.EstimateMicros),
		slog.Bool("retryable_today", r.RetryableToday()),
	}
	if r.Cause != nil {
		// Without the cause this line cannot distinguish a DynamoDB throttle from an IAM
		// denial, a missing table, an expired context or a corrupt record — every one of
		// which refuses every capture and shows spent_micros=0 against a non-zero cap.
		// "Refused, spent 0" reads as the breaker itself being broken, which is the
		// misreading that gets it switched off. Logged verbatim rather than through
		// logging.ErrorAttr because Refusal.Cause is a storage or validation error over
		// metering records and carries no content; an error that had crossed a *provider*
		// boundary would need the redacting helper.
		attrs = append(attrs, slog.String("cause", r.Cause.Error()))
	}
	log.Warn("provider call refused by spend breaker", attrs...)
}
