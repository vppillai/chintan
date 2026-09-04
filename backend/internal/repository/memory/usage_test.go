package memory_test

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/usage"
)

func TestUsageAccumulatesByMonthOpAndDayPerTenant(t *testing.T) {
	ctx := context.Background()
	u := memory.NewUsage()
	for _, r := range []usage.Record{
		{TenantID: "a", Day: "2026-09-04", Op: meter.OpTranscribe, CostMicros: 300, Usage: meter.Quantities{meter.UnitAudioSeconds: 30}},
		{TenantID: "a", Day: "2026-09-04", Op: meter.OpCleanup, CostMicros: 600, Usage: meter.Quantities{meter.UnitInputTokens: 900, meter.UnitOutputTokens: 300}},
		{TenantID: "a", Day: "2026-09-05", Op: meter.OpCleanup, CostMicros: 100, Usage: meter.Quantities{meter.UnitInputTokens: 100}},
		{TenantID: "b", Day: "2026-09-04", Op: meter.OpCleanup, CostMicros: 9999},
		{TenantID: "a", Day: "2026-08-31", Op: meter.OpCleanup, CostMicros: 9999},
	} {
		if err := u.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := u.Month(ctx, "a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if got.CostMicros != 1000 || got.Calls != 3 || got.AudioSeconds != 30 || got.InputTokens != 1000 || got.OutputTokens != 300 {
		t.Errorf("totals = %+v", got.Totals)
	}
	if got.Ops["cleanup"].CostMicros != 700 || got.Ops["transcribe"].Calls != 1 {
		t.Errorf("ops = %+v", got.Ops)
	}
	if len(got.Days) != 2 || got.Days[0].Date != "2026-09-04" || got.Days[0].CostMicros != 900 || got.Days[1].CostMicros != 100 {
		t.Errorf("days = %+v", got.Days)
	}

	empty, err := u.Month(ctx, "a", "2026-07")
	if err != nil {
		t.Fatalf("Month(empty): %v", err)
	}
	if empty.CostMicros != 0 || len(empty.Days) != 0 || empty.Days == nil || empty.Ops == nil {
		t.Errorf("empty month = %+v", empty)
	}
	if _, err := u.Month(ctx, "a", "2026-9"); err == nil {
		t.Error("a malformed month was accepted")
	}
}
