package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The negative cases are the ones that matter here. An emitter that writes a good
// record for good input but also writes something for an unscoped tenant, an
// unattributed actor, or a resource holding a transcript body is an emitter that
// produces a log nobody can rely on — and audit records have no delete path (§6.3), so
// a bad one is permanent.

// seqIDs issues zero-padded sequential identifiers.
//
// Padded to ULID width so the sort keys order the same way real ULIDs do: the
// chronological ordering Query depends on is a property of the key, and a fake that
// ordered differently would let an ordering bug pass.
type seqIDs struct{ n int }

func (s *seqIDs) NewID() string {
	s.n++
	return fmt.Sprintf("%026d", s.n)
}

// fixedIDs always returns the same identifier, which is how the write-once behaviour
// becomes observable without waiting for a real ULID collision.
type fixedIDs struct{ id string }

func (f fixedIDs) NewID() string { return f.id }

const testTenant = keys.TenantID("t_test")

var baseTime = time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)

// capturingLogger returns a logger writing JSON to a buffer at debug level, so both the
// presence and the level of a line can be asserted.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(new(bytes.Buffer), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func goodAccess() Access {
	return Access{
		Tenant:   testTenant,
		Actor:    "user:01HQ8ZK0000000000000000000",
		Action:   "capture.read",
		Resource: "c_01HQ8ZK0000000000000000001",
		IP:       "203.0.113.7",
		UA:       "Mozilla/5.0",
		Result:   model.AuditAllowed,
	}
}

// auditKey builds the key a record is expected to land under. Built through the keys
// package rather than written out: no key-prefix literal may appear outside
// internal/keys (I11, check-tenant-keys.sh), and a test that hardcoded the prefix would
// also stop verifying that the emitter and the key helper agree.
func auditKey(t *testing.T, tenant keys.TenantID, id string) keys.DynamoKey {
	t.Helper()
	k, err := keys.Audit(tenant, id)
	if err != nil {
		t.Fatalf("keys.Audit: %v", err)
	}
	return k
}

func TestRecordWritesOneTenantScopedRecord(t *testing.T) {
	repo := repository.NewMemory()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)

	if err := a.Allowed(context.Background(), goodAccess()); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if repo.Len() != 1 {
		t.Fatalf("stored %d items, want exactly 1", repo.Len())
	}

	key := auditKey(t, testTenant, "00000000000000000000000001")
	it, err := repo.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("record is not under the key keys.Audit builds: %v", err)
	}

	want := map[string]any{
		"actor":    "user:01HQ8ZK0000000000000000000",
		"action":   "capture.read",
		"resource": "c_01HQ8ZK0000000000000000001",
		"ip":       "203.0.113.7",
		"ua":       "Mozilla/5.0",
		"result":   model.AuditAllowed,
		"ts":       "2026-08-04T10:00:00Z",
	}
	for k, v := range want {
		if got := it.Attrs[k]; got != v {
			t.Errorf("attr %q = %v, want %v", k, got, v)
		}
	}
	// §6.3 lists exactly these attributes for the Audit entity, plus ttl. An extra one
	// is a record whose shape varies by call site, which audit.sh cannot query.
	if len(it.Attrs) != len(want)+1 {
		t.Errorf("record carries %d attributes, want %d (§6.3's field set plus ttl): %v",
			len(it.Attrs), len(want)+1, it.Attrs)
	}
}

func TestRecordDoesNotProjectIntoGSI1(t *testing.T) {
	// §6.3: audit is high-volume and "must never project into" the sparse index, or the
	// index becomes a second copy of the table — paid for on every write at on-demand
	// pricing.
	repo := repository.NewMemory()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)
	if err := a.Allowed(context.Background(), goodAccess()); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	it, err := repo.Get(context.Background(), auditKey(t, testTenant, "00000000000000000000000001"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if it.GSI1PK != "" || it.GSI1SK != "" {
		t.Errorf("audit record carries GSI1 attributes (%q/%q); it must not project into the sparse index (§6.3)", it.GSI1PK, it.GSI1SK)
	}
}

func TestTTLComesFromRetentionAuditDays(t *testing.T) {
	cases := map[string]struct {
		ttlDays  int
		wantDays int
	}{
		"configured value is used":                                     {ttlDays: 2555, wantDays: 2555},
		"a shorter window is honoured, so a test instance can use one": {ttlDays: 30, wantDays: 30},
		// A zero or negative window would mean an audit record that expires
		// immediately, i.e. an audit log that is empty exactly when it is needed. The
		// config schema requires audit_days > 0, so this only fires on a caller that
		// bypassed config — mirroring meter's treatment of ttlMonths.
		"zero falls back to the documented 7-year default":     {ttlDays: 0, wantDays: auditTTLDaysDefault},
		"negative falls back rather than expiring immediately": {ttlDays: -1, wantDays: auditTTLDaysDefault},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), tc.ttlDays)
			if err := a.Allowed(context.Background(), goodAccess()); err != nil {
				t.Fatalf("Allowed: %v", err)
			}
			it, err := repo.Get(context.Background(), auditKey(t, testTenant, "00000000000000000000000001"))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			want := baseTime.AddDate(0, 0, tc.wantDays).Unix()
			if it.TTL != want {
				t.Errorf("item TTL = %d, want %d (%d days from the fixed clock)", it.TTL, want, tc.wantDays)
			}
			if it.Attrs["ttl"] != want {
				t.Errorf("ttl attribute = %v, want %d; DynamoDB expires on the attribute, not the field name", it.Attrs["ttl"], want)
			}
		})
	}
}

func TestRecordIsWriteOnce(t *testing.T) {
	// §6.3: "Audit and Usage items are write-once." A taken key must fail rather than
	// overwrite — an append-only log that can overwrite is not append-only, and the
	// destroyed record is unrecoverable.
	repo := repository.NewMemory()
	a := New(repo, clock.Fixed{T: baseTime}, fixedIDs{id: "00000000000000000000000042"}, discardLogger(), 2555)

	if err := a.Allowed(context.Background(), goodAccess()); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := a.Allowed(context.Background(), goodAccess())
	if err == nil {
		t.Fatal("second write to the same key succeeded; audit records are write-once (§6.3)")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("error is %v, want one wrapping repository.ErrAlreadyExists so a caller can distinguish an ID collision", err)
	}
	if repo.Len() != 1 {
		t.Errorf("stored %d items after a duplicate write, want 1", repo.Len())
	}
}

func TestPackageExposesNoMutationPath(t *testing.T) {
	// §6.3: "No update or delete path exists in application code." Enforced here rather
	// than by review, so that adding an Update or Delete method fails a test instead of
	// shipping. Only the tenant-erasure operation (§9.3) may remove an audit record, and
	// it does not live in this package.
	allowed := map[string]bool{"Record": true, "Allowed": true, "Denied": true, "Query": true}
	typ := reflect.TypeOf(&Auditor{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !allowed[name] {
			t.Errorf("Auditor exposes method %q; audit records are write-once and this package must offer no update or delete path (§6.3, I13)", name)
		}
	}
}

func TestNoWritePathExistsBesidesPutOnce(t *testing.T) {
	// The reflection check above is weaker than it reads: reflect.Type.NumMethod sees
	// only *exported* methods on *Auditor, so an unexported mutator, a package-level
	// func, or a method on some second type all pass it. §6.3 says no update or delete
	// path exists in application code, full stop — so the check that matters is over the
	// source: this package may call PutOnce and QueryPrefix on the repository and nothing
	// else. A Put would overwrite, and Delete or Update would end the append-only
	// property the whole store depends on.
	src, err := os.ReadFile("audit.go")
	if err != nil {
		t.Fatalf("reading the package source: %v", err)
	}
	// Method names on repository.Repository that would constitute a mutation path. Held
	// as a list rather than inferred, because the point is to fail on a *new* call.
	for _, forbidden := range []string{".Put(", ".Delete(", ".Update(", ".BatchWrite(", ".TransactWrite("} {
		if bytes.Contains(src, []byte(forbidden)) {
			t.Errorf("audit.go calls %s; audit records are write-once, and only repository.PutOnce may write one (§6.3, I13)", strings.Trim(forbidden, ".("))
		}
	}
	if !bytes.Contains(src, []byte(".PutOnce(")) {
		t.Error("audit.go no longer calls PutOnce; a Put that can overwrite destroys an existing record, which an append-only log may never do")
	}
}

func TestRecordRefusesUnusableInput(t *testing.T) {
	long := strings.Repeat("x", 600)
	cases := map[string]struct {
		mutate func(*Access)
		want   string
	}{
		"empty tenant": {
			// I11: an audit record in no tenant's partition is worse than none — it is
			// evidence that an unscoped path exists.
			mutate: func(ev *Access) { ev.Tenant = "" },
			want:   "tenant_id is empty",
		},
		"whitespace tenant": {
			mutate: func(ev *Access) { ev.Tenant = " " },
			want:   "tenant_id is empty",
		},
		"missing actor": {
			mutate: func(ev *Access) { ev.Actor = "" },
			want:   "actor is required",
		},
		"actor is an email address": {
			// PII in the longest-retained store in the system (§9.2).
			mutate: func(ev *Access) { ev.Actor = "user:someone@example.com" },
			want:   "PII",
		},
		"oversized actor": {
			mutate: func(ev *Access) { ev.Actor = long },
			want:   "over the 128-byte bound",
		},
		"actor with a control character": {
			mutate: func(ev *Access) { ev.Actor = "user:a\nuser:b" },
			want:   "control character",
		},
		"missing action": {
			mutate: func(ev *Access) { ev.Action = "" },
			want:   "is not an action name",
		},
		"action with capitals": {
			mutate: func(ev *Access) { ev.Action = "Capture.Read" },
			want:   "is not an action name",
		},
		"action holding a sentence": {
			// The content-smuggling case the shape restriction exists for: a phrase
			// this long could be transcript text, and it cannot reach the record.
			mutate: func(ev *Access) { ev.Action = "read the capture about the compression library" },
			want:   "is not an action name",
		},
		"missing resource": {
			mutate: func(ev *Access) { ev.Resource = "" },
			want:   "resource is required",
		},
		"oversized resource": {
			mutate: func(ev *Access) { ev.Resource = long },
			want:   "never its content",
		},
		"multi-line resource": {
			mutate: func(ev *Access) { ev.Resource = "c_1\nLZ4 decode-only looks like the practical choice" },
			want:   "control character",
		},
		"missing result": {
			mutate: func(ev *Access) { ev.Result = "" },
			want:   "neither",
		},
		"invented result": {
			// A third outcome would be silently invisible to audit.sh's result filter.
			mutate: func(ev *Access) { ev.Result = "ok" },
			want:   "neither",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)
			ev := goodAccess()
			tc.mutate(&ev)

			err := a.Record(context.Background(), ev)
			if err == nil {
				t.Fatalf("record accepted; expected refusal mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q; a record refused for the wrong reason leaves the real constraint unverified", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "audit: ") {
				t.Errorf("error %q is not prefixed with the package name", err)
			}
			if repo.Len() != 0 {
				t.Errorf("a refused record was still written (%d items stored)", repo.Len())
			}
		})
	}
}

// secret is a phrase that could only have come from the rejected value, so its presence
// anywhere in an error or a log line is a leak and nothing else.
const secret = "LZ4-decode-only-looks-like-the-practical-choice"

// transcriptBody is a mis-passed body: the shape of the call-site mistake each field
// bound exists to catch, at a size where quoting it is also a CloudWatch bill (§10.1).
var transcriptBody = strings.Repeat("the compression spike says "+secret+". ", 200)

func TestNoRejectedValueReachesTheErrorOrTheLog(t *testing.T) {
	// §9.2 covers error messages as well as log lines, because a returned error may end
	// up in an HTTP body or in a caller's own log. Every refusal in validate() is
	// exercised here, not just `resource`: when only `resource` was covered, the actor
	// and action messages quoted the rejected value with %q for months, so a Cognito
	// username and a 10KB transcript went to CloudWatch at WARN — written there by the
	// check whose entire job is keeping them out of the seven-year store.
	cases := map[string]struct {
		mutate    func(*Access)
		wantField string
		wantRule  string
		// leak is the phrase that must appear nowhere; empty means the package-level
		// secret. A case whose value must stay under a field bound carries its own.
		leak string
	}{
		"an email actor must not put the address in the error or the log": {
			mutate:    func(ev *Access) { ev.Actor = secret + "@example.com" },
			wantField: "actor",
			wantRule:  "email-shaped",
		},
		"an over-bound actor": {
			mutate:    func(ev *Access) { ev.Actor = "user:" + transcriptBody },
			wantField: "actor",
			wantRule:  "over-bound",
		},
		"an actor carrying a control character": {
			mutate:    func(ev *Access) { ev.Actor = "user:a\n" + secret },
			wantField: "actor",
			wantRule:  "control-character",
		},
		"an actor that is not valid UTF-8": {
			mutate:    func(ev *Access) { ev.Actor = "user:\xff\xfe" + secret },
			wantField: "actor",
			wantRule:  "invalid-utf8",
		},
		"a transcript passed where an action belongs": {
			// The reproduction: no length bound ran before the regexp, so the whole body
			// was quoted into a 10KB error and a 10KB WARN line.
			mutate:    func(ev *Access) { ev.Action = transcriptBody },
			wantField: "action",
			wantRule:  "over-bound",
		},
		"a short sentence where an action belongs is still content": {
			// Under actionMax, so the shape check refuses it — and must still not quote
			// it: a 30-byte phrase can be a thought just as much as a 10KB one.
			mutate:    func(ev *Access) { ev.Action = "read the note about lz4-decode" },
			wantField: "action",
			wantRule:  "shape",
			leak:      "lz4-decode",
		},
		"an over-bound resource": {
			mutate:    func(ev *Access) { ev.Resource = transcriptBody },
			wantField: "resource",
			wantRule:  "over-bound",
		},
		"a multi-line resource": {
			mutate:    func(ev *Access) { ev.Resource = "c_1\n" + secret },
			wantField: "resource",
			wantRule:  "control-character",
		},
		"a resource that is not valid UTF-8": {
			mutate:    func(ev *Access) { ev.Resource = "c_\xff\xfe1" + secret },
			wantField: "resource",
			wantRule:  "invalid-utf8",
		},
		"an invented result": {
			mutate:    func(ev *Access) { ev.Result = secret },
			wantField: "result",
			wantRule:  "not-enumerated",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			log, buf := capturingLogger()
			a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, log, 2555)

			ev := goodAccess()
			tc.mutate(&ev)
			leak := tc.leak
			if leak == "" {
				leak = secret
			}

			err := a.Record(context.Background(), ev)
			if err == nil {
				t.Fatal("the record was accepted")
			}
			if strings.Contains(err.Error(), leak) {
				t.Errorf("the returned error echoes the rejected value; a handler may put it in an HTTP body (§9.2): %q", err)
			}
			if strings.Contains(buf.String(), leak) {
				t.Errorf("the rejection log echoes the rejected value (§9.2): %s", buf.String())
			}
			// A message that describes rather than quotes cannot grow with the value. The
			// bound also pins the CloudWatch cost the DEBUG-for-allowed decision protects.
			if n := len(err.Error()); n > 400 {
				t.Errorf("the error is %d bytes; a message that describes a value rather than quoting it does not scale with it", n)
			}
			if n := len(buf.String()); n > 600 {
				t.Errorf("the rejection log line is %d bytes; ingestion is $0.50/GB (§10.1) and a rejection carries no content", n)
			}
			// The diagnosis has to survive the redaction, or the next engineer reads the
			// value out of production to find out which field was wrong.
			if !strings.Contains(buf.String(), `"field":"`+tc.wantField+`"`) {
				t.Errorf("the rejection log does not name the field %q: %s", tc.wantField, buf.String())
			}
			if !strings.Contains(buf.String(), `"rule":"`+tc.wantRule+`"`) {
				t.Errorf("the rejection log does not name the rule %q: %s", tc.wantRule, buf.String())
			}
			if !strings.Contains(buf.String(), "[redacted]") {
				t.Errorf("the rejection log does not record that a value was present; want a Redacted attribute: %s", buf.String())
			}
			if repo.Len() != 0 {
				t.Errorf("a refused record was still written (%d items)", repo.Len())
			}
		})
	}
}

func TestTheRejectionLogNeverCarriesTheErrorMessage(t *testing.T) {
	// The structural half of the fix. Keeping every message content-free is a promise
	// each future edit can break silently; a log line that does not format the message
	// at all cannot be broken that way. So assert the absence of the message, not just
	// the absence of today's value.
	repo := repository.NewMemory()
	log, buf := capturingLogger()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, log, 2555)

	ev := goodAccess()
	ev.Actor = "someone@example.com"

	err := a.Record(context.Background(), ev)
	if err == nil {
		t.Fatal("an email actor was accepted")
	}
	// "PII" appears only in the validation message, never in the structured attributes.
	if strings.Contains(buf.String(), "PII") {
		t.Errorf("the rejection log formats the validation message; it must carry field/rule/length only, so that a %%q added to a message later cannot leak: %s", buf.String())
	}
	if lvl := levelOf(t, buf, "audit record rejected"); lvl != "WARN" {
		t.Errorf("rejection logged at %q, want WARN", lvl)
	}
}

func TestValidationFailuresIdentifyTheFieldToTheCaller(t *testing.T) {
	// Describing a value instead of quoting it may not cost the caller the diagnosis: an
	// error that said only "invalid input" would send the next engineer to production to
	// read the value. Field name, byte length, and the rule are all content-free.
	repo := repository.NewMemory()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)

	ev := goodAccess()
	ev.Action = transcriptBody

	err := a.Record(context.Background(), ev)
	if err == nil {
		t.Fatal("a transcript body was accepted as an action")
	}
	for _, want := range []string{"action", fmt.Sprintf("%d bytes", len(transcriptBody)), "64-byte bound"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q; the field, its length, and the rule are the diagnosis", err, want)
		}
	}
}

func TestC1ControlCharactersAreTreatedAsControlCharacters(t *testing.T) {
	// U+0085 NEL is in the C1 range, which `r < 0x20 || r == 0x7f` let through. Some log
	// processors treat NEL as a line terminator, which is exactly the forged-log-entry
	// risk the newline handling exists for — so the narrow test defeated its own purpose.
	const nel = "\u0085"

	t.Run("a NEL in a resource is refused", func(t *testing.T) {
		repo := repository.NewMemory()
		a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)
		ev := goodAccess()
		ev.Resource = "c_1" + nel + "next line pretending to be a record"
		err := a.Record(context.Background(), ev)
		if err == nil {
			t.Fatal("a resource containing U+0085 NEL was accepted and stored")
		}
		if !strings.Contains(err.Error(), "control character") {
			t.Errorf("error %q does not name the control-character rule", err)
		}
		if repo.Len() != 0 {
			t.Errorf("the record was written anyway (%d items)", repo.Len())
		}
	})

	t.Run("a NEL in an actor is refused", func(t *testing.T) {
		repo := repository.NewMemory()
		a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)
		ev := goodAccess()
		ev.Actor = "user:a" + nel + "user:b"
		if err := a.Record(context.Background(), ev); err == nil {
			t.Fatal("an actor containing U+0085 NEL was accepted")
		}
	})

	t.Run("a NEL in a client user agent is stripped, not refused", func(t *testing.T) {
		// Client-supplied, so it is normalised: refusing would hand a client a way to
		// fail its own requests (see sanitize).
		if got := sanitizeUA("Mozilla" + nel + "5.0"); strings.Contains(got, nel) {
			t.Errorf("sanitizeUA left U+0085 NEL in %q", got)
		}
	})
}

func TestRecordReportsAWriteFailureRatherThanSwallowingIt(t *testing.T) {
	// I13 makes the record non-optional, so a failed write must reach the caller — the
	// caller's contract is to fail the request rather than serve content unrecorded. It
	// is also logged at ERROR, so a call site that mishandles the error still leaves a
	// trace of the gap.
	repo := repository.NewMemory()
	log, buf := capturingLogger()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, log, 2555)

	boom := errors.New("dynamodb unavailable")
	repo.FailNext(boom)

	err := a.Allowed(context.Background(), goodAccess())
	if err == nil {
		t.Fatal("a failed audit write returned nil; the caller would serve content with no record of it (I13)")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the storage failure with %%w", err)
	}
	if lvl := levelOf(t, buf, "audit write failed"); lvl != "ERROR" {
		t.Errorf("audit write failure logged at %q, want ERROR", lvl)
	}
}

func TestAnUnusableGeneratedIDIsLoggedAsWellAsReturned(t *testing.T) {
	// Same gap, same reasoning as the write-failure branch: an IDGenerator returning an
	// id keys refuses produces an access with no record of it, and a call site that
	// swallowed the error would leave no trace at all. Before this, that branch returned
	// the error and logged nothing.
	repo := repository.NewMemory()
	log, buf := capturingLogger()
	// '#' is the key-segment delimiter keys.identRe forbids — the plausible shape of a
	// future generator's mistake.
	a := New(repo, clock.Fixed{T: baseTime}, fixedIDs{id: "bad#id"}, log, 2555)

	err := a.Allowed(context.Background(), goodAccess())
	if err == nil {
		t.Fatal("an unusable generated id was accepted")
	}
	if !strings.HasPrefix(err.Error(), "audit: ") {
		t.Errorf("error %q is not package-prefixed", err)
	}
	if repo.Len() != 0 {
		t.Errorf("a record was written under an unusable key (%d items)", repo.Len())
	}
	if lvl := levelOf(t, buf, "audit key generation failed"); lvl != "ERROR" {
		t.Errorf("the audit gap was logged at %q, want ERROR", lvl)
	}
}

func TestDeniedIsRecordedAndWarned(t *testing.T) {
	// §9.1: "A cross-tenant access attempt is an audit event at WARN and returns 404,
	// not 403." Both halves live here: the record is written with result=denied, and the
	// line is WARN so it is visible without a query.
	repo := repository.NewMemory()
	log, buf := capturingLogger()
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, log, 2555)

	ev := goodAccess()
	ev.Result = "" // Denied must not need the caller to remember the constant.
	if err := a.Denied(context.Background(), ev); err != nil {
		t.Fatalf("Denied: %v", err)
	}
	it, err := repo.Get(context.Background(), auditKey(t, testTenant, "00000000000000000000000001"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if it.Attrs["result"] != model.AuditDenied {
		t.Errorf("result = %v, want %q", it.Attrs["result"], model.AuditDenied)
	}
	if lvl := levelOf(t, buf, "access denied"); lvl != "WARN" {
		t.Errorf("denial logged at %q, want WARN (§9.1)", lvl)
	}
}

func TestAllowedAccessIsNotLoggedAboveDebug(t *testing.T) {
	// The durable record is the DynamoDB item. One INFO line per content access would
	// make CloudWatch ingestion at $0.50/GB a top line item on a ~$5/month budget
	// (§10.1), so the allowed path logs at DEBUG only.
	repo := repository.NewMemory()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, log, 2555)

	if err := a.Allowed(context.Background(), goodAccess()); err != nil {
		t.Fatalf("Allowed: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("an allowed access emitted a log line at INFO or above: %s", buf.String())
	}
	if repo.Len() != 1 {
		t.Errorf("the record itself was not written (%d items)", repo.Len())
	}
}

func TestClientSuppliedFieldsAreSanitisedRatherThanRefused(t *testing.T) {
	// Refusing a record over a client-supplied header would hand any client a way to
	// fail its own requests, because Record's contract is that the caller aborts on
	// error. So these are bounded and normalised instead.
	cases := map[string]struct {
		ip, ua       string
		wantIP       string
		wantUALen    int
		wantUAAbsent string
	}{
		"a plain address and agent pass through": {
			ip: "203.0.113.7", ua: "Mozilla/5.0", wantIP: "203.0.113.7", wantUALen: len("Mozilla/5.0"),
		},
		"an absent address stays absent, distinguishable from a bad one": {
			ip: "", ua: "", wantIP: "", wantUALen: 0,
		},
		"an unresolved forwarded chain is marked, not stored as fact": {
			ip: "203.0.113.7, 198.51.100.4", ua: "x", wantIP: ipInvalid, wantUALen: 1,
		},
		"a non-address is marked": {
			ip: "not-an-ip", ua: "x", wantIP: ipInvalid, wantUALen: 1,
		},
		"an ipv6 address is canonicalised so one address is one value": {
			ip: "0:0:0:0:0:0:0:1", ua: "x", wantIP: "::1", wantUALen: 1,
		},
		"a header newline cannot forge a second log entry": {
			ip: "203.0.113.7", ua: "Mozilla\n\rfake", wantIP: "203.0.113.7",
			wantUALen: len("Mozillafake"), wantUAAbsent: "\n",
		},
		"an oversized agent is truncated, not refused": {
			ip: "203.0.113.7", ua: strings.Repeat("u", 4000), wantIP: "203.0.113.7", wantUALen: uaMax,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			a := New(repo, clock.Fixed{T: baseTime}, &seqIDs{}, discardLogger(), 2555)
			ev := goodAccess()
			ev.IP, ev.UA = tc.ip, tc.ua

			if err := a.Allowed(context.Background(), ev); err != nil {
				t.Fatalf("client input made the record fail, which would fail the user's request: %v", err)
			}
			it, err := repo.Get(context.Background(), auditKey(t, testTenant, "00000000000000000000000001"))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got := it.Attrs["ip"]; got != tc.wantIP {
				t.Errorf("ip = %v, want %q", got, tc.wantIP)
			}
			ua, _ := it.Attrs["ua"].(string)
			if len(ua) != tc.wantUALen {
				t.Errorf("ua is %d bytes (%q), want %d", len(ua), ua, tc.wantUALen)
			}
			if tc.wantUAAbsent != "" && strings.Contains(ua, tc.wantUAAbsent) {
				t.Errorf("ua still contains %q", tc.wantUAAbsent)
			}
		})
	}
}

// writeAt records one access at a chosen instant, sharing a repository across
// timestamps. A new Auditor per instant rather than a mutable clock, because
// clock.Fixed deliberately returns a new value from Advance instead of mutating.
func writeAt(t *testing.T, repo repository.Repository, ids IDGenerator, at time.Time, ev Access) {
	t.Helper()
	a := New(repo, clock.Fixed{T: at}, ids, discardLogger(), 2555)
	if err := a.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record at %s: %v", at, err)
	}
}

// seedLog writes four records across two days, two actors, and both outcomes.
func seedLog(t *testing.T) (*repository.Memory, *Auditor) {
	t.Helper()
	repo := repository.NewMemory()
	ids := &seqIDs{}

	mk := func(actor, action, resource, result string) Access {
		ev := goodAccess()
		ev.Actor, ev.Action, ev.Resource, ev.Result = actor, action, resource, result
		return ev
	}
	writeAt(t, repo, ids, baseTime, mk("user:a", "capture.read", "c_1", model.AuditAllowed))
	writeAt(t, repo, ids, baseTime.Add(time.Hour), mk("user:b", "capture.read", "c_2", model.AuditDenied))
	writeAt(t, repo, ids, baseTime.AddDate(0, 0, 1), mk("user:a", "item.update", "i_1", model.AuditAllowed))
	writeAt(t, repo, ids, baseTime.AddDate(0, 0, 2), mk("user:a", "capture.read", "c_1", model.AuditAllowed))

	// A second tenant's records must be invisible to the first tenant's query (I11).
	other := goodAccess()
	other.Tenant = "t_other"
	other.Resource = "c_other"
	writeAt(t, repo, ids, baseTime, other)

	return repo, New(repo, clock.Fixed{T: baseTime}, ids, discardLogger(), 2555)
}

func TestQuerySelectsTheRecordsAuditShNeeds(t *testing.T) {
	repo, a := seedLog(t)
	if repo.Len() != 5 {
		t.Fatalf("seeded %d records, want 5", repo.Len())
	}

	cases := map[string]struct {
		q             Query
		wantResources []string
		wantTruncated bool
		// descending is set where the result is newest-first, since the ascending
		// ordering assertion below does not apply then.
		descending bool
	}{
		"everything for one tenant, in chronological order": {
			q:             Query{Tenant: testTenant},
			wantResources: []string{"c_1", "c_2", "i_1", "c_1"},
		},
		"a date lower bound needs no date arithmetic in the caller": {
			q:             Query{Tenant: testTenant, From: "2026-08-05"},
			wantResources: []string{"i_1", "c_1"},
		},
		"the upper bound is exclusive, so consecutive windows tile": {
			q:             Query{Tenant: testTenant, From: "2026-08-04", To: "2026-08-05"},
			wantResources: []string{"c_1", "c_2"},
		},
		"a full timestamp bound narrows within a day": {
			q:             Query{Tenant: testTenant, From: "2026-08-04T10:30:00Z", To: "2026-08-05"},
			wantResources: []string{"c_2"},
		},
		"by actor": {
			q:             Query{Tenant: testTenant, Actor: "user:a"},
			wantResources: []string{"c_1", "i_1", "c_1"},
		},
		"by action": {
			q:             Query{Tenant: testTenant, Action: "item.update"},
			wantResources: []string{"i_1"},
		},
		"by resource, which is the 'who touched this capture' question": {
			q:             Query{Tenant: testTenant, Resource: "c_1"},
			wantResources: []string{"c_1", "c_1"},
		},
		"by outcome, for the denial review": {
			q:             Query{Tenant: testTenant, Result: model.AuditDenied},
			wantResources: []string{"c_2"},
		},
		"filters compose": {
			q:             Query{Tenant: testTenant, Actor: "user:a", Action: "capture.read", From: "2026-08-06"},
			wantResources: []string{"c_1"},
		},
		"limit counts matches, not records examined": {
			q:             Query{Tenant: testTenant, Actor: "user:a", Limit: 2},
			wantResources: []string{"c_1", "i_1"},
			// Three matches, two returned: an operator told "2 records" with no signal
			// would read a truncated answer as a complete one.
			wantTruncated: true,
		},
		"a limit that does not cut reports no truncation": {
			q:             Query{Tenant: testTenant, Actor: "user:a", Limit: 3},
			wantResources: []string{"c_1", "i_1", "c_1"},
		},
		"newest-first is the 'recent accesses to this resource' question (§11.4)": {
			q:             Query{Tenant: testTenant, Newest: true},
			wantResources: []string{"c_1", "i_1", "c_2", "c_1"},
			descending:    true,
		},
		"a limit under newest-first returns the most recent matches, not the oldest": {
			// The failure this exists for: ascending plus a limit shows the twenty oldest
			// accesses in a seven-year log and reads as a complete answer.
			q:             Query{Tenant: testTenant, Newest: true, Limit: 2},
			wantResources: []string{"c_1", "i_1"},
			wantTruncated: true,
			descending:    true,
		},
		"the other tenant sees only its own record (I11)": {
			q:             Query{Tenant: "t_other"},
			wantResources: []string{"c_other"},
		},
		"a window with no records is empty rather than an error": {
			q:             Query{Tenant: testTenant, From: "2027-01-01"},
			wantResources: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			page, err := a.Query(context.Background(), tc.q)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			got := page.Records
			var resources []string
			for _, r := range got {
				resources = append(resources, r.Resource)
			}
			if !reflect.DeepEqual(resources, tc.wantResources) {
				t.Errorf("resources = %v, want %v", resources, tc.wantResources)
			}
			if page.Truncated != tc.wantTruncated {
				t.Errorf("Truncated = %v, want %v; 'N matches' and 'N of many' are different compliance answers",
					page.Truncated, tc.wantTruncated)
			}
			// Chronological order is the property the ULID sort key exists for; without
			// it a time-range read over the audit log is not possible at all (§6.3).
			for i := 1; i < len(got); i++ {
				if tc.descending && got[i-1].TS < got[i].TS {
					t.Errorf("newest-first records are not in reverse order: %s before %s", got[i-1].TS, got[i].TS)
				}
				if !tc.descending && got[i-1].TS > got[i].TS {
					t.Errorf("records are not in chronological order: %s before %s", got[i-1].TS, got[i].TS)
				}
			}
			for _, r := range got {
				if r.ID == "" {
					t.Error("decoded record has no ID; an operator cannot quote it back")
				}
				if r.TTL == 0 {
					t.Error("decoded record has TTL 0, which reads as 'retention is not configured'")
				}
			}
		})
	}
}

func TestQueryRefusesAnUnusableQuery(t *testing.T) {
	_, a := seedLog(t)
	cases := map[string]struct {
		q    Query
		want string
	}{
		"no tenant": {
			// I11 applies to admin scripts too — audit.sh included.
			q:    Query{},
			want: "tenant_id is empty",
		},
		"a from bound in the wrong format": {
			q:    Query{Tenant: testTenant, From: "04/08/2026"},
			want: "must be yyyy-mm-dd",
		},
		"a from bound carrying a local offset": {
			// String comparison against a stored UTC timestamp would select the wrong
			// window, silently — the same reason keys.GSI1 refuses a non-UTC value.
			q:    Query{Tenant: testTenant, From: "2026-08-04T10:00:00+05:30"},
			want: "RFC3339 UTC",
		},
		"a to bound in the wrong format": {
			q:    Query{Tenant: testTenant, To: "yesterday"},
			want: "must be yyyy-mm-dd",
		},
		// The shape regexp accepted all of these, and the bound was then compared
		// lexicographically: a transposed date returned an empty log with a nil error and
		// told an operator that nobody had accessed anything. Same reasoning as the result
		// filter — a wrong answer that looks right is the worst outcome for a compliance
		// question.
		"a day/month transposition, the most common date typo": {
			q:    Query{Tenant: testTenant, From: "2026-31-08"},
			want: "not a real date",
		},
		"a month of 13": {
			q:    Query{Tenant: testTenant, From: "2026-13-01"},
			want: "not a real date",
		},
		"a day of 32": {
			q:    Query{Tenant: testTenant, To: "2026-08-32"},
			want: "not a real date",
		},
		"zeroes, which sorted below every record and silently meant unbounded": {
			q:    Query{Tenant: testTenant, From: "2026-00-00"},
			want: "not a real date",
		},
		"a day that does not exist in that month": {
			q:    Query{Tenant: testTenant, From: "2026-02-30"},
			want: "not a real date",
		},
		"an hour of 24 in a full timestamp bound": {
			q:    Query{Tenant: testTenant, From: "2026-08-04T24:00:00Z"},
			want: "not a real date",
		},
		"a minute of 61 in a full timestamp bound": {
			q:    Query{Tenant: testTenant, To: "2026-08-04T10:61:00Z"},
			want: "not a real date",
		},
		"a misspelt result filter": {
			// Returning nothing would be indistinguishable from a clean log, which is
			// the wrong answer to report from a typo.
			q:    Query{Tenant: testTenant, Result: "allow"},
			want: "neither",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := a.Query(context.Background(), tc.q)
			if err == nil {
				t.Fatalf("query accepted; expected refusal mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestQueryReportsAReadFailure(t *testing.T) {
	// A read that failed must not be reported as an empty log: "nobody accessed it" and
	// "we could not tell" are different answers, and only one of them is safe to act on.
	repo, a := seedLog(t)
	boom := errors.New("dynamodb throttled")
	repo.FailNext(boom)

	page, err := a.Query(context.Background(), Query{Tenant: testTenant})
	if err == nil {
		t.Fatalf("a failed read returned %d records and no error", len(page.Records))
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the storage failure with %%w", err)
	}
}

func TestAuditPrefixIsDerivedFromTheKeyHelper(t *testing.T) {
	// The prefix must match what keys.Audit produces, since the literal cannot be
	// written here (I11). If keys ever changes the prefix, this fails rather than the
	// query silently returning nothing.
	pk, prefix, err := auditPrefix(testTenant)
	if err != nil {
		t.Fatalf("auditPrefix: %v", err)
	}
	k := auditKey(t, testTenant, "00000000000000000000000001")
	if pk != k.PK {
		t.Errorf("pk = %q, want %q", pk, k.PK)
	}
	if !strings.HasPrefix(k.SK, prefix) || prefix == "" {
		t.Errorf("prefix %q is not a prefix of the audit sort key %q", prefix, k.SK)
	}
	if _, _, err := auditPrefix(""); err == nil {
		t.Error("auditPrefix accepted an empty tenant; no read path may be unscoped (I11)")
	}
}

// levelOf returns the level of the first logged line whose message contains want.
func levelOf(t *testing.T, buf *bytes.Buffer, want string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if strings.Contains(entry.Msg, want) {
			return entry.Level
		}
	}
	t.Fatalf("no log line mentioning %q; got: %s", want, buf.String())
	return ""
}
