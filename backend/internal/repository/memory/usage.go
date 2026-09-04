package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Usage is an in-memory usage.Recorder and usage.Reader for tests and local
// development. It accumulates the same shape the DynamoDB rows hold — a month
// total, its per-op split, and one total per day — so a handler test reads
// back what the breaker would have written.
type Usage struct {
	mu      sync.Mutex
	records []usage.Record
}

var (
	_ usage.Recorder = (*Usage)(nil)
	_ usage.Reader   = (*Usage)(nil)
)

// NewUsage returns an empty in-memory usage store.
func NewUsage() *Usage { return &Usage{} }

func (u *Usage) Record(ctx context.Context, r usage.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.records = append(u.records, r)
	return nil
}

// Records returns everything recorded, in order, for tests to assert on.
func (u *Usage) Records() []usage.Record {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]usage.Record(nil), u.records...)
}

func (u *Usage) Month(ctx context.Context, tenantID, month string) (usage.Month, error) {
	if err := ctx.Err(); err != nil {
		return usage.Month{}, err
	}
	if !usage.ValidMonth(month) {
		return usage.Month{}, usage.ErrBadMonth
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	out := usage.Month{Month: month, Ops: map[string]usage.Totals{}, Days: []usage.Day{}}
	days := map[string]*usage.Totals{}
	for _, r := range u.records {
		if r.TenantID != tenantID || !strings.HasPrefix(r.Day, month+"-") {
			continue
		}
		apply(&out.Totals, r)
		op := out.Ops[string(r.Op)]
		apply(&op, r)
		out.Ops[string(r.Op)] = op
		if days[r.Day] == nil {
			days[r.Day] = &usage.Totals{}
		}
		apply(days[r.Day], r)
	}
	for date, t := range days {
		out.Days = append(out.Days, usage.Day{Date: date, Totals: *t})
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Date < out.Days[j].Date })
	return out, nil
}

func apply(t *usage.Totals, r usage.Record) {
	t.CostMicros += r.CostMicros
	t.Calls++
	t.AudioSeconds += r.Usage[meter.UnitAudioSeconds]
	t.InputTokens += int64(r.Usage[meter.UnitInputTokens])
	t.OutputTokens += int64(r.Usage[meter.UnitOutputTokens])
}
