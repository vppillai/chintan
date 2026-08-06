package main

// `chintanctl usage` — the per-tenant cost report (§11.4, Phase 0).
//
// §11.4: "Per-tenant cost report from Usage records, by month and provider; reconciles
// metered totals against actual AWS and provider bills."
//
// # What this report is for
//
// §10.7 is the budget it defends: a modelled ~$0.35–1.05/month, with a hard instruction
// that "if any phase's design pushes recurring cost above $5/month, stop and flag it
// before implementing." That is only enforceable if the actual figure is cheap to obtain,
// so the report states the §10.7 status of every month it covers rather than leaving the
// comparison to whoever remembers the table.
//
// The second job is §Phase 0's acceptance criterion: "a round trip of upload → transcribe
// produces metering records whose summed cost_micros matches the provider's reported cost
// within 5%." The metered side of that comparison is computed here from Usage records
// (I12). The billed side cannot be: no AWS or provider billing API is reachable from this
// binary — Cost Explorer would need an SDK dependency this module does not carry, and it
// charges $0.01 per request, which against a ~$1/month budget is a measurable fraction of
// the thing being measured. So the actual figures are supplied by the operator from the
// invoice (--actual, --actual-total) and this computes the variance in integer micros and
// answers the 5% question directly.
//
// # Money is integer micros, everywhere
//
// The tolerance is 5%; float money does not reconcile. See the money section of obs.go.
// The only float near money is a config price, converted once — and the only float in the
// arithmetic is a duration in seconds, which is genuinely fractional.

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const usageCmdUsage = `chintanctl usage --tenant <id> [--month yyyy-mm | --since yyyy-mm --until yyyy-mm]

Per-tenant cost report from Usage records, by month, provider, unit and op (§11.4).
Reconciles metered totals against the actual AWS and provider bills, and reports each
month against the §10.7 budget.

Read-only. No --apply, and none needed (§11.3). One audit record is written for the
invocation (I13, §11.3) before anything is read.

Money is integer micro-dollars throughout, because §Phase 0 requires summed cost_micros
to match the provider's reported cost within 5% and float money does not reconcile.

The billed side of that comparison is supplied, not fetched: no billing API is reachable
from here, and Cost Explorer charges $0.01 per request against a ~$1/month budget. Read
the figures off the provider's invoice and the AWS bill:

  --actual <provider>=<usd>   billed amount for one provider, repeatable
  --actual-total <usd>        billed amount for everything in the window

Exit codes: 0 report produced, 3 a reconciliation line is outside tolerance, 4 a month
exceeds the §10.7 ceiling of $5, 1 failure, 2 invocation error.

Examples:
  chintanctl usage --tenant u-123 --config ../config/instances/prod.yaml
  chintanctl usage --tenant u-123 --config ../config/instances/prod.yaml \
      --month 2026-07 --actual groq_whisper_turbo=0.38 --actual-total 1.02 --json
`

// §10.7's figures, as micros. The target is the top of the modelled range ($0.35–1.05)
// rather than its midpoint: a month at $1.04 is inside the design, a month at $1.20 is a
// signal to re-derive the table, and the spec is explicit that the rows are the thing to
// re-derive rather than the total to trust.
const (
	usageTargetMicros  = 1_050_000
	usageCeilingMicros = 5_000_000
)

// usageRetentionMonthsDefault mirrors retention.usage_months (§6.3): 25 months, annual
// reconciliation plus a year. Used as the window cap when no config is loaded.
const usageRetentionMonthsDefault = 25

// usageBudget statuses.
const (
	usageWithinTarget = "within_target"
	usageAboveTarget  = "above_target"
	usageAboveCeiling = "above_ceiling"
)

// actualFlag collects repeated --actual provider=usd pairs.
//
// Parsed at flag time so a malformed figure is refused before any audit record is written:
// an invocation that never read anything should not leave a record claiming it did.
type actualFlag struct {
	order  []string
	micros map[string]int64
}

func (a *actualFlag) String() string { return strings.Join(a.order, ",") }

func (a *actualFlag) Set(v string) error {
	name, amount, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("expected <provider>=<usd>, e.g. groq_whisper_turbo=0.38")
	}
	micros, err := obsParseUSD(amount)
	if err != nil {
		return err
	}
	if micros < 0 {
		return fmt.Errorf("billed amount for %s is negative; a credit is not a bill", name)
	}
	if a.micros == nil {
		a.micros = map[string]int64{}
	}
	if _, dup := a.micros[name]; dup {
		// Refused rather than last-wins: two --actual flags for one provider means the
		// operator is reading two lines off an invoice and one of them would vanish.
		return fmt.Errorf("provider %s given twice; sum the invoice lines and pass one figure", name)
	}
	a.order = append(a.order, name)
	a.micros[name] = micros
	return nil
}

// ---------------------------------------------------------------------------
// Report shape (the --json contract, §11.3)
// ---------------------------------------------------------------------------

type usageReport struct {
	Tenant string `json:"tenant"`
	Source string `json:"source"`
	Since  string `json:"since"`
	Until  string `json:"until"`

	Months []usageMonthReport `json:"months"`

	Records    int    `json:"records"`
	CostMicros int64  `json:"cost_micros"`
	CostUSD    string `json:"cost_usd"`

	// Budget is the worst status across the reported months (§10.7).
	Budget         string               `json:"budget"`
	TargetMicros   int64                `json:"budget_target_micros"`
	CeilingMicros  int64                `json:"budget_ceiling_micros"`
	WorstMonth     string               `json:"budget_worst_month,omitempty"`
	Reconciliation *usageReconciliation `json:"reconciliation,omitempty"`

	// OwnAuditRecord is the audit record this invocation wrote (I13). Reported so the
	// report and the log it produced can be tied together.
	OwnAuditRecord string `json:"own_audit_record"`

	Notes []string `json:"notes,omitempty"`
}

type usageMonthReport struct {
	Month      string     `json:"month"`
	Rows       []usageRow `json:"rows"`
	Records    int        `json:"records"`
	CostMicros int64      `json:"cost_micros"`
	CostUSD    string     `json:"cost_usd"`
	Budget     string     `json:"budget"`

	Pricing []usagePricingCheck `json:"pricing_check,omitempty"`
}

// usageRow is one (provider, unit, op) group.
//
// op is a reported dimension rather than an aggregated-away detail because §7.2 requires
// shadow-mode spend to be visible: shadow transcription doubles STT cost under a distinct
// op, and a report that summed it into the active provider's line would make the doubling
// invisible in the one place someone looks for it.
type usageRow struct {
	Provider   string  `json:"provider"`
	Unit       string  `json:"unit"`
	Op         string  `json:"op"`
	Records    int     `json:"records"`
	Quantity   float64 `json:"quantity"`
	CostMicros int64   `json:"cost_micros"`
	CostUSD    string  `json:"cost_usd"`
}

// usagePricingCheck compares recorded cost against what the configured price implies.
//
// This is the half of the §Phase 0 reconciliation that needs no invoice, and it catches a
// specific failure the invoice check cannot isolate: an adapter metering the wrong basis.
// G-013 is the reason it is per-record rather than per-total — Groq bills a 10-second
// minimum per request, so 45 two-second segments cost the same as 45 ten-second ones, and
// an expected figure derived from summed seconds would understate the bill by 5× while
// looking arithmetically sound.
type usagePricingCheck struct {
	Provider        string `json:"provider"`
	Unit            string `json:"unit"`
	Basis           string `json:"basis"`
	ExpectedMicros  int64  `json:"expected_micros"`
	RecordedMicros  int64  `json:"recorded_micros"`
	VarianceBP      *int64 `json:"variance_bp,omitempty"`
	WithinTolerance bool   `json:"within_tolerance"`
}

type usageReconciliation struct {
	ToleranceBP int64            `json:"tolerance_bp"`
	Lines       []usageReconLine `json:"lines"`
	Within      bool             `json:"within_tolerance"`
}

type usageReconLine struct {
	Subject       string `json:"subject"`
	MeteredMicros int64  `json:"metered_micros"`
	MeteredUSD    string `json:"metered_usd"`
	ActualMicros  int64  `json:"actual_micros"`
	ActualUSD     string `json:"actual_usd"`
	DiffMicros    int64  `json:"diff_micros"`
	VarianceBP    *int64 `json:"variance_bp,omitempty"`
	Within        bool   `json:"within_tolerance"`
	Note          string `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func runUsage(args []string) int {
	fs := newFlagSet("usage", usageCmdUsage)
	var f obsFlags
	f.register(fs, "script:usage.sh")
	month := fs.String("month", "", "single month yyyy-mm (sugar for --since=--until); defaults to the current month")
	since := fs.String("since", "", "first month yyyy-mm")
	until := fs.String("until", "", "last month yyyy-mm, inclusive")
	toleranceBP := fs.Int64("tolerance-bp", 500, "reconciliation tolerance in basis points (500 = 5%, the §Phase 0 figure)")
	actualTotal := fs.String("actual-total", "", "billed total for the window, in USD, from the AWS bill")
	actuals := &actualFlag{}
	fs.Var(actuals, "actual", "billed amount for one provider as <provider>=<usd>, repeatable")
	if err := fs.Parse(args); err != nil {
		return obsExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "chintanctl usage: unexpected argument %q\n\n%s", fs.Arg(0), usageCmdUsage)
		return obsExitUsage
	}
	if *toleranceBP < 0 {
		fmt.Fprintln(os.Stderr, "chintanctl usage: --tolerance-bp cannot be negative")
		return obsExitUsage
	}

	var totalActual *int64
	if *actualTotal != "" {
		m, err := obsParseUSD(*actualTotal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "chintanctl usage: --actual-total: %v\n", err)
			return obsExitUsage
		}
		if m < 0 {
			fmt.Fprintln(os.Stderr, "chintanctl usage: --actual-total is negative; a credit is not a bill")
			return obsExitUsage
		}
		totalActual = &m
	}

	// The window is resolved and validated BEFORE openObs, so a malformed month never
	// produces an audit record: a record attesting an access that could not have happened
	// is noise in the one log that must stay trustworthy (I13).
	from, to, err := usageWindow(*month, *since, *until, clock.System{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "chintanctl usage: %v\n", err)
		return obsExitUsage
	}

	ctx := context.Background()
	env, err := openObs(ctx, &f, obsActionUsage, "usage-report:"+from+".."+to)
	if err != nil {
		return obsFail("usage", err)
	}

	rep, err := buildUsageReport(ctx, env, from, to, actuals, totalActual, *toleranceBP)
	if err != nil {
		return obsFail("usage", err)
	}

	if f.asJSON {
		if err := obsEmitJSON(os.Stdout, rep); err != nil {
			return obsFail("usage", err)
		}
	} else {
		writeUsageHuman(os.Stdout, rep)
	}
	return usageExitCode(rep)
}

// usageExitCode turns the findings into a code a caller can branch on.
//
// The budget breach outranks the reconciliation mismatch: §10.7 says a month over $5 is "a
// design error, not a budget overrun" and must stop the phase, whereas a variance against
// an invoice is a measurement to investigate. A caller that only checks for non-zero sees
// both; one that branches sees the more serious first.
func usageExitCode(rep *usageReport) int {
	if rep.Budget == usageAboveCeiling {
		return obsExitBudget
	}
	if rep.Reconciliation != nil && !rep.Reconciliation.Within {
		return obsExitVariance
	}
	return obsExitOK
}

// usageWindow resolves --month/--since/--until into an inclusive month range.
func usageWindow(month, since, until string, clk clock.Clock) (string, string, error) {
	if month != "" && (since != "" || until != "") {
		return "", "", fmt.Errorf("--month is sugar for --since=--until; pass one form or the other")
	}
	if month != "" {
		since, until = month, month
	}
	if since == "" && until == "" {
		// Default to the month in progress. clock rather than time.Now so the clock
		// package keeps its monopoly and a test can pin the default.
		now := clock.Month(clk.Now())
		since, until = now, now
	}
	if since == "" {
		since = until
	}
	if until == "" {
		until = since
	}
	// time.Parse rather than a length check or a regexp: it refuses 2026-13 and 2026-1,
	// both of which would otherwise build a key prefix that matches no record and report a
	// confident $0.00 — the same silent-empty failure meter.DayTotal's parse exists to
	// prevent.
	//
	// The rejected value is quoted only while it is short enough to be a month (see
	// obsShortArg): a refusal that echoes an unbounded argument is how a mis-passed value
	// ends up in a log, which is the lesson internal/audit paid for twice (§9.2).
	if _, err := time.Parse("2006-01", since); err != nil {
		return "", "", fmt.Errorf("--since %s is not yyyy-mm", obsShortArg(since))
	}
	if _, err := time.Parse("2006-01", until); err != nil {
		return "", "", fmt.Errorf("--until %s is not yyyy-mm", obsShortArg(until))
	}
	if since > until {
		return "", "", fmt.Errorf("--since %s is after --until %s", since, until)
	}
	return since, until, nil
}

// usageMonths expands an inclusive month range.
//
// Capped at the usage retention window: records older than retention.usage_months are gone
// (§6.3), so a longer window cannot return anything for its early months and would report
// $0.00 for them as though that were a measurement. Refusing says so instead.
func usageMonths(from, to string, cap int) ([]string, error) {
	start, err := time.Parse("2006-01", from)
	if err != nil {
		return nil, err
	}
	end, err := time.Parse("2006-01", to)
	if err != nil {
		return nil, err
	}
	var out []string
	for t := start; !t.After(end); t = t.AddDate(0, 1, 0) {
		out = append(out, t.Format("2006-01"))
		if len(out) > cap {
			return nil, fmt.Errorf("window %s..%s spans more than the %d-month usage retention window (§6.3); records before it have expired and would report as zero spend rather than as absent", from, to, cap)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

// usageRecord is one decoded Usage item.
type usageRecord struct {
	sk         string
	unit       string
	provider   string
	op         string
	quantity   float64
	costMicros int64
	ts         string
}

// readUsageMonth reads one month's usage records for one tenant.
//
// A bounded prefix read, not a scan: the month lives in the sort key precisely so this and
// the spend breaker can read a range (§6.3, keys.UsageMonthPrefix). The partition key comes
// from the key helper, so there is no expressible unscoped read (I11).
func readUsageMonth(ctx context.Context, repo repository.Repository, tenant keys.TenantID, month string) ([]usageRecord, error) {
	pk, prefix, err := keys.UsageMonthPrefix(tenant, month)
	if err != nil {
		return nil, err
	}
	items, err := repo.QueryPrefix(ctx, pk, prefix, 0)
	if err != nil {
		return nil, fmt.Errorf("reading usage for %s: %w", month, err)
	}
	out := make([]usageRecord, 0, len(items))
	for _, it := range items {
		rec := usageRecord{sk: it.Key.SK}
		// **A record that cannot be read is an error, never a zero row.** This is the same
		// reasoning meter.costMicros states for the breaker, and it applies with more force
		// here: the whole purpose of this report is a 5% comparison, and a handful of
		// records silently contributing nothing produces a total that is wrong in the
		// safe-looking direction — plausible, low, and unattributable (G-074).
		v, ok := it.Attrs["cost_micros"]
		if !ok {
			return nil, fmt.Errorf("usage record %s has no cost_micros; every usage record is written with one (I12)", it.Key.SK)
		}
		// repository.AsInt64, not a type assertion: the two Repository implementations
		// disagree on the Go type of a whole number, so `.(int64)` here would pass every
		// test against the fake and read as zero against DynamoDB (G-074).
		cost, ok := repository.AsInt64(v)
		if !ok {
			return nil, fmt.Errorf("usage record %s has a cost_micros that is not an exact integer number of micros (%T); money that arrived as a float has already lost the precision this report depends on", it.Key.SK, v)
		}
		rec.costMicros = cost

		if rec.unit, ok = it.Attrs["unit"].(string); !ok || rec.unit == "" {
			return nil, fmt.Errorf("usage record %s has no unit; a cost with no unit cannot be attributed (I12)", it.Key.SK)
		}
		if rec.provider, ok = it.Attrs["provider"].(string); !ok || rec.provider == "" {
			// meter.Record requires a provider precisely so this report can attribute a
			// cost to an invoice (§9.2, §Phase 0). A record without one predates that rule
			// or was not written by meter.
			return nil, fmt.Errorf("usage record %s has no provider; an unattributed cost cannot be reconciled against a bill (§9.2)", it.Key.SK)
		}
		rec.op, _ = it.Attrs["op"].(string)
		rec.ts, _ = it.Attrs["ts"].(string)
		if q, ok := repository.AsFloat64(it.Attrs["quantity"]); ok {
			rec.quantity = q
		} else if it.Attrs["quantity"] != nil {
			return nil, fmt.Errorf("usage record %s has an unreadable quantity (%T)", it.Key.SK, it.Attrs["quantity"])
		}
		out = append(out, rec)
	}
	return out, nil
}

func buildUsageReport(ctx context.Context, env *obsEnv, from, to string, actuals *actualFlag, actualTotal *int64, toleranceBP int64) (*usageReport, error) {
	retention := usageRetentionMonthsDefault
	if env.cfg != nil && env.cfg.Retention.UsageMonths != nil && *env.cfg.Retention.UsageMonths > 0 {
		retention = *env.cfg.Retention.UsageMonths
	}
	months, err := usageMonths(from, to, retention)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errObsUsage, err)
	}

	rep := &usageReport{
		Tenant:         string(env.tenant),
		Source:         env.source,
		Since:          from,
		Until:          to,
		TargetMicros:   usageTargetMicros,
		CeilingMicros:  usageCeilingMicros,
		Budget:         usageWithinTarget,
		OwnAuditRecord: env.ownAuditID,
	}

	// The breaker's own view of the month, from meter. Read for the cross-check below
	// rather than for the figures: the report and the §10.5.9 spend breaker must agree
	// about what a month cost, and if they do not, the breaker is the one that has to be
	// right — so a disagreement is a hard failure here rather than a footnote.
	mtr := meter.New(env.repo, env.clk, &obsIDs{}, obsLogger(), 0)

	perProvider := map[string]int64{}
	for _, m := range months {
		recs, err := readUsageMonth(ctx, env.repo, env.tenant, m)
		if err != nil {
			return nil, err
		}
		mr := usageMonthReport{Month: m}

		type groupKey struct{ provider, unit, op string }
		groups := map[groupKey]*usageRow{}
		for _, r := range recs {
			k := groupKey{r.provider, r.unit, r.op}
			row := groups[k]
			if row == nil {
				row = &usageRow{Provider: r.provider, Unit: r.unit, Op: r.op}
				groups[k] = row
			}
			row.Records++
			row.Quantity += r.quantity
			row.CostMicros += r.costMicros
			mr.Records++
			mr.CostMicros += r.costMicros
			perProvider[r.provider] += r.costMicros
		}
		for _, row := range groups {
			row.CostUSD = obsFormatUSD(row.CostMicros)
			mr.Rows = append(mr.Rows, *row)
		}
		sort.Slice(mr.Rows, func(i, j int) bool {
			a, b := mr.Rows[i], mr.Rows[j]
			if a.Provider != b.Provider {
				return a.Provider < b.Provider
			}
			if a.Unit != b.Unit {
				return a.Unit < b.Unit
			}
			return a.Op < b.Op
		})
		mr.CostUSD = obsFormatUSD(mr.CostMicros)
		mr.Budget = usageBudgetStatus(mr.CostMicros)
		mr.Pricing = usagePricingChecks(env, recs, toleranceBP)

		// Cross-check against meter.MonthTotal — the function the spend breaker uses.
		// Same records, read again, summed by the canonical implementation. It costs one
		// extra bounded prefix read per month at a few hundred small items (§10.7), which
		// is worth paying for the guarantee that the cost report and the cap cannot drift
		// apart silently.
		total, err := mtr.MonthTotal(ctx, env.tenant, m)
		if err != nil {
			return nil, fmt.Errorf("cross-checking %s against meter.MonthTotal: %w", m, err)
		}
		if total != mr.CostMicros {
			return nil, fmt.Errorf("month %s totals %d micros here and %d micros through meter.MonthTotal; the cost report and the §10.5.9 spend breaker must agree, and the breaker is the one that has to be right", m, mr.CostMicros, total)
		}

		rep.Months = append(rep.Months, mr)
		rep.Records += mr.Records
		rep.CostMicros += mr.CostMicros
		if usageBudgetWorse(mr.Budget, rep.Budget) {
			rep.Budget, rep.WorstMonth = mr.Budget, m
		}
	}
	rep.CostUSD = obsFormatUSD(rep.CostMicros)
	rep.Reconciliation = usageReconcile(perProvider, actuals, actualTotal, rep.CostMicros, toleranceBP)
	rep.Notes = usageNotes(rep, env)
	return rep, nil
}

func usageBudgetStatus(micros int64) string {
	switch {
	case micros > usageCeilingMicros:
		return usageAboveCeiling
	case micros > usageTargetMicros:
		return usageAboveTarget
	default:
		return usageWithinTarget
	}
}

func usageBudgetRank(status string) int {
	switch status {
	case usageAboveCeiling:
		return 2
	case usageAboveTarget:
		return 1
	default:
		return 0
	}
}

func usageBudgetWorse(a, b string) bool { return usageBudgetRank(a) > usageBudgetRank(b) }

// usagePricingChecks derives the expected cost from the configured provider price.
//
// Only stt_seconds, because it is the only unit §7.1 prices in config (cost_per_hour_usd,
// min_billed_seconds). A provider with no catalog entry or no price gets no check rather
// than a check against zero — an expected cost of nothing would report every real cost as
// infinitely over.
func usagePricingChecks(env *obsEnv, recs []usageRecord, toleranceBP int64) []usagePricingCheck {
	if env.cfg == nil {
		return nil
	}
	byProvider := map[string][]usageRecord{}
	for _, r := range recs {
		if r.unit != string(model.UnitSTTSeconds) {
			continue
		}
		byProvider[r.provider] = append(byProvider[r.provider], r)
	}
	var out []usagePricingCheck
	for provider, rs := range byProvider {
		entry, ok := env.cfg.Providers.STT.Catalog[provider]
		if !ok || entry.CostPerHourUSD == nil {
			continue
		}
		// The one place a float touches money, and it is the config boundary: the price is
		// a YAML float, converted to micros once, and every sum after this is integer.
		hourMicros := int64(math.Round(*entry.CostPerHourUSD * obsMicrosPerUSD))
		minBilled := 0.0
		if entry.MinBilledSecs != nil {
			minBilled = float64(*entry.MinBilledSecs)
		}
		var expected, recorded int64
		for _, r := range rs {
			// Per record, so the per-request minimum is applied per request. Summing the
			// seconds first and applying the minimum to the total would model a provider
			// that bills one long request, which is exactly the mistake G-013 describes.
			billed := r.quantity
			if billed < minBilled {
				billed = minBilled
			}
			expected += int64(math.Round(billed * float64(hourMicros) / 3600))
			recorded += r.costMicros
		}
		check := usagePricingCheck{
			Provider:       provider,
			Unit:           string(model.UnitSTTSeconds),
			Basis:          fmt.Sprintf("cost_per_hour_usd=%v min_billed_seconds=%v", *entry.CostPerHourUSD, minBilled),
			ExpectedMicros: expected,
			RecordedMicros: recorded,
		}
		if bp, ok := obsVarianceBP(recorded, expected); ok {
			check.VarianceBP = &bp
		}
		check.WithinTolerance = obsWithinTolerance(recorded, expected, toleranceBP)
		out = append(out, check)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// usageReconcile compares metered totals against the supplied billed figures.
//
// A provider named in --actual with no metering records is the finding this exists to
// surface, not an edge case to tolerate: billed spend with no Usage record means a provider
// call happened on a path that does not meter, which breaks I12 and — because the §10.5.9
// spend breaker computes the day from these records — means that spend is also uncapped.
func usageReconcile(perProvider map[string]int64, actuals *actualFlag, actualTotal *int64, meteredTotal, toleranceBP int64) *usageReconciliation {
	if (actuals == nil || len(actuals.order) == 0) && actualTotal == nil {
		return nil
	}
	rec := &usageReconciliation{ToleranceBP: toleranceBP, Within: true}
	add := func(subject string, metered, actual int64) {
		line := usageReconLine{
			Subject:       subject,
			MeteredMicros: metered,
			MeteredUSD:    obsFormatUSD(metered),
			ActualMicros:  actual,
			ActualUSD:     obsFormatUSD(actual),
			DiffMicros:    metered - actual,
		}
		if bp, ok := obsVarianceBP(metered, actual); ok {
			line.VarianceBP = &bp
		} else if metered == 0 {
			line.Note = "no billed amount and no metered spend"
		} else {
			line.Note = "billed nothing but metered spend exists; the recorded cost basis has no invoice behind it"
		}
		if actual > 0 && metered == 0 {
			line.Note = "billed spend with no metering records — an unmetered provider path (I12), which is also spend the §10.5.9 daily cap cannot see"
		}
		line.Within = obsWithinTolerance(metered, actual, toleranceBP)
		if !line.Within {
			rec.Within = false
		}
		rec.Lines = append(rec.Lines, line)
	}
	if actuals != nil {
		for _, name := range actuals.order {
			add(name, perProvider[name], actuals.micros[name])
		}
	}
	if actualTotal != nil {
		add("total", meteredTotal, *actualTotal)
	}
	return rec
}

// usageNotes states what the numbers do not say.
//
// Every note here exists because the figure alone reads as an answer when it is not one. An
// empty report is the important case: with I12 requiring a metering event for every billable
// operation, zero records means no billable operation ran — but it looks identical to a
// pipeline that is spending money without metering it, and those two need different actions.
func usageNotes(rep *usageReport, env *obsEnv) []string {
	var notes []string
	if rep.Records == 0 {
		notes = append(notes, "no usage records in this window: either no billable operation ran, or something is spending without metering (I12). The two look identical here — check whether a capture exists for the period before reading this as zero spend.")
	}
	if rep.Reconciliation == nil {
		notes = append(notes, "no billed figures supplied, so §Phase 0's 5% reconciliation is not closed. Pass --actual <provider>=<usd> from the provider invoice and --actual-total <usd> from the AWS bill for the same window.")
	}
	if env.cfg == nil {
		notes = append(notes, "no --config, so no pricing cross-check: the configured cost_per_hour_usd and min_billed_seconds are what an expected STT cost is derived from (§7.1, G-013).")
	}
	if rep.Budget != usageWithinTarget {
		notes = append(notes, "§10.7's modelled basis is ~20 minutes of speech a day, ~45 segments/day. If real usage diverges materially, re-derive the table rather than trusting the total.")
	}
	if rep.Budget == usageAboveCeiling {
		notes = append(notes, "above the $5/month ceiling: §10.7 calls that a design error rather than a budget overrun — stop and flag it before implementing further.")
	}
	return notes
}

// ---------------------------------------------------------------------------
// Human output
// ---------------------------------------------------------------------------

func writeUsageHuman(w io.Writer, rep *usageReport) {
	fmt.Fprintf(w, "usage report — tenant %s\n", rep.Tenant)
	fmt.Fprintf(w, "source: %s   window: %s..%s\n\n", rep.Source, rep.Since, rep.Until)

	for _, m := range rep.Months {
		fmt.Fprintf(w, "%s   %s USD   %d record(s)   [%s]\n", m.Month, obsPad(m.CostUSD), m.Records, m.Budget)
		if len(m.Rows) > 0 {
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  provider\tunit\top\tquantity\trecords\tcost USD")
			for _, r := range m.Rows {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\n",
					r.Provider, r.Unit, obsDash(r.Op), obsFormatQuantity(r.Quantity), r.Records, r.CostUSD)
			}
			_ = tw.Flush()
		}
		for _, p := range m.Pricing {
			status := "within tolerance"
			if !p.WithinTolerance {
				status = "OUTSIDE TOLERANCE"
			}
			fmt.Fprintf(w, "  pricing check %s (%s): recorded %s vs expected %s — %s\n",
				p.Provider, p.Basis, obsFormatUSD(p.RecordedMicros), obsFormatUSD(p.ExpectedMicros), status)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "total: %s USD across %d record(s)\n", obsFormatUSD(rep.CostMicros), rep.Records)
	fmt.Fprintf(w, "budget (§10.7): %s (target %s, ceiling %s)",
		rep.Budget, obsFormatUSD(rep.TargetMicros), obsFormatUSD(rep.CeilingMicros))
	if rep.WorstMonth != "" {
		fmt.Fprintf(w, " — worst month %s", rep.WorstMonth)
	}
	fmt.Fprintln(w)

	if rep.Reconciliation != nil {
		fmt.Fprintf(w, "\nreconciliation against billed figures (tolerance %d bp):\n", rep.Reconciliation.ToleranceBP)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  subject\tmetered USD\tbilled USD\tdiff USD\tvariance\tverdict")
		for _, l := range rep.Reconciliation.Lines {
			variance := "n/a"
			if l.VarianceBP != nil {
				variance = fmt.Sprintf("%d bp", *l.VarianceBP)
			}
			verdict := "ok"
			if !l.Within {
				verdict = "OUTSIDE TOLERANCE"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				l.Subject, l.MeteredUSD, l.ActualUSD, obsFormatUSD(l.DiffMicros), variance, verdict)
		}
		_ = tw.Flush()
		for _, l := range rep.Reconciliation.Lines {
			if l.Note != "" {
				fmt.Fprintf(w, "  note (%s): %s\n", l.Subject, l.Note)
			}
		}
	}

	for _, n := range rep.Notes {
		fmt.Fprintf(w, "\nnote: %s\n", n)
	}
	fmt.Fprintf(w, "\naudit record for this invocation: %s\n", rep.OwnAuditRecord)
}

// obsFormatQuantity prints a metered quantity without exponent notation.
//
// Quantities span seconds (tens), tokens (thousands) and bytes (hundreds of millions), and
// %v would render the last as 1.234e+08 — a figure nobody can compare against a provider's
// dashboard.
func obsFormatQuantity(q float64) string {
	if q == math.Trunc(q) && math.Abs(q) < 1<<53 {
		return fmt.Sprintf("%d", int64(q))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", q), "0"), ".")
}

func obsDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// obsPad right-aligns a money figure to a fixed width so a column of months lines up
// without a tabwriter for the one-line-per-month summary.
func obsPad(s string) string { return fmt.Sprintf("%12s", s) }
