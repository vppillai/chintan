// Package breaker bounds third-party spend per day, for the whole instance.
//
// v1 had nothing bounding Groq or OpenAI spend. Reserved Lambda concurrency
// caps AWS, not provider APIs, so a stuck retry loop or a leaked login billed
// without limit.
//
// The design point worth preserving: Do owns the provider call. There is no way
// to reach the provider without reserving against the day's counter first, and
// the breaker writes the usage log line itself — so a cost is never in neither
// the pending nor the spent column.
//
// There is one cap and one counter. The instance's operator sets the cap in the
// template (DailySpendCapMicros) and every paid call on the instance reserves
// against the same SPEND#<day> row. The 2026-09-03 review found the previous
// design — a per-tenant cap read from the settings record, a resolver
// interface, a "lower of the two" rule, and a usage sink nothing in production
// constructed — guarding complexity rather than a requirement: the thing that
// stops a runaway loop is the ADD-and-compare, and this is it.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// ErrSpendCapExceeded is returned when a call would take the instance past its
// daily cap. It is deliberately distinct so the UI can explain it rather than
// showing a generic failure.
var ErrSpendCapExceeded = errors.New("breaker: daily spend cap exceeded")

// Counter is the atomic per-day accumulator.
//
// Add must be atomic and must return the post-increment total; a read-then-write
// implementation reintroduces exactly the race this package exists to close.
// Negative deltas release a reservation.
type Counter interface {
	Add(ctx context.Context, day string, deltaMicros int64) (int64, error)
}

// Estimate is what a call is predicted to cost, used to reserve budget before
// the provider is contacted.
type Estimate struct {
	Provider string
	Model    string
	Op       meter.Op
	Usage    meter.Quantities
	// TenantID is who the call is attributed to in the per-tenant usage rows.
	// The cap is instance-wide and ignores it; the accounting does not. It is
	// an explicit field rather than read off the context because the context's
	// tenant is for log attribution only (obs.WithTenant), and a cost record
	// should not depend on a logging convenience having been set upstream.
	TenantID string
}

// Result is what the call actually consumed. An empty Usage means the caller
// could not measure it, and the estimate stands.
type Result struct {
	Usage meter.Quantities
}

// Breaker enforces a daily spend cap.
type Breaker struct {
	counter  Counter
	prices   meter.PriceTable
	capMicro int64
	now      func() time.Time
	usage    usage.Recorder
}

// Option configures a Breaker.
type Option func(*Breaker)

// WithClock overrides the clock. For tests.
func WithClock(fn func() time.Time) Option {
	return func(b *Breaker) { b.now = fn }
}

// WithUsage attributes every settled call to its tenant through rec, in the
// same place the breaker writes the usage log line. Without it the breaker
// counts and caps exactly as before and attributes nothing.
func WithUsage(rec usage.Recorder) Option {
	return func(b *Breaker) { b.usage = rec }
}

// New builds a Breaker. A capMicros of 0 or less disables the cap while still
// counting, which is the correct behaviour for a fresh install that has no
// spend history to set a cap from.
func New(counter Counter, prices meter.PriceTable, capMicros int64, opts ...Option) *Breaker {
	b := &Breaker{
		counter:  counter,
		prices:   prices,
		capMicro: capMicros,
		now:      time.Now,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Do reserves budget, runs fn, then reconciles the reservation against actual
// usage and logs it.
//
// On any failure of fn the reservation is released, so a provider outage does
// not consume the day's budget. A redelivery cannot bypass the cap: every
// attempt reserves before it calls, and an attempt that is refused releases
// exactly what it reserved.
func (b *Breaker) Do(ctx context.Context, est Estimate, fn func(context.Context) (Result, error)) (Result, error) {
	day := b.now().UTC().Format("2006-01-02")
	estimated := b.prices.Cost(est.Provider, est.Model, est.Usage)

	total, err := b.counter.Add(ctx, day, estimated)
	if err != nil {
		return Result{}, fmt.Errorf("breaker: reserve: %w", err)
	}

	if b.capMicro > 0 && total > b.capMicro {
		// Release immediately: a rejected call must not hold budget.
		if _, rerr := b.counter.Add(ctx, day, -estimated); rerr != nil {
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
			slog.Int64("cap_micros", b.capMicro),
			slog.Int64("would_be_micros", total),
			slog.String("op", string(est.Op)))
		return Result{}, ErrSpendCapExceeded
	}

	started := b.now()
	res, callErr := fn(ctx)
	elapsed := b.now().Sub(started)

	if callErr != nil {
		if _, rerr := b.counter.Add(ctx, day, -estimated); rerr != nil {
			obs.Log(ctx).Error("failed to release spend reservation after provider error",
				slog.String("error", rerr.Error()),
				slog.Int64("reserved_micros", estimated))
		}
		obs.Duration(ctx, "ProviderLatency", elapsed, map[string]string{"Provider": est.Provider, "Op": string(est.Op), "Outcome": "error"})
		return Result{}, callErr
	}

	// Reconcile: settle the difference between what we reserved and what the
	// call actually used, so the day's total reflects reality.
	actualUsage := est.Usage
	if len(res.Usage) > 0 {
		actualUsage = res.Usage
	}
	actual := b.prices.Cost(est.Provider, est.Model, actualUsage)
	if delta := actual - estimated; delta != 0 {
		if _, rerr := b.counter.Add(ctx, day, delta); rerr != nil {
			obs.Log(ctx).Error("failed to reconcile spend reservation",
				slog.String("error", rerr.Error()),
				slog.Int64("delta_micros", delta))
		}
	}

	// The record of the call. Written by the breaker, not the caller, so a
	// caller that forgets cannot leave a cost in neither column. It carries
	// shape and cost only — no transcript, prompt or completion text — and the
	// log group's retention is how long it lives.
	attrs := []any{
		slog.String("provider", est.Provider),
		slog.String("model", est.Model),
		slog.String("op", string(est.Op)),
		slog.Int64("cost_micros", actual),
		slog.Int64("day_total_micros", total+actual-estimated),
	}
	for unit, quantity := range actualUsage {
		attrs = append(attrs, slog.Float64(string(unit), quantity))
	}
	obs.Log(ctx).Info("provider usage", attrs...)
	b.record(ctx, day, est, actual, actualUsage)
	obs.Emit(ctx,
		map[string]string{"Provider": est.Provider, "Op": string(est.Op)},
		obs.Metric{Name: "ProviderCostMicros", Value: float64(actual), Unit: obs.UnitCount},
		obs.Metric{Name: "ProviderCalls", Value: 1, Unit: obs.UnitCount},
	)
	obs.Duration(ctx, "ProviderLatency", elapsed, map[string]string{"Provider": est.Provider, "Op": string(est.Op), "Outcome": "ok"})
	return res, nil
}

// record attributes a settled call to its tenant. It runs after the spend
// counter is reconciled and the log line is written, and its failure is logged
// rather than returned: the call has already happened and been paid for, and
// failing the capture over a missing accounting row would turn a bookkeeping
// fault into lost dictation. The log line above is the record of last resort.
//
// A call with no tenant — there is none today; every pipeline stage sets one —
// is logged once and skipped rather than written to an anonymous row, because
// an anonymous row is a number nobody can bill or explain.
func (b *Breaker) record(ctx context.Context, day string, est Estimate, costMicros int64, used meter.Quantities) {
	if b.usage == nil {
		return
	}
	if est.TenantID == "" {
		obs.Log(ctx).Warn("provider call carried no tenant; usage not attributed",
			slog.String("op", string(est.Op)))
		return
	}
	err := b.usage.Record(ctx, usage.Record{
		TenantID:   est.TenantID,
		Day:        day,
		Provider:   est.Provider,
		Op:         est.Op,
		CostMicros: costMicros,
		Usage:      used,
	})
	if err != nil {
		obs.Log(ctx).Error("failed to record tenant usage",
			slog.String("error", err.Error()),
			slog.String("op", string(est.Op)),
			slog.Int64("cost_micros", costMicros))
		obs.Count(ctx, "UsageRecordFailures", map[string]string{"Op": string(est.Op)})
	}
}
