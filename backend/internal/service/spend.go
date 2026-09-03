package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrSpendCapped means the instance's daily provider budget is already spent,
// so a new capture would produce a recording nothing will transcribe.
//
// It is a distinct error, not a generic failure, because the UI has to explain
// a budget decision rather than report a fault — and because a client that
// cannot tell the two apart will retry the one that must not be retried.
var ErrSpendCapped = errors.New("daily provider spend cap reached")

// SpendCounter is the atomic per-day accumulator the breaker enforces the cap
// with. The API reads it; only the worker writes to it.
//
// The interface is satisfied by pipeline.DynamoCounter. It is declared here so
// the API binary depends on the shape of the counter and not on the pipeline.
type SpendCounter interface {
	Add(ctx context.Context, day string, deltaMicros int64) (int64, error)
}

// SpendGate answers "would a new capture be transcribed, or refused?" before a
// recording is uploaded.
//
// Enforcement itself belongs to the breaker, which owns the provider call. This
// is the courtesy check that turns a silent dead end — audio uploaded, capture
// stuck at spend_capped, nothing explaining why — into a 429 the client can
// show before the user speaks. It reads the same counter against the same cap
// the breaker enforces, so the two cannot disagree.
type SpendGate struct {
	counter   SpendCounter
	capMicros int64
	now       func() time.Time
}

// NewSpendGate builds the gate. A capMicros of 0 or less disables the check,
// matching a breaker that counts without enforcing.
func NewSpendGate(counter SpendCounter, capMicros int64) *SpendGate {
	return &SpendGate{counter: counter, capMicros: capMicros, now: time.Now}
}

// Capped reports whether the instance has already reached its cap today.
//
// The read is an ADD of zero: the counter's only operation is atomic, and
// reading through it means there is no second code path that could disagree
// with what the breaker enforces.
func (g *SpendGate) Capped(ctx context.Context) (bool, error) {
	if g == nil || g.counter == nil || g.capMicros <= 0 {
		return false, nil
	}
	day := g.now().UTC().Format("2006-01-02")
	total, err := g.counter.Add(ctx, day, 0)
	if err != nil {
		// A counter that cannot be read must not become a closed door. The
		// breaker still refuses the provider call, so the worst case is a
		// capture that fails later with a clear status rather than an outage.
		return false, fmt.Errorf("spend gate: read counter: %w", err)
	}
	return total >= g.capMicros, nil
}
