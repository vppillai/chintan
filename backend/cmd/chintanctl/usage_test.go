package main

// Tests for the cost report (§11.4, §10.7, §Phase 0's 5% reconciliation).
//
// The three that matter most, in order: the report is tenant-scoped (I11), an unreadable
// record fails rather than counting as free (G-074), and the pricing cross-check applies the
// per-request billing minimum (G-013). Each of those failures produces a total that is wrong
// in the plausible direction, which is the direction nobody checks.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// obsTestEnv builds an obsEnv over a seeded in-memory repository, bypassing openObs so a
// test can choose the tenant and the records without an audit write in the way.
func obsTestEnv(t *testing.T, tenant string, fixture string, withConfig bool) *obsEnv {
	t.Helper()
	mem := repository.NewMemory()
	if fixture != "" {
		if err := obsLoadFixture(fixture, mem); err != nil {
			t.Fatalf("loading %s: %v", fixture, err)
		}
	}
	env := &obsEnv{
		tenant:     keys.TenantID(tenant),
		repo:       mem,
		clk:        clock.Fixed{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)},
		source:     "fixtures:" + fixture,
		ownAuditID: "test-own-record",
	}
	if withConfig {
		cfg, err := config.Load(obsDevConfig)
		if err != nil {
			t.Fatalf("loading %s: %v", obsDevConfig, err)
		}
		env.cfg = cfg
	}
	return env
}

func TestUsageWindowResolutionAndRefusals(t *testing.T) {
	clk := clock.Fixed{T: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)}

	from, to, err := usageWindow("", "", "", clk)
	if err != nil || from != "2026-08" || to != "2026-08" {
		t.Fatalf("default window = %q..%q, %v; want the current month", from, to, err)
	}
	if from, to, err := usageWindow("2026-07", "", "", clk); err != nil || from != "2026-07" || to != "2026-07" {
		t.Errorf("--month window = %q..%q, %v", from, to, err)
	}
	if from, to, err := usageWindow("", "2026-06", "", clk); err != nil || from != "2026-06" || to != "2026-06" {
		t.Errorf("--since alone = %q..%q, %v", from, to, err)
	}

	// Each of these would otherwise build a key prefix matching no record and report a
	// confident 0.00 — the silent-empty failure meter.DayTotal's parse exists to prevent.
	bad := []struct{ month, since, until string }{
		{"2026-07", "2026-06", ""}, // both forms
		{"", "2026-13", ""},        // month 13
		{"", "2026-7", ""},         // unpadded
		{"", "2026-08", "2026-07"}, // reversed
		{"", "not-a-month", ""},    // nonsense
	}
	for _, c := range bad {
		if _, _, err := usageWindow(c.month, c.since, c.until, clk); err == nil {
			t.Errorf("usageWindow(%q,%q,%q) was accepted", c.month, c.since, c.until)
		}
	}
}

func TestUsageMonthsRefusesAWindowPastRetention(t *testing.T) {
	got, err := usageMonths("2026-06", "2026-08", 25)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "2026-06,2026-07,2026-08" {
		t.Errorf("month expansion = %v", got)
	}
	// Records past retention.usage_months are gone (§6.3), so their months would report as
	// zero spend rather than as absent data.
	if _, err := usageMonths("2020-01", "2026-08", 25); err == nil {
		t.Error("a window longer than the retention period must be refused")
	}
}

// I11, at the data layer rather than through the API — which is what §Phase 0 requires.
// t-beta's single record is $9.00, past the §10.7 ceiling, so a leak would also change
// t-alpha's budget status and exit code rather than hiding in a total.
func TestUsageReportIsTenantScoped(t *testing.T) {
	ctx := context.Background()

	alpha, err := buildUsageReport(ctx, obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-08", nil, nil, 500)
	if err != nil {
		t.Fatalf("t-alpha report: %v", err)
	}
	// 311+311+111+8400+3000+1200+64+100000 for July, 622+40 for August.
	const wantAlpha = 113_397 + 662
	if alpha.CostMicros != wantAlpha {
		t.Errorf("t-alpha total = %d micros (%s USD), want %d", alpha.CostMicros, alpha.CostUSD, wantAlpha)
	}
	if alpha.Budget != usageWithinTarget {
		t.Errorf("t-alpha budget = %q, want %q", alpha.Budget, usageWithinTarget)
	}
	if usageExitCode(alpha) != obsExitOK {
		t.Errorf("t-alpha exit code = %d, want 0", usageExitCode(alpha))
	}
	for _, m := range alpha.Months {
		for _, r := range m.Rows {
			if r.CostMicros >= 9_000_000 {
				t.Fatalf("a t-beta record reached t-alpha's report: %+v", r)
			}
		}
	}

	beta, err := buildUsageReport(ctx, obsTestEnv(t, "t-beta", obsFixtureRecords, true), "2026-07", "2026-08", nil, nil, 500)
	if err != nil {
		t.Fatalf("t-beta report: %v", err)
	}
	if beta.CostMicros != 9_000_000 {
		t.Errorf("t-beta total = %d micros, want 9000000 — its own records and only its own", beta.CostMicros)
	}
	if beta.Budget != usageAboveCeiling || usageExitCode(beta) != obsExitBudget {
		t.Errorf("t-beta budget = %q exit = %d; $9.00 is past the §10.7 ceiling", beta.Budget, usageExitCode(beta))
	}
}

// Shadow-mode spend must stay visible: §7.2 says running it doubles STT cost, and a report
// that folded it into the active provider's line would hide the doubling.
func TestShadowSpendIsItsOwnRow(t *testing.T) {
	rep, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-07", nil, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	var shadow *usageRow
	for i := range rep.Months[0].Rows {
		if strings.HasSuffix(rep.Months[0].Rows[i].Op, ".shadow") {
			shadow = &rep.Months[0].Rows[i]
		}
	}
	if shadow == nil {
		t.Fatal("no shadow row in the report")
	}
	if shadow.CostMicros != 8400 || shadow.Provider == "groq_whisper_turbo" {
		t.Errorf("shadow row = %+v; it must carry its own provider and its own cost", *shadow)
	}
}

// G-074: a record read through a direct type assertion reads as zero against DynamoDB while
// passing against the fake. A record this reader cannot understand must fail the report, not
// contribute nothing — an under-count here is a plausible-looking bill and, through the same
// records, headroom the §10.5.9 cap cannot see.
func TestAnUnreadableUsageRecordFailsTheReportRatherThanCountingAsFree(t *testing.T) {
	cases := map[string]map[string]any{
		"cost_micros absent":     {"unit": "stt_seconds", "provider": "groq_whisper_turbo", "quantity": 1.0, "ts": "2026-07-01T00:00:00Z"},
		"cost_micros fractional": {"unit": "stt_seconds", "provider": "groq_whisper_turbo", "cost_micros": 1.5, "quantity": 1.0, "ts": "2026-07-01T00:00:00Z"},
		"unit absent":            {"provider": "groq_whisper_turbo", "cost_micros": int64(10), "quantity": 1.0, "ts": "2026-07-01T00:00:00Z"},
		"provider absent":        {"unit": "stt_seconds", "cost_micros": int64(10), "quantity": 1.0, "ts": "2026-07-01T00:00:00Z"},
		"quantity unreadable":    {"unit": "stt_seconds", "provider": "g", "cost_micros": int64(10), "quantity": "twelve", "ts": "2026-07-01T00:00:00Z"},
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			mem := repository.NewMemory()
			key, err := keys.Usage("t-alpha", "2026-07", "stt_seconds", "01K0AAAAAAAAAAAAAAAAAAAAAZ")
			if err != nil {
				t.Fatal(err)
			}
			if err := mem.PutOnce(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
				t.Fatal(err)
			}
			if _, err := readUsageMonth(context.Background(), mem, "t-alpha", "2026-07"); err == nil {
				t.Fatal("expected a refusal, got a report that would have counted this record as free")
			}
		})
	}
}

// G-013: Groq bills a 10-second minimum per request. The three July STT records are 28s, 28s
// and 4s, so the expected cost is derived from 28+28+10 = 66 billed seconds (733 micros) and
// not from 60 metered seconds (667 micros). A check that summed the seconds first would
// report a false 10% variance on a correctly-metered month — and would go quiet on a real
// one, because the error is proportional to how many short requests there were.
func TestPricingCheckAppliesThePerRequestBillingMinimum(t *testing.T) {
	rep, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-07", nil, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	var groq *usagePricingCheck
	for i := range rep.Months[0].Pricing {
		if rep.Months[0].Pricing[i].Provider == "groq_whisper_turbo" {
			groq = &rep.Months[0].Pricing[i]
		}
	}
	if groq == nil {
		t.Fatal("no pricing check for groq_whisper_turbo; config/instances/dev.yaml prices it")
	}
	if groq.ExpectedMicros != 733 {
		t.Errorf("expected cost = %d micros, want 733 (28+28+10 billed seconds at 0.04 USD/h); 667 means the 10-second minimum was applied to the total instead of per request (G-013)", groq.ExpectedMicros)
	}
	if groq.RecordedMicros != 733 || !groq.WithinTolerance {
		t.Errorf("recorded %d micros, within=%v; the fixture is metered exactly", groq.RecordedMicros, groq.WithinTolerance)
	}

	// A provider with no configured price gets no check rather than a check against zero,
	// which would report every real cost as infinitely over.
	for _, p := range rep.Months[0].Pricing {
		if p.Provider == "minimax_m3" {
			t.Error("an LLM provider has no cost_per_hour_usd in config and must not get an STT pricing check")
		}
	}
	// And with no config there is no pricing basis at all — reported as a note, not silence.
	noCfg, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", obsFixtureRecords, false), "2026-07", "2026-07", nil, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(noCfg.Months[0].Pricing) != 0 {
		t.Error("no config means no pricing basis; a check without one would be arithmetic on a guess")
	}
	if !obsNotesMention(noCfg.Notes, "pricing cross-check") {
		t.Errorf("notes do not say why the pricing check is absent: %v", noCfg.Notes)
	}
}

func TestReconciliationAnswersTheFivePercentQuestion(t *testing.T) {
	ctx := context.Background()
	env := obsTestEnv(t, "t-alpha", obsFixtureRecords, true)

	// July metered spend for groq is 733 micros. A bill of 0.000740 is +0.95%: within.
	actuals := &actualFlag{}
	if err := actuals.Set("groq_whisper_turbo=0.000740"); err != nil {
		t.Fatal(err)
	}
	total := int64(113_400)
	rep, err := buildUsageReport(ctx, env, "2026-07", "2026-07", actuals, &total, 500)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reconciliation == nil || !rep.Reconciliation.Within {
		t.Fatalf("reconciliation = %+v; both lines are inside 5%%", rep.Reconciliation)
	}
	if usageExitCode(rep) != obsExitOK {
		t.Errorf("exit code = %d, want 0 when everything reconciles", usageExitCode(rep))
	}

	// A bill twice the metered figure is not within 5%, and the exit code has to say so or
	// nothing automated can act on it.
	off := &actualFlag{}
	if err := off.Set("groq_whisper_turbo=0.001466"); err != nil {
		t.Fatal(err)
	}
	rep, err = buildUsageReport(ctx, obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-07", off, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reconciliation.Within {
		t.Error("a 100% variance was reported as within tolerance")
	}
	if usageExitCode(rep) != obsExitVariance {
		t.Errorf("exit code = %d, want %d", usageExitCode(rep), obsExitVariance)
	}

	// The finding this exists for: billed spend with no metering records at all. That is an
	// unmetered provider path (I12) and therefore spend the daily cap cannot see (§10.5.9).
	unmetered := &actualFlag{}
	if err := unmetered.Set("some_new_provider=1.00"); err != nil {
		t.Fatal(err)
	}
	rep, err = buildUsageReport(ctx, obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-07", unmetered, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	line := rep.Reconciliation.Lines[0]
	if line.MeteredMicros != 0 || line.Within || !strings.Contains(line.Note, "unmetered") {
		t.Errorf("unmetered billed spend reported as %+v; it must be flagged, not averaged away", line)
	}
	// The variance is well defined here — the bill is the denominator, and metering
	// captured none of it, so -10000 bp is exactly the right figure. (The undefined
	// direction is the other one: a variance against a zero BILL, covered in obs_test.go.)
	if line.VarianceBP == nil || *line.VarianceBP != -10_000 {
		t.Errorf("variance = %v, want -10000 bp: none of a real bill was metered", line.VarianceBP)
	}
}

func TestActualFlagRefusesWhatCannotBeAnInvoiceLine(t *testing.T) {
	a := &actualFlag{}
	if err := a.Set("groq=0.40"); err != nil {
		t.Fatal(err)
	}
	// Last-wins on a duplicate would silently drop one invoice line.
	if err := a.Set("groq=0.50"); err == nil {
		t.Error("a repeated provider must be refused")
	}
	for _, in := range []string{"noequals", "=0.40", "groq=abc", "groq=-1.00"} {
		if err := a.Set(in); err == nil {
			t.Errorf("--actual %q was accepted", in)
		}
	}
}

// §10.7: above $5/month is "a design error, not a budget overrun". The exit code has to
// outrank a reconciliation variance, because the two call for different actions.
func TestBudgetBreachOutranksAVarianceInTheExitCode(t *testing.T) {
	actuals := &actualFlag{}
	if err := actuals.Set("groq_whisper_turbo=0.10"); err != nil {
		t.Fatal(err)
	}
	rep, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", obsFixtureOverCeiling, true), "2026-07", "2026-07", actuals, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Budget != usageAboveCeiling {
		t.Fatalf("budget = %q, want %q for a $6.00 month", rep.Budget, usageAboveCeiling)
	}
	if rep.Reconciliation.Within {
		t.Fatal("the variance in this fixture is also outside tolerance; the test needs both to be true")
	}
	if usageExitCode(rep) != obsExitBudget {
		t.Errorf("exit code = %d, want %d — the ceiling breach is the more serious finding", usageExitCode(rep), obsExitBudget)
	}
	if !obsNotesMention(rep.Notes, "design error") {
		t.Errorf("notes do not quote §10.7's instruction: %v", rep.Notes)
	}
}

// An empty report is the most misreadable output this command has: it looks identical
// whether nothing billable ran or something is spending without metering (I12).
func TestAnEmptyReportSaysWhatEmptyMeans(t *testing.T) {
	rep, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", "", true), "2026-07", "2026-07", nil, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Records != 0 || rep.CostMicros != 0 {
		t.Fatalf("expected an empty report, got %+v", rep)
	}
	if !obsNotesMention(rep.Notes, "spending without metering") {
		t.Errorf("an empty report must not read as zero spend: %v", rep.Notes)
	}
	if !obsNotesMention(rep.Notes, "5% reconciliation is not closed") {
		t.Errorf("a report with no billed figures must say the §Phase 0 check is still open: %v", rep.Notes)
	}
}

func TestHumanOutputCarriesTheFiguresAndTheAuditRecord(t *testing.T) {
	rep, err := buildUsageReport(context.Background(), obsTestEnv(t, "t-alpha", obsFixtureRecords, true), "2026-07", "2026-07", nil, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writeUsageHuman(&buf, rep)
	out := buf.String()
	for _, want := range []string{"t-alpha", "2026-07", "groq_whisper_turbo", "transcribe.shadow", "0.113397", "within_target", "test-own-record"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output is missing %q:\n%s", want, out)
		}
	}
	// Byte quantities must not render as 3.145728e+08 — a figure nobody can compare against
	// a provider's dashboard.
	if strings.Contains(out, "e+") {
		t.Errorf("a quantity rendered in exponent notation:\n%s", out)
	}
}

func obsNotesMention(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}
