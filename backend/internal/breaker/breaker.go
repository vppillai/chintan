// Package breaker bounds third-party spend per tenant per day.
//
// v1 had nothing bounding Groq or OpenAI spend. Reserved Lambda concurrency
// caps AWS, not provider APIs, so a stuck retry loop or a leaked login billed
// without limit.
//
// The design point worth preserving: Do owns the provider call. There is no way
// to reach the provider without passing the check, and the breaker writes the
// metering record itself — so a cost is never in neither the pending nor the
// spent column.
//
// "Passing the check" has to mean the tenant's own number. Built with only the
// instance-wide cap — which defaults to 0, meaning record but never enforce —
// Do dutifully checked every call against a limit no tenant had chosen, while
// the cap a tenant actually set was honoured once, by a courtesy check on the
// request path, before any of the spending happened. The path to the provider
// was never skipped; it was measured against the wrong number, which reads the
// same in a log and costs the same on a bill. WithCapResolver is what closes
// that: see capFor for which of the two caps applies.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// ErrSpendCapExceeded is returned when a call would take a tenant past its
// daily cap. It is deliberately distinct so the UI can explain it rather than
// showing a generic failure.
var ErrSpendCapExceeded = errors.New("breaker: daily spend cap exceeded")

// Counter is an atomic per-tenant, per-day accumulator.
//
// Add must be atomic and must return the post-increment total; a read-then-write
// implementation reintroduces exactly the race this package exists to close.
// Negative deltas release a reservation.
type Counter interface {
	Add(ctx context.Context, tenantID, day string, deltaMicros int64) (int64, error)
}

// Estimate is what a call is predicted to cost, used to reserve budget before
// the provider is contacted.
type Estimate struct {
	Provider string
	Model    string
	Op       meter.Op
	Unit     meter.Unit
	Quantity float64
}

// Result is what the call actually consumed. Quantity of 0 means the caller
// could not measure it, and the estimate stands.
type Result struct {
	Unit     meter.Unit
	Quantity float64
}

// CapResolver reports the daily cap a tenant chose for themselves, in
// microdollars. A return of 0 means the tenant has set no cap of their own.
//
// It is an interface declared here, rather than a settings store handed in,
// because the breaker must not depend on how a cap is stored — and because the
// only question it is allowed to ask is this one.
type CapResolver interface {
	DailyCapMicros(ctx context.Context, tenantID string) (int64, error)
}

// Breaker enforces a daily spend cap.
type Breaker struct {
	counter  Counter
	sink     meter.Sink
	pricer   meter.Pricer
	capMicro int64
	caps     CapResolver
	now      func() time.Time
}

// Option configures a Breaker.
type Option func(*Breaker)

// WithClock overrides the clock. For tests.
func WithClock(fn func() time.Time) Option {
	return func(b *Breaker) { b.now = fn }
}

// WithCapResolver teaches the breaker to ask what cap the tenant set for
// themselves. Without it only the instance-wide cap is enforced, which is a cap
// no tenant ever chose.
func WithCapResolver(r CapResolver) Option {
	return func(b *Breaker) { b.caps = r }
}

// New builds a Breaker. A capMicros of 0 or less disables the cap while still
// metering, which is the correct behaviour for a fresh install that has no
// spend history to set a cap from.
func New(counter Counter, sink meter.Sink, pricer meter.Pricer, capMicros int64, opts ...Option) *Breaker {
	b := &Breaker{
		counter:  counter,
		sink:     sink,
		pricer:   pricer,
		capMicro: capMicros,
		now:      time.Now,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// capFor is the cap this call is measured against: the lower of the tenant's
// own daily cap and the instance-wide ceiling, ignoring whichever is unset.
//
// Both directions matter. A tenant who set $1 must not be able to spend the
// instance's $50, and a tenant who set $1,000 must not be able to raise the
// operator's ceiling by typing a bigger number into their own settings.
//
// A lookup that fails falls back to the instance ceiling rather than refusing.
// A settings read is a DynamoDB call like any other, and a throttle on it would
// otherwise stop every capture on the instance — including the ones no cap
// would have refused. The fallback is loud: the error is logged and counted, so
// "the tenant's cap is not being read" is visible rather than inferred.
func (b *Breaker) capFor(ctx context.Context, tenantID string) int64 {
	if b.caps == nil {
		return b.capMicro
	}
	tenantCap, err := b.caps.DailyCapMicros(ctx, tenantID)
	if err != nil {
		obs.Log(ctx).Error("could not read the tenant's daily spend cap; falling back to the instance cap",
			slog.String("error", err.Error()),
			slog.Int64("instance_cap_micros", b.capMicro))
		obs.Count(ctx, "SpendCapLookupFailures", map[string]string{"Reason": "resolver_error"})
		return b.capMicro
	}
	if tenantCap > 0 && (b.capMicro <= 0 || tenantCap < b.capMicro) {
		return tenantCap
	}
	return b.capMicro
}

// Do reserves budget, runs fn, then reconciles the reservation against actual
// usage and records it.
//
// On any failure of fn the reservation is released, so a provider outage does
// not consume the tenant's budget for the day.
func (b *Breaker) Do(ctx context.Context, tenantID string, est Estimate, fn func(context.Context) (Result, error)) (Result, error) {
	day := b.now().UTC().Format("2006-01-02")
	estimated := b.pricer.CostMicros(est.Provider, est.Model, est.Unit, est.Quantity)
	capMicros := b.capFor(ctx, tenantID)

	total, err := b.counter.Add(ctx, tenantID, day, estimated)
	if err != nil {
		return Result{}, fmt.Errorf("breaker: reserve: %w", err)
	}

	if capMicros > 0 && total > capMicros {
		// Release immediately: a rejected call must not hold budget.
		if _, rerr := b.counter.Add(ctx, tenantID, day, -estimated); rerr != nil {
			obs.Log(ctx).Error("failed to release spend reservation after cap rejection",
				slog.String("error", rerr.Error()),
				slog.Int64("reserved_micros", estimated))
		}
		// WithRollup, like its two siblings ProviderKeyRejected and
		// ProviderRateLimited: the dimensionless identity is what lets the
		// spend-cap alarm be an ordinary standard-resolution alarm instead of a
		// Metrics Insights query, which is billed per metric analysed with no
		// free tier. The dimensioned copy still says which provider and op.
		obs.CountWithRollup(ctx, "SpendCapRejections", map[string]string{"Provider": est.Provider, "Op": string(est.Op)})
		obs.Log(ctx).Warn("daily spend cap reached",
			slog.Int64("cap_micros", capMicros),
			slog.Int64("instance_cap_micros", b.capMicro),
			slog.Int64("would_be_micros", total),
			slog.String("op", string(est.Op)))
		return Result{}, ErrSpendCapExceeded
	}

	started := b.now()
	res, callErr := fn(ctx)
	elapsed := b.now().Sub(started)

	if callErr != nil {
		if _, rerr := b.counter.Add(ctx, tenantID, day, -estimated); rerr != nil {
			obs.Log(ctx).Error("failed to release spend reservation after provider error",
				slog.String("error", rerr.Error()),
				slog.Int64("reserved_micros", estimated))
		}
		obs.Duration(ctx, "ProviderLatency", elapsed, map[string]string{"Provider": est.Provider, "Op": string(est.Op), "Outcome": "error"})
		return Result{}, callErr
	}

	// Reconcile: settle the difference between what we reserved and what the
	// call actually used, so the day's total reflects reality.
	actualUnit, actualQty := est.Unit, est.Quantity
	if res.Quantity > 0 {
		actualUnit, actualQty = res.Unit, res.Quantity
	}
	actual := b.pricer.CostMicros(est.Provider, est.Model, actualUnit, actualQty)
	if delta := actual - estimated; delta != 0 {
		if _, rerr := b.counter.Add(ctx, tenantID, day, delta); rerr != nil {
			obs.Log(ctx).Error("failed to reconcile spend reservation",
				slog.String("error", rerr.Error()),
				slog.Int64("delta_micros", delta))
		}
	}

	// Recorded by the breaker, not the caller. A caller that forgets leaves a
	// cost in neither the pending nor the spent column.
	if b.sink != nil {
		if rerr := b.sink.Record(ctx, meter.Usage{
			TenantID:      tenantID,
			Provider:      est.Provider,
			Model:         est.Model,
			Op:            est.Op,
			Unit:          actualUnit,
			Quantity:      actualQty,
			CostMicros:    actual,
			CorrelationID: obs.CorrelationID(ctx),
			At:            b.now().UTC(),
		}); rerr != nil {
			// Metering must not fail a call the provider already completed and
			// billed for.
			obs.Log(ctx).Error("failed to record usage", slog.String("error", rerr.Error()))
		}
	}

	obs.Duration(ctx, "ProviderLatency", elapsed, map[string]string{"Provider": est.Provider, "Op": string(est.Op), "Outcome": "ok"})
	return res, nil
}
