package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// devConfig is the real instance config, not a fixture. auditTTLDays requires a
// config that passes the full validator, and a hand-written minimal one would either
// fail validation or become a second definition of what a valid config is (§7.4).
func devConfig() string { return filepath.Join("..", "..", "..", "config", "instances", "dev.yaml") }

const testEmail = "someone@example.com"

// goldenDigest pins subjectDigest's output for one address.
//
// A golden value rather than a re-implementation of the recipe, which would be
// tautological. This is the number that makes the recipe documented in
// scripts/users.sh --help checkable:
//
//	printf '%s' someone@example.com | sha256sum | cut -c1-16
//
// An operator who cannot reproduce a digest cannot find a user's records in the audit
// log, which is the only reason the digest exists (§9.2).
const goldenDigest = "72497f475e4f76d0"

func TestSubjectDigestIsStableAndCaseInsensitive(t *testing.T) {
	if got := subjectDigest(testEmail); got != goldenDigest {
		t.Fatalf("subjectDigest(%q) = %q, want %q — the recipe documented in users.sh --help no longer reproduces it", testEmail, got, goldenDigest)
	}
	// The pool declares UsernameAttributes: [email], and Cognito treats that username
	// case-insensitively — so two spellings are one user and must correlate to one
	// digest in the audit log. Whitespace is trimmed for the same reason: a trailing
	// space from a copy-paste is not a different user.
	for _, variant := range []string{"Someone@Example.com", "SOMEONE@EXAMPLE.COM", "  someone@example.com  "} {
		if got := subjectDigest(variant); got != goldenDigest {
			t.Errorf("subjectDigest(%q) = %q, want %q — spellings of one address must not split its audit history", variant, got, goldenDigest)
		}
	}
}

// TestUsersActionMappingRecordsNoMutationForAPlan is the mapping's load-bearing
// property, not merely its contents: a --dry-run invocation must never record a
// mutating action. §11.5 requires that dry-run output describe precisely what --apply
// does, and the audit log is output too — a plan that recorded "user.delete" would be
// a deletion in the seven-year access log that never happened, in the one store with
// no correction path (§6.3).
func TestUsersActionMappingRecordsNoMutationForAPlan(t *testing.T) {
	readOnly := map[string]bool{"user.read": true, "user.list": true}
	for op, modes := range usersAction {
		plan, ok := modes["plan"]
		if !ok {
			t.Errorf("operation %q has no plan mode; every operation is invocable as a dry run", op)
			continue
		}
		if !readOnly[plan] {
			t.Errorf("operation %q records %q for a plan; a dry run reads and changes nothing", op, plan)
		}
		if _, ok := modes["execute"]; !ok {
			t.Errorf("operation %q has no execute mode", op)
		}
	}
	// Every operation users.sh can invoke is in the table. A missing entry would make
	// the script fail at the audit step, after argument parsing and before any Cognito
	// call — recoverable, but only by reading this file.
	for _, op := range usersOperations {
		if _, ok := usersAction[op]; !ok {
			t.Errorf("operation %q is offered by --help but absent from usersAction", op)
		}
	}
	if len(usersOperations) != len(usersAction) {
		t.Errorf("--help offers %d operations but the mapping has %d", len(usersOperations), len(usersAction))
	}
}

func TestAuditItemActions(t *testing.T) {
	cases := []struct {
		operation, mode, wantAction, wantResource string
	}{
		{"add", "plan", "user.read", usersSubjectPrefix + goldenDigest},
		{"add", "execute", "user.create", usersSubjectPrefix + goldenDigest},
		{"resend", "execute", "user.invite_resend", usersSubjectPrefix + goldenDigest},
		{"remove", "plan", "user.read", usersSubjectPrefix + goldenDigest},
		{"remove", "execute", "user.delete", usersSubjectPrefix + goldenDigest},
		{"reset", "execute", "user.password_reset", usersSubjectPrefix + goldenDigest},
		{"list", "plan", "user.list", usersCollectionResource},
	}
	for _, c := range cases {
		req := auditItemRequest{
			tenant: "t-vp", operation: c.operation, mode: c.mode,
			cfgPath: devConfig(), actor: defaultUsersActor,
		}
		if c.operation != "list" {
			req.subject = testEmail
		}
		got, err := buildAuditItem(req)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.operation, c.mode, err)
		}
		if got.Action != c.wantAction {
			t.Errorf("%s/%s action = %q, want %q", c.operation, c.mode, got.Action, c.wantAction)
		}
		if got.Resource != c.wantResource {
			t.Errorf("%s/%s resource = %q, want %q", c.operation, c.mode, got.Resource, c.wantResource)
		}
		if got.Item["action"].(map[string]any)["S"] != c.wantAction {
			t.Errorf("%s/%s: the rendered item's action disagrees with the reported one; users.sh would print a plan that does not match the record it writes", c.operation, c.mode)
		}
	}
}

// TestNoEmailReachesTheRecord is the §9.2 assertion, made over the whole serialised
// output rather than over the resource field alone — because the leak that matters is
// the one in a field nobody thought to check.
func TestNoEmailReachesTheRecord(t *testing.T) {
	const email = "Distinctive.Local+tag@example.org"
	res, err := buildAuditItem(auditItemRequest{
		tenant: "t-vp", operation: "add", mode: "execute",
		subject: email, cfgPath: devConfig(), actor: defaultUsersActor,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	// The local part, the domain, and the '@' separately: a partially redacted address
	// is still PII, and "no '@' anywhere" alone would pass on output containing the
	// local part next to the domain.
	for _, fragment := range []string{"Distinctive.Local", "distinctive.local", "example.org", "@"} {
		if strings.Contains(out, fragment) {
			t.Errorf("rendered audit record contains %q; no part of an email address may enter the audit log, the longest-retained store in the system (§9.2)", fragment)
		}
	}
}

// TestRenderedItemMatchesAuditPackage is the drift guard.
//
// The renderer deliberately does not know the audit item's attribute set — it
// serialises whatever internal/audit stored. This asserts that coupling holds: the
// rendered attribute names must be exactly the audit package's own, plus the key and
// ttl attributes repository adds. If a future field is added to audit.Access, this
// fails here rather than producing users.sh records that are silently missing it.
func TestRenderedItemMatchesAuditPackage(t *testing.T) {
	const tenant = keys.TenantID("t-vp")

	// The same write, performed directly against the audit package.
	repo := repository.NewMemory()
	clk := clock.System{}
	aud := audit.New(repo, clk, ids.NewGenerator(clk), slog.New(slog.NewTextHandler(io.Discard, nil)), 2555)
	if err := aud.Allowed(context.Background(), audit.Access{
		Tenant: tenant, Actor: defaultUsersActor, Action: "user.create",
		Resource: usersSubjectPrefix + goldenDigest,
	}); err != nil {
		t.Fatal(err)
	}
	stored := repo.Keys()
	if len(stored) != 1 {
		t.Fatalf("audit package wrote %d items, want 1", len(stored))
	}
	canonical, err := repo.Get(context.Background(), stored[0])
	if err != nil {
		t.Fatal(err)
	}

	res, err := buildAuditItem(auditItemRequest{
		tenant: string(tenant), operation: "add", mode: "execute",
		subject: testEmail, cfgPath: devConfig(), actor: defaultUsersActor,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A set, because the audit package writes ttl both as Item.TTL and as a
	// descriptive attribute (§6.3), and repository refuses any disagreement between
	// the two — so one rendered "ttl" satisfies both.
	wantSet := map[string]bool{"PK": true, "SK": true, "ttl": true}
	for k := range canonical.Attrs {
		wantSet[k] = true
	}
	want := make([]string, 0, len(wantSet))
	for k := range wantSet {
		want = append(want, k)
	}
	got := make([]string, 0, len(res.Item))
	for k := range res.Item {
		got = append(got, k)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Fatalf("rendered attributes %v, audit package writes %v — the renderer has drifted from internal/audit and users.sh records are incomplete or malformed", got, want)
	}

	// The key must be the keys package's, not something assembled here (I11).
	wantKey, err := keys.Audit(tenant, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatal(err)
	}
	if res.Item["PK"].(map[string]any)["S"] != wantKey.PK {
		t.Errorf("PK = %v, want %q from keys.Audit", res.Item["PK"], wantKey.PK)
	}
	if !strings.HasPrefix(res.Item["SK"].(map[string]any)["S"].(string), strings.SplitAfter(wantKey.SK, "#")[0]) {
		t.Errorf("SK = %v, which does not carry the audit sort-key prefix from keys.Audit", res.Item["SK"])
	}
}

// TestAuditItemRefusals covers every way the subcommand must decline, because each of
// them is a case where proceeding would either write an untenanted key (I11) or create
// a Cognito user nobody can sign in as.
func TestAuditItemRefusals(t *testing.T) {
	base := auditItemRequest{
		tenant: "t-vp", operation: "add", mode: "execute",
		subject: testEmail, cfgPath: devConfig(), actor: defaultUsersActor,
	}
	mutate := func(f func(*auditItemRequest)) auditItemRequest {
		r := base
		f(&r)
		return r
	}
	cases := []struct {
		name  string
		req   auditItemRequest
		usage bool
	}{
		{"no tenant (I11)", mutate(func(r *auditItemRequest) { r.tenant = "" }), true},
		{"unknown operation", mutate(func(r *auditItemRequest) { r.operation = "promote" }), true},
		{"unknown mode", mutate(func(r *auditItemRequest) { r.mode = "maybe" }), true},
		{"no subject", mutate(func(r *auditItemRequest) { r.subject = "" }), true},
		{"subject with no @", mutate(func(r *auditItemRequest) { r.subject = "someone" }), true},
		{"subject with two @", mutate(func(r *auditItemRequest) { r.subject = "a@b@example.com" }), true},
		{"dotless domain", mutate(func(r *auditItemRequest) { r.subject = "someone@localhost" }), true},
		{"empty local part", mutate(func(r *auditItemRequest) { r.subject = "@example.com" }), true},
		{"subject with a space", mutate(func(r *auditItemRequest) { r.subject = "some one@example.com" }), true},
		{"subject with a newline", mutate(func(r *auditItemRequest) { r.subject = "someone@example.com\nx" }), true},
		{"over-long subject", mutate(func(r *auditItemRequest) { r.subject = strings.Repeat("a", 250) + "@example.com" }), true},
		{"subject passed to list", mutate(func(r *auditItemRequest) { r.operation = "list" }), true},
		{"no config (§7.4)", mutate(func(r *auditItemRequest) { r.cfgPath = "" }), true},
		{"missing config file", mutate(func(r *auditItemRequest) { r.cfgPath = filepath.Join("testdata", "absent.yaml") }), false},
		// An actor containing '@' is refused by internal/audit itself. Asserted here so
		// that a future caller passing an operator's email as --actor fails, rather than
		// discovering it seven years later in a subject-access request (§9.2).
		{"email-shaped actor", mutate(func(r *auditItemRequest) { r.actor = "someone@example.com" }), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildAuditItem(c.req)
			if err == nil {
				t.Fatal("expected a refusal, got none")
			}
			if isUsersUsageError(err) != c.usage {
				t.Errorf("usage-error classification = %v, want %v — the exit code would be wrong (2 for a wrong invocation, 1 for a failure)", isUsersUsageError(err), c.usage)
			}
		})
	}
}

// TestRefusalNeverQuotesTheSubject follows audit.validationError's rule: a message
// about a rejected value describes it and never quotes it. This message reaches
// stderr, and stderr on a CI step is a log (§9.2).
func TestRefusalNeverQuotesTheSubject(t *testing.T) {
	const bad = "not-an-address-but-distinctive"
	_, err := buildAuditItem(auditItemRequest{
		tenant: "t-vp", operation: "add", mode: "execute",
		subject: bad, cfgPath: devConfig(), actor: defaultUsersActor,
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), bad) {
		t.Errorf("refusal quotes the rejected subject: %q", err.Error())
	}
}

// TestTTLDaysComesFromConfig proves the retention window is read rather than
// defaulted. A users.sh record retained for a different window than every handler's
// record in the same table is the drift §7.4 forbids.
func TestTTLDaysComesFromConfig(t *testing.T) {
	res, err := buildAuditItem(auditItemRequest{
		tenant: "t-vp", operation: "list", mode: "plan",
		cfgPath: devConfig(), actor: defaultUsersActor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TTLDays <= 0 {
		t.Fatalf("ttl_days = %d, want retention.audit_days from %s", res.TTLDays, devConfig())
	}
	if res.Item["ttl"] == nil {
		t.Error("rendered item carries no ttl attribute; an audit record with no expiry is retained forever (§6.3)")
	}
}

func TestUsersUnknownSubcommandIsAUsageError(t *testing.T) {
	if code := runUsers([]string{"delete-everything"}); code != 2 {
		t.Errorf("exit code = %d, want 2 for an unknown subcommand", code)
	}
	if code := runUsers(nil); code != 2 {
		t.Errorf("exit code = %d, want 2 for no subcommand", code)
	}
	if code := runUsers([]string{"--help"}); code != 0 {
		t.Errorf("--help exit code = %d, want 0 (§11.3)", code)
	}
}
