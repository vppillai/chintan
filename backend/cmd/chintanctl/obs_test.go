package main

// Tests for the shared observability wiring: the money arithmetic §Phase 0's 5%
// reconciliation rests on, the fixture loader the CI tests read through, and openObs's
// fail-closed audit write (I13).
//
// §11.5 requires admin-script tests to run against the fake harness with no AWS
// credentials. Nothing here touches AWS: --fixtures seeds an in-memory repository, which is
// the same seam §Phase 0 requires the cross-tenant test to run against directly rather than
// only through the API.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Paths to the shared fixtures, relative to this package.
const (
	obsFixtureRecords     = "../../../scripts/test/fixtures/observability/records.json"
	obsFixtureOverCeiling = "../../../scripts/test/fixtures/observability/over-ceiling.json"
	obsDevConfig          = "../../../config/instances/dev.yaml"
)

func TestParseUSDIsExactAndRefusesAnythingThatIsNotABilledFigure(t *testing.T) {
	ok := map[string]int64{
		"0.40":      400_000,
		"1":         1_000_000,
		"$1.02":     1_020_000,
		"0.000111":  111,
		"0.4":       400_000,
		"-0.5":      -500_000,
		"12.345678": 12_345_678,
	}
	for in, want := range ok {
		got, err := obsParseUSD(in)
		if err != nil {
			t.Fatalf("obsParseUSD(%q): unexpected error %v", in, err)
		}
		if got != want {
			t.Errorf("obsParseUSD(%q) = %d micros, want %d", in, got, want)
		}
	}

	// 0.07 is the case that makes hand-parsing worth the lines: ParseFloat("0.07")*1e6 is
	// 69999.99999999999, so a bill entered as 0.07 would reconcile one micro away from a
	// metered 70000 for no reason present in the data.
	if got, _ := obsParseUSD("0.07"); got != 70_000 {
		t.Errorf("obsParseUSD(0.07) = %d, want 70000 exactly", got)
	}

	bad := []string{"", "abc", ".5", "1e-3", "1,02", "0.0000001", "1.", "0.12345678", "--1"}
	for _, in := range bad {
		if got, err := obsParseUSD(in); err == nil {
			t.Errorf("obsParseUSD(%q) = %d, want a refusal", in, got)
		}
	}
}

func TestFormatUSDShowsEveryMicro(t *testing.T) {
	cases := map[int64]string{
		0:         "0.000000",
		111:       "0.000111",
		1_050_000: "1.050000",
		-500_000:  "-0.500000",
		9_000_000: "9.000000",
	}
	for micros, want := range cases {
		if got := obsFormatUSD(micros); got != want {
			t.Errorf("obsFormatUSD(%d) = %q, want %q", micros, got, want)
		}
	}
}

// The boundary is the whole point: §Phase 0 says "within 5%", so 5.00% passes and 5.01%
// does not, and neither answer may depend on a display rounding.
func TestToleranceIsExactAtFivePercent(t *testing.T) {
	if !obsWithinTolerance(105, 100, 500) {
		t.Error("exactly 5% high should be within a 500bp tolerance")
	}
	if !obsWithinTolerance(95, 100, 500) {
		t.Error("exactly 5% low should be within a 500bp tolerance")
	}
	if obsWithinTolerance(10_501, 10_000, 500) {
		t.Error("5.01% high must be outside a 500bp tolerance")
	}
	// A variance whose rounded basis-point figure is 500 but whose true value is above it
	// must still fail, or the tolerance is decided by the formatter.
	if obsWithinTolerance(10_502, 10_000, 500) {
		t.Error("5.02% must be outside tolerance regardless of how it rounds for display")
	}
	// Billed nothing, metered something: undefined as a ratio, and not within tolerance.
	if obsWithinTolerance(1, 0, 500) {
		t.Error("metered spend against a zero bill must not read as within tolerance")
	}
	if !obsWithinTolerance(0, 0, 500) {
		t.Error("nothing metered and nothing billed is a match")
	}
	if _, ok := obsVarianceBP(1, 0); ok {
		t.Error("a variance against a zero actual must report as undefined, not as a number")
	}
	if bp, ok := obsVarianceBP(95, 100); !ok || bp != -500 {
		t.Errorf("obsVarianceBP(95,100) = %d,%v; want -500,true", bp, ok)
	}
}

func TestFixtureLoaderRefusesWhatWouldSilentlySeedNothing(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A mistyped top-level key would otherwise load zero records and let a test assert
	// against an empty store and pass.
	if err := obsLoadFixture(write("typo.json", `{"usages":[]}`), repository.NewMemory()); err == nil {
		t.Error("a fixture with an unknown field must be refused")
	}
	// I11: a record with no tenant has no key. keys refuses it; the loader must surface that
	// rather than skip the record.
	if err := obsLoadFixture(write("untenanted.json",
		`{"usage":[{"id":"01K0","unit":"stt_seconds","ts":"2026-07-01T00:00:00Z"}]}`),
		repository.NewMemory()); err == nil {
		t.Error("a usage record with no tenant must be refused (I11)")
	}
	if err := obsLoadFixture(write("untenanted-audit.json",
		`{"audit":[{"id":"01K0","actor":"user:u","action":"capture.read","result":"allowed","ts":"2026-07-01T00:00:00Z"}]}`),
		repository.NewMemory()); err == nil {
		t.Error("an audit record with no tenant must be refused (I11)")
	}
}

func TestTheHarnessFixturesLoad(t *testing.T) {
	for _, path := range []string{obsFixtureRecords, obsFixtureOverCeiling} {
		mem := repository.NewMemory()
		if err := obsLoadFixture(path, mem); err != nil {
			t.Fatalf("loading %s: %v", path, err)
		}
		if mem.Len() == 0 {
			// A fixture that loads nothing makes every test reading it pass vacuously,
			// which is the §0.5A failure mode arriving quietly.
			t.Fatalf("%s seeded no records", path)
		}
	}
}

// openObs must write the invocation's audit record before anything is read (I13), and it
// must know which record it wrote so audit.sh can exclude exactly that one.
func TestOpenObsRecordsTheInvocationAndKnowsItsOwnRecord(t *testing.T) {
	env, err := openObs(context.Background(), &obsFlags{
		tenant:   "t-alpha",
		fixtures: obsFixtureRecords,
		as:       "script:test",
	}, obsActionUsage, "usage-report:2026-07..2026-07")
	if err != nil {
		t.Fatalf("openObs: %v", err)
	}
	if env.ownAuditID == "" {
		t.Fatal("openObs did not report the id of the record it wrote")
	}
	if !strings.Contains(env.source, "fixtures:") {
		t.Errorf("source %q does not declare that these are fixtures", env.source)
	}

	auditor := audit.New(env.repo, env.clk, &obsIDs{}, obsLogger(), 0)
	page, err := auditor.Query(context.Background(), audit.Query{Tenant: env.tenant, Actor: "script:test"})
	if err != nil {
		t.Fatalf("querying back: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("want exactly one record for this invocation, got %d", len(page.Records))
	}
	got := page.Records[0]
	if got.ID != env.ownAuditID {
		t.Errorf("own record id %q does not match the record found (%q)", env.ownAuditID, got.ID)
	}
	if got.Action != obsActionUsage || got.Result != "allowed" {
		t.Errorf("record has action %q result %q", got.Action, got.Result)
	}
	if got.IP != "" || got.UA != "" {
		// audit.Access documents empty as "no address was available — a script invocation",
		// which is deliberately distinct from the "invalid" marker a bad claim gets.
		t.Errorf("a script invocation must claim no IP or UA, got ip=%q ua=%q", got.IP, got.UA)
	}
}

func TestOpenObsRefusesBeforeReadingAnything(t *testing.T) {
	cases := map[string]obsFlags{
		"no tenant (I11)":      {fixtures: obsFixtureRecords, as: "script:test"},
		"tenant with a '#'":    {tenant: "t#a", fixtures: obsFixtureRecords, as: "script:test"},
		"no config, no source": {tenant: "t-alpha", as: "script:test"},
		"missing fixture file": {tenant: "t-alpha", fixtures: "does-not-exist.json", as: "script:test"},
		// An email actor is refused by the audit package, and openObs's contract is that a
		// refused audit record means no read happens at all (I13, fail closed).
		"email-shaped actor (§9.2)": {tenant: "t-alpha", fixtures: obsFixtureRecords, as: "vpillai@example.com"},
	}
	for name, f := range cases {
		flags := f
		t.Run(name, func(t *testing.T) {
			if _, err := openObs(context.Background(), &flags, obsActionUsage, "usage-report:2026-07..2026-07"); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}
