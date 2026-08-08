package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// usageSKPrefix is the sort key prefix internal/meter's records carry:
// USAGE#<yyyy-mm-dd>#<id>. The date is inside the key so a date range is a
// key condition rather than a filter over the whole partition.
const usageSKPrefix = "USAGE#"

type usageRow struct {
	TenantID   string  `json:"tenant_id"`
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	Op         string  `json:"op"`
	Unit       string  `json:"unit"`
	Calls      int     `json:"calls"`
	Quantity   float64 `json:"quantity"`
	CostMicros int64   `json:"cost_micros"`
}

type usageResult struct {
	Target     target     `json:"target"`
	Since      string     `json:"since,omitempty"`
	Tenants    []string   `json:"tenants"`
	Records    int        `json:"records"`
	Rows       []usageRow `json:"rows"`
	CostMicros int64      `json:"cost_micros"`
}

func (r *usageResult) human(w io.Writer) {
	fmt.Fprintf(w, "usage %s (%s)", r.Target.Instance, r.Target.Environment)
	if r.Since != "" {
		fmt.Fprintf(w, " since %s", r.Since)
	}
	fmt.Fprintln(w)
	if r.Records == 0 {
		fmt.Fprintf(w, "  no USAGE# records in range\n")
		return
	}
	fmt.Fprintf(w, "  %-16s %-8s %-22s %-10s %-14s %8s %14s %12s\n",
		"TENANT", "PROVIDER", "MODEL", "OP", "UNIT", "CALLS", "QUANTITY", "COST (USD)")
	for _, row := range r.Rows {
		fmt.Fprintf(w, "  %-16s %-8s %-22s %-10s %-14s %8d %14.3f %12s\n",
			truncate(row.TenantID, 16), truncate(row.Provider, 8), truncate(row.Model, 22),
			truncate(row.Op, 10), truncate(row.Unit, 14), row.Calls, row.Quantity,
			usd(row.CostMicros))
	}
	fmt.Fprintf(w, "  %d records, total %s\n", r.Records, usd(r.CostMicros))
}

func usd(micros int64) string {
	return fmt.Sprintf("$%.4f", float64(micros)/1_000_000)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func cmdUsage(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var since string
	fs := newFlagSet("usage", stderr)
	g.register(fs, true, false)
	fs.StringVar(&since, "since", "", "only count records on or after this date (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if since != "" {
		if _, err := time.Parse("2006-01-02", since); err != nil {
			return fmt.Errorf("--since %q must be YYYY-MM-DD", since)
		}
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runUsage(ctx, e, g.tenants, since)
	if err != nil {
		return err
	}
	return report(stdout, g.jsonOut, res)
}

// runUsage aggregates the metering records so spend is inspectable without the
// console. It reads shape and cost only: a USAGE# record deliberately carries
// no transcript, prompt, or completion, so nothing this command prints can be
// user content.
func runUsage(ctx context.Context, e *env, explicitTenants []string, since string) (*usageResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}
	res := &usageResult{Target: e.Target, Since: since, Tenants: tenants}

	agg := map[string]*usageRow{}
	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)
		err := e.Part.Scan(tctx, tenantPK(tenantID), usageSKPrefix, func(it Item) error {
			day := usageDay(it)
			if since != "" && day != "" && day < since {
				return nil
			}
			u := usageFromItem(it)
			res.Records++
			key := strings.Join([]string{tenantID, u.Provider, u.Model, string(u.Op), string(u.Unit)}, "\x00")
			row, ok := agg[key]
			if !ok {
				row = &usageRow{
					TenantID: tenantID, Provider: u.Provider, Model: u.Model,
					Op: string(u.Op), Unit: string(u.Unit),
				}
				agg[key] = row
			}
			row.Calls++
			row.Quantity += u.Quantity
			row.CostMicros += u.CostMicros
			res.CostMicros += u.CostMicros
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	res.Rows = make([]usageRow, 0, len(agg))
	for _, row := range agg {
		res.Rows = append(res.Rows, *row)
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Op != b.Op {
			return a.Op < b.Op
		}
		return a.Unit < b.Unit
	})

	obs.Log(ctx).Info("aggregated usage",
		slog.Int("records", res.Records),
		slog.Int("rows", len(res.Rows)),
		slog.Int64("cost_micros", res.CostMicros),
	)
	return res, nil
}

// usageDay extracts the date from USAGE#<yyyy-mm-dd>#<id>, or "" when the key
// does not carry one.
func usageDay(it Item) string {
	rest, ok := strings.CutPrefix(it.SK(), usageSKPrefix)
	if !ok {
		return ""
	}
	day, _, _ := strings.Cut(rest, "#")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return ""
	}
	return day
}

// usageFromItem decodes a metering record. As everywhere else in the table,
// the `data` blob is the complete record and the promoted attributes are the
// fallback.
func usageFromItem(it Item) meter.Usage {
	var u meter.Usage
	if blob := it.Str("data"); blob != "" {
		if err := json.Unmarshal([]byte(blob), &u); err == nil && u.Provider != "" {
			return u
		}
	}
	u.TenantID = it.Str("tenant_id")
	u.Provider = it.Str("provider")
	u.Model = it.Str("model")
	u.Op = meter.Op(it.Str("op"))
	u.Unit = meter.Unit(it.Str("unit"))
	u.CostMicros = it.Num("cost_micros")
	if v, ok := it["quantity"]; ok && v.N != nil {
		if q, err := strconv.ParseFloat(*v.N, 64); err == nil {
			u.Quantity = q
		}
	}
	return u
}
