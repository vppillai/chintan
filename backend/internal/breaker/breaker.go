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

// Breaker enforces a daily spend cap.
type Breaker struct {
	counter  Counter
	sink     meter.Sink
	pricer   meter.Pricer
	capMicro int64
	now      func() time.Time
}

// Option configures a Breaker.
type Option func(*Breaker)

// WithClock overrides the clock. For tests.
func WithClock(fn func() time.Time) Option {
	return func(b *Breaker) { b.now = fn }
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

// Do reserves budget, runs fn, then reconciles the reservation against actual
// usage and records it.
//
// On any failure of fn the reservation is released, so a provider outage does
// not consume the tenant's budget for the day.
func (b *Breaker) Do(ctx context.Context, tenantID string, est Estimate, fn func(context.Context) (Result, error)) (Result, error) {
	day := b.now().UTC().Format("2006-01-02")
	estimated := b.pricer.CostMicros(est.Provider, est.Model, est.Unit, est.Quantity)

	total, err := b.counter.Add(ctx, tenantID, day, estimated)
	if err != nil {
		return Result{}, fmt.Errorf("breaker: reserve: %w", err)
	}

	if b.capMicro > 0 && total > b.capMicro {
		// Release immediately: a rejected call must not hold budget.
		if _, rerr := b.counter.Add(ctx, tenantID, day, -estimated); rerr != nil {
			obs.Log(ctx).Error("failed to release spend reservation after cap rejection",
				slog.String("error", rerr.Error()),
				slog.Int64("reserved_micros", estimated))
		}
		obs.Count(ctx, "SpendCapRejections", map[string]string{"Provider": est.Provider, "Op": string(est.Op)})
		obs.Log(ctx).Warn("daily spend cap reached",
			slog.Int64("cap_micros", b.capMicro),
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
