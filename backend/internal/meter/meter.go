// Package meter records billable operations (I12).
//
// I12: "Every billable operation emits a metering event with tenant_id, unit, quantity,
// and provider cost basis."
//
// §2A.1 explains why this is Phase 0 work rather than deferred with the rest of billing:
//
//	"You cannot retroactively measure the past. Every pricing model that could ever
//	exist is built on per-tenant STT seconds, LLM tokens, and stored bytes. This is ~20
//	lines and is immediately useful for personal cost tracking."
//
// It is also load-bearing for something in use today, not only for a hypothetical
// commercial future: the daily spend circuit breaker (§10.5.9) computes spend from
// these records before every provider call. Unmetered usage is not merely unbillable,
// it is uncapped.
//
// **Every provider adapter calls Meter.Record.** There is one function, deliberately,
// so that adding a provider cannot accidentally add an unmetered path.
package meter

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// usageTTLMonths is retention.usage_months (§6.3): 25 months, covering annual
// reconciliation plus a year. Passed in from config rather than hardcoded — this
// constant is only the documented default for the error message.
const usageTTLMonthsDefault = 25

// Event is one billable operation.
type Event struct {
	Tenant   keys.TenantID
	Unit     model.MeterUnit
	Quantity float64

	// Provider identifies the third party. Required: §9.2 needs a record of "which
	// provider processed which content, so a future privacy policy can be accurate and
	// a provider change can be reasoned about."
	Provider string

	// CostMicros is millionths of a USD. Integer by design — see model.Usage.
	CostMicros int64

	// Op distinguishes operations that share a unit. Shadow-mode transcription must
	// use a distinct op so its doubled spend is visible and can be switched off
	// knowingly (§7.2).
	Op string
}

// IDGenerator produces the sortable unique component of a usage key.
type IDGenerator interface {
	NewID() string
}

// Meter writes usage records.
type Meter struct {
	repo      repository.Repository
	clk       clock.Clock
	ids       IDGenerator
	log       *slog.Logger
	ttlMonths int
}

// New builds a Meter. ttlMonths comes from retention.usage_months (§7.4).
func New(repo repository.Repository, clk clock.Clock, ids IDGenerator, log *slog.Logger, ttlMonths int) *Meter {
	if ttlMonths <= 0 {
		ttlMonths = usageTTLMonthsDefault
	}
	return &Meter{repo: repo, clk: clk, ids: ids, log: log, ttlMonths: ttlMonths}
}

// Record writes one metering event.
//
// **Returns an error rather than swallowing one, and callers must not ignore it.** The
// temptation is to treat metering as best-effort telemetry that should never fail a
// user's request — but the spend breaker reads these records, so a silently dropped
// event raises the effective daily cap. A provider call whose metering failed is a call
// that escaped the cap.
//
// What a caller does with the error is a judgement per call site: the pipeline should
// fail the stage, since it can be retried and the audio is buffered (I2).
func (m *Meter) Record(ctx context.Context, ev Event) error {
	if err := ev.validate(); err != nil {
		return err
	}

	now := m.clk.Now()
	month := clock.Month(now)

	key, err := keys.Usage(ev.Tenant, month, string(ev.Unit), m.ids.NewID())
	if err != nil {
		return fmt.Errorf("meter: %w", err)
	}

	rec := model.Usage{
		ID:         key.SK,
		Unit:       ev.Unit,
		Quantity:   ev.Quantity,
		Provider:   ev.Provider,
		CostMicros: ev.CostMicros,
		Op:         ev.Op,
		TS:         clock.RFC3339UTC(now),
		TTL:        now.AddDate(0, m.ttlMonths, 0).Unix(),
	}

	item := repository.Item{
		Key: key,
		Attrs: map[string]any{
			"unit":        string(rec.Unit),
			"quantity":    rec.Quantity,
			"provider":    rec.Provider,
			"cost_micros": rec.CostMicros,
			"op":          rec.Op,
			"ts":          rec.TS,
			"ttl":         rec.TTL,
		},
		TTL: rec.TTL,
		// No GSI1 attributes. Usage is high-volume and must never project into the
		// sparse index, or it becomes a second copy of the table (§6.3).
	}

	// PutOnce, not Put: usage records are write-once (§6.3). A duplicate key would mean
	// the ID generator collided, which is worth failing on rather than overwriting a
	// record that a cost reconciliation may already have counted.
	if err := m.repo.PutOnce(ctx, item); err != nil {
		return fmt.Errorf("meter: writing usage record: %w", err)
	}

	// Logged at debug with no content — a unit, a quantity, and a cost are not user
	// content, but the op and provider are all that is needed to trace spend (§9.2).
	logging.FromContext(ctx, m.log).Debug("metered",
		slog.String("unit", string(ev.Unit)),
		slog.Float64("quantity", ev.Quantity),
		slog.String("provider", ev.Provider),
		slog.Int64("cost_micros", ev.CostMicros),
		slog.String("op", ev.Op),
	)
	return nil
}

func (ev Event) validate() error {
	if _, err := keys.Tenant(ev.Tenant); err != nil {
		return fmt.Errorf("meter: %w", err)
	}
	switch ev.Unit {
	case model.UnitSTTSeconds, model.UnitLLMInputTokens, model.UnitLLMOutputTokens,
		model.UnitEmbeddingTokens, model.UnitStorageBytes, model.UnitRequests:
	default:
		return fmt.Errorf("meter: unknown unit %q; the enumeration in model is the complete set (I12)", ev.Unit)
	}
	if ev.Quantity < 0 {
		return fmt.Errorf("meter: quantity %v is negative", ev.Quantity)
	}
	if ev.Provider == "" {
		// §9.2 requires knowing which provider processed which content. An unattributed
		// cost also cannot be reconciled against a provider's invoice, which §Phase 0
		// acceptance requires to within 5%.
		return fmt.Errorf("meter: provider is required (§9.2)")
	}
	if ev.Op == "" {
		return fmt.Errorf("meter: op is required; shadow-mode spend must be distinguishable from the active provider's (§7.2)")
	}
	if ev.CostMicros < 0 {
		return fmt.Errorf("meter: cost_micros %d is negative", ev.CostMicros)
	}
	return nil
}

// MonthTotal sums cost across one month, in micros.
//
// Used by usage.sh for the per-tenant cost report, and by the breaker for its window.
// Reads a bounded key range rather than scanning, which is what the month partitioning
// in the sort key is for (§6.3).
func (m *Meter) MonthTotal(ctx context.Context, tenant keys.TenantID, month string) (int64, error) {
	pk, prefix, err := keys.UsageMonthPrefix(tenant, month)
	if err != nil {
		return 0, fmt.Errorf("meter: %w", err)
	}
	items, err := m.repo.QueryPrefix(ctx, pk, prefix, 0)
	if err != nil {
		return 0, fmt.Errorf("meter: reading usage for %s: %w", month, err)
	}
	var total int64
	for _, it := range items {
		if c, ok := it.Attrs["cost_micros"].(int64); ok {
			total += c
		}
	}
	return total, nil
}

// DayTotal sums cost across one day, in micros.
//
// The breaker's window (§10.5.9). Reads the month range and filters by the record's
// timestamp: the sort key partitions by month, not day, so a day is a filter rather
// than a narrower range read. At the modelled volume — ~45 segments/day (§10.7) — a
// month is a few hundred small items, so this is cheap. It would not be at commercial
// scale, and the fix then is a day-partitioned key, which is why the ULID in the key
// keeps records sortable within their unit.
func (m *Meter) DayTotal(ctx context.Context, tenant keys.TenantID, day string) (int64, error) {
	if len(day) < 7 {
		return 0, fmt.Errorf("meter: day %q is not yyyy-mm-dd", day)
	}
	month := day[:7]
	pk, prefix, err := keys.UsageMonthPrefix(tenant, month)
	if err != nil {
		return 0, fmt.Errorf("meter: %w", err)
	}
	items, err := m.repo.QueryPrefix(ctx, pk, prefix, 0)
	if err != nil {
		return 0, fmt.Errorf("meter: reading usage for %s: %w", month, err)
	}
	var total int64
	for _, it := range items {
		ts, _ := it.Attrs["ts"].(string)
		if len(ts) < 10 || ts[:10] != day {
			continue
		}
		if c, ok := it.Attrs["cost_micros"].(int64); ok {
			total += c
		}
	}
	return total, nil
}
