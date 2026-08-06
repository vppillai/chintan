package main

// Tests for the audit-log query (§11.4).
//
// The interesting behaviour is the recursion: this command appends to the log it reads. So
// the assertions are that the invocation's own record exists (I13), that exactly one record
// is excluded from the answer, that a --limit still returns that many records afterwards,
// and that a prior invocation's record is NOT hidden — reads of the log are accesses worth
// seeing.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
)

// obsAuditEnv seeds the shared fixture and writes an invocation record the way openObs does,
// returning an env whose ownAuditID is that record's key.
func obsAuditEnv(t *testing.T, tenant string) *obsEnv {
	t.Helper()
	env := obsTestEnv(t, tenant, obsFixtureRecords, false)
	gen := &obsIDs{inner: ids.NewGenerator(env.clk)}
	auditor := audit.New(env.repo, env.clk, gen, obsLogger(), 0)
	if err := auditor.Allowed(context.Background(), audit.Access{
		Tenant:   keys.TenantID(tenant),
		Actor:    "script:audit.sh",
		Action:   obsActionAudit,
		Resource: "audit-log",
	}); err != nil {
		t.Fatalf("writing the invocation record: %v", err)
	}
	key, err := keys.Audit(keys.TenantID(tenant), gen.last)
	if err != nil {
		t.Fatal(err)
	}
	env.ownAuditID = key.SK
	return env
}

func TestAuditQueryExcludesExactlyThisInvocationsOwnRecord(t *testing.T) {
	env := obsAuditEnv(t, "t-alpha")
	rep, err := runAuditQuery(context.Background(), env, audit.Query{Tenant: env.tenant, Newest: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OwnExcluded {
		t.Error("this invocation's own record was left in the answer; --limit 1 would then answer with nothing but itself")
	}
	for _, r := range rep.Records {
		if r.ID == env.ownAuditID {
			t.Fatal("the excluded record is still present")
		}
	}
	// The fixture holds four t-alpha records, one of which is a PREVIOUS usage.report
	// invocation. That one must survive: a query that hid every tool-written record would
	// hide the fact that the log had been read.
	if rep.Count != 4 {
		t.Fatalf("count = %d, want the fixture's four t-alpha records", rep.Count)
	}
	found := false
	for _, r := range rep.Records {
		if r.Action == obsActionUsage {
			found = true
		}
	}
	if !found {
		t.Error("a previous invocation's own record was hidden; only THIS invocation's is excluded")
	}
}

// Without raising the limit before the query, the self-exclusion silently eats one row of
// every page — and at --limit 1 the whole answer.
func TestLimitStillReturnsThatManyRecordsAfterTheSelfExclusion(t *testing.T) {
	env := obsAuditEnv(t, "t-alpha")
	rep, err := runAuditQuery(context.Background(), env, audit.Query{Tenant: env.tenant, Newest: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count != 1 {
		t.Fatalf("count = %d, want 1", rep.Count)
	}
	if !rep.Truncated {
		// "Exactly one match" and "one of five" are different compliance answers.
		t.Error("a cut list must be reported as truncated")
	}
	if rep.Records[0].ID == env.ownAuditID {
		t.Error("the single returned record is the query's own record")
	}
}

// I11 at the data layer: t-beta's record must be unreachable from t-alpha's query, whatever
// the filters say.
func TestAuditQueryIsTenantScoped(t *testing.T) {
	env := obsAuditEnv(t, "t-alpha")
	rep, err := runAuditQuery(context.Background(), env, audit.Query{Tenant: env.tenant}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Records {
		if strings.Contains(r.Actor, "beta") {
			t.Fatalf("a t-beta record reached t-alpha's query: %+v", r)
		}
	}
	beta := obsAuditEnv(t, "t-beta")
	rep, err = runAuditQuery(context.Background(), beta, audit.Query{Tenant: beta.tenant}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count != 1 || rep.Records[0].Actor != "user:u-beta" {
		t.Fatalf("t-beta sees %d record(s): %+v", rep.Count, rep.Records)
	}
}

func TestAuditFiltersSelectAndRefuse(t *testing.T) {
	env := obsAuditEnv(t, "t-alpha")
	ctx := context.Background()

	denied, err := runAuditQuery(ctx, env, audit.Query{Tenant: env.tenant, Result: "denied"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Count != 1 || denied.Records[0].Actor != "user:u-intruder" {
		t.Fatalf("result filter returned %+v", denied.Records)
	}

	window, err := runAuditQuery(ctx, env, audit.Query{Tenant: env.tenant, From: "2026-07-01", To: "2026-07-03"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The upper bound is exclusive, so the 2026-07-03 denial is out and the two July 2nd
	// records are in — this is what makes consecutive windows tile without double-counting.
	if window.Count != 2 {
		t.Fatalf("window 07-01..07-03 returned %d records, want 2 (exclusive upper bound)", window.Count)
	}

	// A malformed bound and a nonsense result filter are refused rather than answered with
	// an empty log, and they are invocation errors (exit 2), not tool failures.
	for _, q := range []audit.Query{
		{Tenant: env.tenant, From: "2026-31-08"},
		{Tenant: env.tenant, To: "2026-02-30"},
		{Tenant: env.tenant, Result: "allow"},
	} {
		_, err := runAuditQuery(ctx, env, q, 0)
		if err == nil {
			t.Errorf("query %+v was accepted; an empty log is the wrong answer to a typo", q)
			continue
		}
		if !errors.Is(err, errObsUsage) {
			t.Errorf("query %+v failed with %v, which would exit 1 rather than 2", q, err)
		}
	}
}

func TestAuditHumanOutputStatesTheEmptyCaseAndTheSelfExclusion(t *testing.T) {
	env := obsAuditEnv(t, "t-alpha")
	rep, err := runAuditQuery(context.Background(), env, audit.Query{Tenant: env.tenant, Actor: "user:nobody"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rep.Filters = auditFilters("user:nobody", "", "", "", "", "", 0, false)
	var buf bytes.Buffer
	writeAuditHuman(&buf, rep)
	out := buf.String()
	// The filters are printed next to the empty result, so a mistyped actor is visible
	// rather than reading as a clean log.
	for _, want := range []string{"no records matched", "actor=user:nobody", env.ownAuditID} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	full, err := runAuditQuery(context.Background(), env, audit.Query{Tenant: env.tenant, Newest: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	writeAuditHuman(&buf, full)
	if !strings.Contains(buf.String(), "TRUNCATED") {
		t.Errorf("a truncated answer must say so:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "excluded from the rows above") {
		t.Errorf("the self-exclusion must be disclosed:\n%s", buf.String())
	}
}

func TestAuditFilterLineIsOrderedSoTwoRunsDiffCleanly(t *testing.T) {
	got := auditFilterLine(auditFilters("user:u", "capture.read", "capture/1", "allowed", "2026-08-01", "2026-09-01", 50, true))
	want := "actor=user:u action=capture.read resource=capture/1 result=allowed since=2026-08-01 until=2026-09-01 limit=50 order=oldest-first"
	if got != want {
		t.Errorf("filter line =\n  %s\nwant\n  %s", got, want)
	}
}
