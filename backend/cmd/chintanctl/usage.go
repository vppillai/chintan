package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The usage rows, as internal/usage writes them. Spelled here because this
// command works on raw items; the two are pinned together by
// TestUsageListsTheMonthRowsInternalUsageWrites, which seeds rows through the
// real writer.
const (
	usageSKPrefix    = "USAGE#"
	usageGSI1        = "gsi1"
	usageGSI1PK      = "gsi1pk"
	usageTenantSKPfx = "TENANT#"
	usageOpPrefix    = "op_"
	usageCostSuffix  = "_cost_micros"
)

var usageMonthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// usageTenant is one tenant's month.
type usageTenant struct {
	TenantID     string           `json:"tenant_id"`
	CostMicros   int64            `json:"cost_micros"`
	Calls        int64            `json:"calls"`
	AudioSeconds float64          `json:"audio_seconds"`
	APIRequests  int64            `json:"api_requests"`
	OpCostMicros map[string]int64 `json:"op_cost_micros"`
}

type usageResult struct {
	Target     target        `json:"target"`
	Month      string        `json:"month"`
	Tenants    []usageTenant `json:"tenants"`
	CostMicros int64         `json:"cost_micros"`
}

func (r *usageResult) human(w *lineWriter) {
	w.printf("usage %s (%s) for %s\n", r.Target.Instance, r.Target.Environment, r.Month)
	if len(r.Tenants) == 0 {
		w.printf("  no tenant has a usage row for this month\n")
		return
	}
	w.printf("  %-40s %12s %7s %10s %9s  %s\n", "tenant", "cost", "calls", "audio min", "api req", "per-op cost")
	for _, t := range r.Tenants {
		ops := make([]string, 0, len(t.OpCostMicros))
		for op := range t.OpCostMicros {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		parts := make([]string, 0, len(ops))
		for _, op := range ops {
			parts = append(parts, op+"="+dollars(t.OpCostMicros[op]))
		}
		w.printf("  %-40s %12s %7d %10.1f %9d  %s\n", t.TenantID, dollars(t.CostMicros), t.Calls, t.AudioSeconds/60, t.APIRequests, strings.Join(parts, " "))
	}
	w.printf("  %-40s %12s\n", fmt.Sprintf("total (%d tenants)", len(r.Tenants)), dollars(r.CostMicros))
}

// dollars renders microdollars for a summary line. Four decimals: a month of
// personal use is cents, and "$0.04" would hide most of what the table is for.
func dollars(micros int64) string {
	return fmt.Sprintf("$%.4f", float64(micros)/1_000_000)
}

func cmdUsage(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var month string
	fs := newFlagSet("usage", stderr)
	g.register(fs, true, false)
	fs.StringVar(&month, "month", time.Now().UTC().Format("2006-01"), "calendar month, UTC, as yyyy-mm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !usageMonthRe.MatchString(month) {
		return fmt.Errorf("--month %q must be yyyy-mm", month)
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runUsage(ctx, e, month, g.tenants)
	if err != nil {
		return err
	}
	return report(stdout, g.jsonOut, res)
}

// runUsage lists the month. With no --tenant it finds the tenants through
// GSI1 — every month row carries gsi1pk USAGE#<month>, gsi1sk TENANT#<id> for
// exactly this — and then reads each tenant's month row and its day rows in
// one prefix scan, since the index projects none of the counters. The cost
// counters come off the month row; api_requests is the sum of the day rows,
// as internal/usage.Month reads it, because the request counter writes the
// day row only. With --tenant it reads those rows directly. It writes nothing.
func runUsage(ctx context.Context, e *env, month string, explicitTenants []string) (*usageResult, error) {
	tenants := append([]string(nil), explicitTenants...)
	if len(tenants) == 0 {
		err := e.Part.ScanIndex(ctx, usageGSI1, usageGSI1PK, usageSKPrefix+month, func(it Item) error {
			id := strings.TrimPrefix(it.Str("gsi1sk"), usageTenantSKPfx)
			if id == "" || id == it.Str("gsi1sk") {
				return fmt.Errorf("usage index row %s %s has no tenant sort key", it.PK(), it.SK())
			}
			tenants = append(tenants, id)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, id := range tenants {
		if err := checkTenantID(id); err != nil {
			return nil, err
		}
	}
	sort.Strings(tenants)

	res := &usageResult{Target: e.Target, Month: month, Tenants: []usageTenant{}}
	for _, id := range tenants {
		var monthRow Item
		var requests int64
		err := e.Part.Scan(ctx, tenantPK(id), usageSKPrefix+month, func(it Item) error {
			switch sk := it.SK(); {
			case sk == usageSKPrefix+month:
				monthRow = it
			case strings.HasPrefix(sk, usageSKPrefix+month+"-"):
				requests += it.Num("api_requests")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if monthRow == nil {
			// Named explicitly and has no row: nothing to show, not an error.
			continue
		}
		t := usageTenantOf(id, monthRow)
		t.APIRequests = requests
		res.Tenants = append(res.Tenants, t)
		res.CostMicros += t.CostMicros
	}
	return res, nil
}

// usageTenantOf reads the cost counters off a month row; api_requests is the
// caller's to fill from the day rows. The per-op split is recovered by
// attribute-name prefix, as the API reads it, so an operation added later
// appears here without a code change.
func usageTenantOf(tenantID string, it Item) usageTenant {
	t := usageTenant{
		TenantID:     tenantID,
		CostMicros:   it.Num("cost_micros"),
		Calls:        it.Num("calls"),
		AudioSeconds: it.Float("audio_seconds"),
		OpCostMicros: map[string]int64{},
	}
	for name := range it {
		rest, ok := strings.CutPrefix(name, usageOpPrefix)
		if !ok {
			continue
		}
		if op, found := strings.CutSuffix(rest, usageCostSuffix); found && op != "" {
			t.OpCostMicros[op] = it.Num(name)
		}
	}
	return t
}
