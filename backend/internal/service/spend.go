package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrSpendCapped means the tenant's daily provider budget is already spent, so
// a new capture would produce a recording nothing will transcribe.
//
// It is a distinct error, not a generic failure, because the UI has to explain
// a budget decision rather than report a fault — and because a client that
// cannot tell the two apart will retry the one that must not be retried.
var ErrSpendCapped = errors.New("daily provider spend cap reached")

// SpendCounter is the atomic per-tenant, per-day accumulator the breaker
// enforces the cap with. The API reads it; only the worker writes to it.
//
// The interface is satisfied by pipeline.DynamoCounter. It is declared here so
// the API binary depends on the shape of the counter and not on the pipeline.
type SpendCounter interface {
	Add(ctx context.Context, tenantID, day string, deltaMicros int64) (int64, error)
}

// SpendGate answers "would a new capture be transcribed, or refused?" before a
// recording is uploaded.
//
// Enforcement itself belongs to the breaker, which owns the provider call. This
// is the courtesy check that turns a silent dead end — audio uploaded, capture
// stuck at spend_capped, nothing explaining why — into a 429 the client can
// show before the user speaks.
//
// It is a courtesy and nothing more, and for a while it was the only place the
// tenant's own cap was read at all: the breaker enforced the instance-wide cap,
// which defaults to unlimited, so a capture accepted here at $0.99 spent went
// on to run an unbounded transcription and two LLM calls, and anything already
// queued never passed this check in the first place. The breaker now resolves
// the same tenant cap itself (breaker.WithCapResolver, wired in cmd/worker),
// so the number checked here and the number enforced there are the same one.
type SpendGate struct {
	counter          SpendCounter
	defaultCapMicros int64
	settings         *SettingsService
	now              func() time.Time
}

// NewSpendGate builds the gate. A defaultCapMicros of 0 or less disables
// enforcement while still metering, which is correct for a fresh install with
// no spend history to set a cap from.
func NewSpendGate(counter SpendCounter, settings *SettingsService, defaultCapMicros int64) *SpendGate {
	return &SpendGate{
		counter:          counter,
		defaultCapMicros: defaultCapMicros,
		settings:         settings,
		now:              time.Now,
	}
}

// Capped reports whether the tenant has already reached its cap today.
//
// The read is an ADD of zero: the counter's only operation is atomic, and
// reading through it means there is no second code path that could disagree
// with what the breaker enforces.
func (g *SpendGate) Capped(ctx context.Context, tenantID string) (bool, error) {
	if g == nil || g.counter == nil {
		return false, nil
	}

	capMicros := g.defaultCapMicros
	if g.settings != nil {
		settings, err := g.settings.GetSettings(ctx, tenantID)
		if err != nil {
			return false, fmt.Errorf("spend gate: read settings: %w", err)
		}
		if settings.DailySpendCapMicros > 0 {
			capMicros = settings.DailySpendCapMicros
		}
	}
	if capMicros <= 0 {
		return false, nil
	}

	day := g.now().UTC().Format("2006-01-02")
	total, err := g.counter.Add(ctx, tenantID, day, 0)
	if err != nil {
		// A counter that cannot be read must not become a closed door. The
		// breaker still refuses the provider call, so the worst case is a
		// capture that fails later with a clear status rather than an outage.
		return false, fmt.Errorf("spend gate: read counter: %w", err)
	}
	return total >= capMicros, nil
}
