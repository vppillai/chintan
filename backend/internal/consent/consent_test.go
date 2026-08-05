package consent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The negative cases are the point of this file. A resolver that answers correctly for a
// well-formed grant but also answers "granted" for a nil map, a corrupt record, or a
// dropped connection is an I14 violation that no positive test would catch — so every
// case below asserts the *reason* for refusal, not merely that a refusal happened. A
// test that only checked `!Allowed()` would pass even if the resolver were refusing for
// the wrong reason, and the reason is what tells an operator whether the user declined
// or the table is broken.
//
// The concurrency tests are the ones that exist because the first version of this package
// was wrong. Its write path was a read-modify-write of the whole tenant record, and two
// interleavings were demonstrated to lose consent state outright: a grant reverting an
// acknowledged withdrawal with no trace of it, and a write to one purpose erasing
// another. Both are reproduced below against the append-only log.

const (
	tenantA = keys.TenantID("t_personal")
	tenantB = keys.TenantID("t_other")
)

// fixedNow is the clock every test shares, so an asserted timestamp is a constant rather
// than something recomputed at assert time (see the clock package).
var fixedNow = clock.Fixed{T: time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)}

const fixedTS = "2026-08-04T10:30:00Z"

// baseAttrs is the tenant record as something else provisioned it. Present in every
// fixture because a consent change must leave all of it untouched — kms_key_id
// especially, which §6.3 requires never to be absent (I8).
func baseAttrs() map[string]any {
	return map[string]any{
		"plan":        "personal",
		"region":      "eu-west-1",
		"kms_key_id":  "alias/aws/dynamodb",
		"status":      "active",
		"created_at":  "2026-01-01T00:00:00Z",
		"unrelated":   "must survive",
		"consent_ver": "not a real attribute, but nothing may drop it either",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// newResolver builds a resolver over a repository holding one tenant record for tenantA
// with the given attributes. A nil attrs map means no tenant record at all.
func newResolver(t *testing.T, attrs map[string]any) (*Resolver, *repository.Memory) {
	t.Helper()
	repo := repository.NewMemory()
	if attrs != nil {
		putTenant(t, repo, tenantA, attrs)
	}
	return New(repo, fixedNow, discardLogger()), repo
}

func putTenant(t *testing.T, repo *repository.Memory, tenant keys.TenantID, attrs map[string]any) {
	t.Helper()
	key, err := keys.Tenant(tenant)
	if err != nil {
		t.Fatalf("building tenant key: %v", err)
	}
	if err := repo.Put(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
		t.Fatalf("seeding tenant record: %v", err)
	}
}

// withConsent returns the base tenant attributes plus a §6.3 `consent` map. Used only by
// the tests that assert this package ignores that attribute.
func withConsent(consent any) map[string]any {
	attrs := baseAttrs()
	attrs[attrConsent] = consent
	return attrs
}

// eventAttrs is the stored shape of one consent event, as DynamoDB unmarshalling
// produces it.
func eventAttrs(purpose Purpose, granted bool, ts, version string) map[string]any {
	return map[string]any{
		fieldPurpose: string(purpose),
		fieldGranted: granted,
		fieldTS:      ts,
		fieldVersion: version,
	}
}

// putEvent seeds one consent event at a chosen sequence number, so a test can build a log
// no code path would produce — a gap, a partial write, a value written by something that
// disagreed with this package about the schema.
func putEvent(t *testing.T, repo *repository.Memory, tenant keys.TenantID, purpose Purpose, seq int, attrs map[string]any) {
	t.Helper()
	key, err := consentEventKey(tenant, purpose, seq)
	if err != nil {
		t.Fatalf("building consent event key: %v", err)
	}
	if err := repo.Put(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
		t.Fatalf("seeding consent event: %v", err)
	}
}

// putRawEvent seeds an item under an arbitrary sort key in the tenant's partition, for
// the cases where the key itself is what is wrong.
func putRawEvent(t *testing.T, repo *repository.Memory, tenant keys.TenantID, sk string, attrs map[string]any) {
	t.Helper()
	key, err := keys.Tenant(tenant)
	if err != nil {
		t.Fatalf("building tenant key: %v", err)
	}
	key.SK = sk
	if err := repo.Put(context.Background(), repository.Item{Key: key, Attrs: attrs}); err != nil {
		t.Fatalf("seeding raw event: %v", err)
	}
}

// assertRefused checks a decision denies, denies for the expected reason, and leaks no
// version. The version check matters on its own: a refusal that still handed back a
// version would let a caller that skipped Allowed() stamp a corpus record with it.
func assertRefused(t *testing.T, d Decision, want Reason) {
	t.Helper()
	if d.Allowed() {
		t.Fatalf("consent was GRANTED where %q was expected; this is an I14 violation: %s", want, d)
	}
	if d.Reason() == ReasonGranted {
		// ReasonGranted on a denying Decision would mislead any settings surface or
		// §11.6 report that branches on the reason rather than on Allowed(). Only
		// evaluate() may produce this value, and only for a grant.
		t.Errorf("a refusal reported reason %q", ReasonGranted)
	}
	if d.Reason() != want {
		t.Errorf("refused for reason %q, want %q (decision: %s)", d.Reason(), want, d)
	}
	if v := d.Version(); v != "" {
		t.Errorf("a refusal returned version %q; nothing may be stamped with the version of a refusal", v)
	}
	if ts := d.GrantedAt(); ts != "" {
		t.Errorf("a refusal returned granted-at %q", ts)
	}
}

func history(t *testing.T, r *Resolver, purpose Purpose) []Event {
	t.Helper()
	events, err := r.History(context.Background(), tenantA, purpose)
	if err != nil {
		t.Fatalf("History(%q): %v", purpose, err)
	}
	return events
}

// ---------------------------------------------------------------------------
// Fail-closed
// ---------------------------------------------------------------------------

// The zero value is the shape a forgotten assignment, an early return, or a
// partially-built struct produces. It must deny.
func TestZeroDecisionIsRefusal(t *testing.T) {
	var d Decision
	if d.Allowed() {
		t.Fatal("the zero Decision reported granted; every degenerate case must be refusal (I14)")
	}
	if d.Version() != "" || d.GrantedAt() != "" {
		t.Error("the zero Decision carried a version or timestamp")
	}
	if d.Reason() == ReasonGranted {
		t.Error("the zero Decision reports reason granted")
	}
}

func TestResolveFailsClosedInEveryDegenerateCase(t *testing.T) {
	cases := map[string]struct {
		// seed builds the stored state. A nil tenant attrs map means no tenant record.
		attrs  map[string]any
		events func(t *testing.T, repo *repository.Memory)
		want   Reason
	}{
		"no tenant record at all": {
			attrs: nil,
			want:  ReasonNoTenantRecord,
		},
		"tenant record with no consent events": {
			attrs: baseAttrs(),
			want:  ReasonNoConsentState,
		},
		"consent events exist for the other purpose only": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeModelImprovement, 0,
					eventAttrs(PurposeModelImprovement, true, fixedTS, "v1"))
			},
			want: ReasonPurposeAbsent,
		},
		"latest event is a refusal": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, false, fixedTS, ""))
			},
			want: ReasonRefused,
		},
		"granted with no version, so a purge could never find the records": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, ""))
			},
			want: ReasonMissingVersion,
		},
		"granted with a whitespace version": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "   "))
			},
			want: ReasonMissingVersion,
		},
		"granted with a version the purge could not match": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, `v 1" OR 1=1`))
			},
			want: ReasonMalformedVersion,
		},
		"granted with no timestamp, so the state is not a timestamped one": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, "", "v1"))
			},
			want: ReasonMissingTimestamp,
		},
		"refused with no timestamp is not a recorded refusal either": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, false, "", ""))
			},
			want: ReasonMissingTimestamp,
		},
		"granted with an unparseable timestamp": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, "yesterday", "v1"))
			},
			want: ReasonMalformedTimestamp,
		},
		"granted with a timestamp carrying a local offset": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, "2026-08-04T10:30:00+05:30", "v1"))
			},
			want: ReasonMalformedTimestamp,
		},
		"granted with a timestamp that is UTC-shaped but not a real instant": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, "2026-02-31T10:30:00Z", "v1"))
			},
			want: ReasonMalformedTimestamp,
		},
		"event with no attributes at all": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{})
			},
			want: ReasonMalformedState,
		},
		"granted field absent, so the event records neither a grant nor a refusal": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
					fieldPurpose: string(PurposeCorpusRetention),
					fieldTS:      fixedTS,
					fieldVersion: "v1",
				})
			},
			want: ReasonMalformedState,
		},
		"granted field is the string \"true\" rather than a bool": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
					fieldPurpose: string(PurposeCorpusRetention),
					fieldGranted: "true",
					fieldTS:      fixedTS,
					fieldVersion: "v1",
				})
			},
			want: ReasonMalformedState,
		},
		"granted field is a number": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
					fieldPurpose: string(PurposeCorpusRetention),
					fieldGranted: 1,
					fieldTS:      fixedTS,
					fieldVersion: "v1",
				})
			},
			want: ReasonMalformedState,
		},
		"version field is a number": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
					fieldPurpose: string(PurposeCorpusRetention),
					fieldGranted: true,
					fieldTS:      fixedTS,
					fieldVersion: 1,
				})
			},
			want: ReasonMalformedState,
		},
		"event attribute names a different purpose than its key": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeModelImprovement, true, fixedTS, "v1"))
			},
			want: ReasonMalformedState,
		},
		"event key carries no sequence number": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putRawEvent(t, repo, tenantA, consentEventSKPrefix+string(PurposeCorpusRetention),
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
			},
			want: ReasonMalformedState,
		},
		"event key sequence is not zero-padded to the sortable width": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putRawEvent(t, repo, tenantA, consentEventSKPrefix+string(PurposeCorpusRetention)+"#0",
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
			},
			want: ReasonMalformedState,
		},
		"event key sequence is not numeric": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putRawEvent(t, repo, tenantA, consentEventSKPrefix+string(PurposeCorpusRetention)+"#-00001",
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
			},
			want: ReasonMalformedState,
		},
		"a gap in the sequence means an event was destroyed": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 2,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v3"))
			},
			want: ReasonAmbiguousHistory,
		},
		"the log starts above zero, so its first events are gone": {
			attrs: baseAttrs(),
			events: func(t *testing.T, repo *repository.Memory) {
				putEvent(t, repo, tenantA, PurposeCorpusRetention, 1,
					eventAttrs(PurposeCorpusRetention, true, fixedTS, "v2"))
			},
			want: ReasonAmbiguousHistory,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo := newResolver(t, c.attrs)
			if c.events != nil {
				c.events(t, repo)
			}
			assertRefused(t, r.Resolve(context.Background(), tenantA, PurposeCorpusRetention), c.want)
		})
	}
}

// An empty or malformed tenant must not resolve, even though the key helper would also
// refuse it: neither check is the only one (I11).
func TestResolveRefusesInvalidTenant(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	for _, tenant := range []keys.TenantID{"", " ", "\t", "t#1"} {
		d := r.Resolve(context.Background(), tenant, PurposeCorpusRetention)
		assertRefused(t, d, ReasonInvalidTenant)
		if d.Err() == nil {
			t.Errorf("tenant %q refused with no underlying error to log", string(tenant))
		}
	}
}

// A purpose outside the enumeration must never resolve against stored state — a typo
// fails closed rather than reaching a lookup that happens to miss.
func TestResolveRefusesUnknownPurpose(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	// Seeded so that a resolver reading blindly by string would find something.
	for _, name := range []string{"corpus", "CORPUS_RETENTION", "corpus_retention "} {
		putRawEvent(t, repo, tenantA, fmt.Sprintf("%s%s#%06d", consentEventSKPrefix, name, 0),
			eventAttrs(Purpose(name), true, fixedTS, "v1"))
	}
	for _, p := range []Purpose{"corpus", "CORPUS_RETENTION", "", "corpus_retention "} {
		assertRefused(t, r.Resolve(context.Background(), tenantA, p), ReasonUnknownPurpose)
	}
	// And a stored event for an unrecognised purpose does not poison a recognised one:
	// a purpose renamed out of the enumeration must not brick the rest.
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	if d := r.Resolve(context.Background(), tenantA, PurposeCorpusRetention); !d.Allowed() {
		t.Fatalf("an unrecognised purpose's events blocked a recognised one: %s (%v)", d, d.Err())
	}
}

// A storage failure is refusal, and the cause stays reachable for alerting. Nothing is
// lost by refusing: §Phase 4 keeps the audio and L0 regardless, so the triple can be
// written later — whereas one written without consent cannot be un-written.
func TestResolveRefusesOnRepositoryError(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	boom := errors.New("dynamodb: throttled")
	repo.FailNext(boom)

	d := r.Resolve(context.Background(), tenantA, PurposeCorpusRetention)
	assertRefused(t, d, ReasonLookupFailed)
	if !errors.Is(d.Err(), boom) {
		t.Errorf("the underlying failure was not reachable through Err(): %v", d.Err())
	}
}

// failingQueryRepo fails the log query while the tenant record reads fine, which is the
// half-failure a single FailNext cannot express.
type failingQueryRepo struct {
	*repository.Memory
	err error
}

func (f failingQueryRepo) QueryPrefix(context.Context, string, string, int) ([]repository.Item, error) {
	return nil, f.err
}

// A log that cannot be read is a refusal, not a grant read from the tenant record — the
// two used to be separate stores and the gate must not fall back onto either one.
func TestResolveRefusesWhenTheLogQueryFails(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	boom := errors.New("dynamodb: provisioned throughput exceeded")
	r := New(failingQueryRepo{Memory: mem, err: boom}, fixedNow, discardLogger())

	d := r.Resolve(context.Background(), tenantA, PurposeCorpusRetention)
	assertRefused(t, d, ReasonLookupFailed)
	if !errors.Is(d.Err(), boom) {
		t.Errorf("the query failure was not reachable through Err(): %v", d.Err())
	}
	if _, err := r.State(context.Background(), tenantA); err == nil {
		t.Error("State swallowed a log query failure")
	}
	if _, err := r.GrantedVersions(context.Background(), tenantA, PurposeCorpusRetention); err == nil {
		t.Error("GrantedVersions returned a purge scope built from a failed query")
	}
	if _, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1"); err == nil {
		t.Error("Grant appended onto a log it could not read")
	}
}

// A resolver wired without a repository refuses rather than panicking: Resolve promises
// never to fail, and a panic in a handler is a worse outcome than a fail-closed refusal.
func TestResolveWithoutRepositoryRefuses(t *testing.T) {
	r := New(nil, fixedNow, discardLogger())
	assertRefused(t, r.Resolve(context.Background(), tenantA, PurposeCorpusRetention), ReasonLookupFailed)
}

// A repository that returns neither an item nor an error must not crash the resolver.
type nilItemRepo struct{ *repository.Memory }

func (nilItemRepo) Get(context.Context, keys.DynamoKey) (*repository.Item, error) { return nil, nil }

func TestResolveHandlesRepositoryReturningNoItemAndNoError(t *testing.T) {
	r := New(nilItemRepo{repository.NewMemory()}, fixedNow, discardLogger())
	assertRefused(t, r.Resolve(context.Background(), tenantA, PurposeCorpusRetention), ReasonLookupFailed)
}

// loadTenant's success sentinel must not be ReasonGranted. It is passed straight to
// refuse() on the error path, so the day someone adds an early return that forgets to set
// a reason, the resulting Decision would deny while reporting reason="granted" to
// anything branching on the reason rather than on Allowed().
func TestLookupSuccessSentinelIsNotAGrantedReason(t *testing.T) {
	if reasonNone == ReasonGranted {
		t.Fatal("the lookup success sentinel is ReasonGranted; only evaluate() may produce that value")
	}
	r, _ := newResolver(t, baseAttrs())
	_, reason, err := r.loadTenant(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("loadTenant: %v", err)
	}
	if reason != reasonNone {
		t.Errorf("loadTenant returned reason %q on success, want the empty sentinel", reason)
	}
	if d := refuse(PurposeCorpusRetention, reason, errors.New("x")); d.Allowed() || d.Reason() == ReasonGranted {
		t.Errorf("a refusal built from the success sentinel reports %s", d)
	}
}

// ---------------------------------------------------------------------------
// The positive path
// ---------------------------------------------------------------------------

func TestResolveReadsTheLatestEventInTheLog(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 1, eventAttrs(PurposeCorpusRetention, false, fixedTS, "v1"))
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 2, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v2"))

	d := r.Resolve(context.Background(), tenantA, PurposeCorpusRetention)
	if !d.Allowed() {
		t.Fatalf("the latest event is a grant but resolution refused: %s (%v)", d, d.Err())
	}
	if d.Version() != "v2" {
		t.Errorf("version %q, want v2; the state in force is the latest event", d.Version())
	}
	if d.GrantedAt() != fixedTS || d.Purpose() != PurposeCorpusRetention || d.Reason() != ReasonGranted {
		t.Errorf("decision is %s (granted_at %q)", d, d.GrantedAt())
	}
}

// Ten events, so lexicographic and numeric order disagree unless the sequence is padded.
// Without the padding event 10 sorts before event 2 and "the latest event wins" silently
// picks the wrong one — which for a withdrawal followed by grants means retention
// continuing after a withdrawal.
func TestLatestEventIsFoundBeyondTheFirstDecade(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("Grant %d: %v", i, err)
		}
		if _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); err != nil {
			t.Fatalf("Withdraw %d: %v", i, err)
		}
	}
	events := history(t, r, PurposeCorpusRetention)
	if len(events) != 12 {
		t.Fatalf("log holds %d events, want 12", len(events))
	}
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonRefused)
	for i, ev := range events {
		if ev.Seq != i {
			t.Fatalf("event %d carries sequence %d; the log must read back dense and in order", i, ev.Seq)
		}
	}
}

// §Phase 4's distinction: consent is per purpose, never one global boolean. Granting
// corpus retention must not grant model improvement.
func TestConsentIsPerPurpose(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()

	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); !d.Allowed() {
		t.Fatalf("the granted purpose did not resolve: %s", d)
	}
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeModelImprovement), ReasonPurposeAbsent)
}

// ---------------------------------------------------------------------------
// Recording
// ---------------------------------------------------------------------------

func TestGrantIsRejectedWithoutAUsableVersion(t *testing.T) {
	cases := map[string]struct {
		version string
		want    string
	}{
		"empty":            {"", "consent version"},
		"whitespace":       {"   ", "consent version"},
		"contains a space": {"v 1", "not a token"},
		"contains a quote": {`v"1`, "not a token"},
		"contains a slash": {"v/1", "not a token"},
		"too long":         {strings.Repeat("v", 65), "not a token"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo := newResolver(t, baseAttrs())
			before := repo.Len()

			d, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, c.version)
			if err == nil {
				t.Fatalf("version %q was accepted; an unversioned grant is unpurgeable (§Phase 4)", c.version)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("rejection did not mention %q:\n%v", c.want, err)
			}
			if d.Allowed() {
				t.Error("a rejected grant returned a permissive decision")
			}
			if repo.Len() != before {
				t.Error("a rejected grant still wrote to storage")
			}
		})
	}
}

func TestGrantAndWithdrawRejectUnrecognisedPurpose(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, "corpus", "v1"); err == nil {
		t.Error("Grant accepted an unrecognised purpose")
	}
	if _, err := r.Withdraw(ctx, tenantA, "corpus"); err == nil {
		t.Error("Withdraw accepted an unrecognised purpose")
	}
	if _, err := consentEventKey(tenantA, "corpus", 0); err == nil {
		t.Error("an unrecognised purpose was given a consent event key, so it would own a log nothing can resolve")
	}
}

// Consent belongs to a provisioned tenant (§6.3). This package must not invent a tenant
// record: one it created would have no kms_key_id, which §6.3 requires never to be absent.
func TestGrantRequiresAnExistingTenantRecord(t *testing.T) {
	r, repo := newResolver(t, nil)
	d, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1")
	if err == nil {
		t.Fatal("Grant created consent state with no tenant record")
	}
	if d.Allowed() {
		t.Error("a failed grant returned a permissive decision")
	}
	if repo.Len() != 0 {
		t.Error("a failed grant wrote a record")
	}
}

// failingPutOnceRepo fails every conditional write, so the "a failed write is never
// reported as a grant" property is exercised rather than assumed.
type failingPutOnceRepo struct {
	*repository.Memory
	err error
}

func (f failingPutOnceRepo) PutOnce(context.Context, repository.Item) error { return f.err }

func TestGrantThatFailsToWriteIsNotReportedAsGranted(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	r := New(failingPutOnceRepo{Memory: mem, err: errors.New("dynamodb: internal server error")}, fixedNow, discardLogger())

	d, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1")
	if err == nil {
		t.Fatal("a failed write returned no error")
	}
	if d.Allowed() {
		t.Fatal("a failed write reported consent as granted; a caller ignoring the error would then retain content it has no consent for (I14)")
	}
	// The state is unchanged, so a later Resolve still refuses.
	r2 := New(mem, fixedNow, discardLogger())
	assertRefused(t, r2.Resolve(context.Background(), tenantA, PurposeCorpusRetention), ReasonNoConsentState)
}

func TestGrantIsRecordedWithTheClockTimestamp(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()

	d, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "2026-08-01")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if !d.Allowed() || d.Version() != "2026-08-01" || d.GrantedAt() != fixedTS {
		t.Fatalf("Grant returned %s (granted_at %q)", d, d.GrantedAt())
	}
	// Resolve must agree with what Grant claimed, or the write and the read disagree
	// about the schema.
	got := r.Resolve(ctx, tenantA, PurposeCorpusRetention)
	if !got.Allowed() || got.Version() != d.Version() || got.GrantedAt() != d.GrantedAt() {
		t.Fatalf("Resolve disagrees with Grant: %s vs %s", got, d)
	}
}

// The timestamp this package writes must satisfy the pattern it reads back with, or every
// grant it records would resolve as ReasonMalformedTimestamp.
func TestRecordedTimestampMatchesTheAcceptedFormat(t *testing.T) {
	ts := clock.RFC3339UTC(fixedNow.Now())
	if !rfc3339UTCRe.MatchString(ts) {
		t.Fatalf("clock.RFC3339UTC produces %q, which the resolver rejects", ts)
	}
}

// A repeated identical settings PATCH must not grow the log.
func TestRepeatedIdenticalGrantDoesNotGrowTheLog(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
			t.Fatalf("Grant %d: %v", i, err)
		}
	}
	if events := history(t, r, PurposeCorpusRetention); len(events) != 1 {
		t.Fatalf("log has %d events after five identical grants, want 1", len(events))
	}
}

func TestRepeatedWithdrawalDoesNotGrowTheLog(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.Withdraw(ctx, tenantA, PurposeModelImprovement); err != nil {
			t.Fatalf("Withdraw %d: %v", i, err)
		}
	}
	events := history(t, r, PurposeModelImprovement)
	if len(events) != 1 {
		t.Fatalf("log has %d events after three identical withdrawals, want 1", len(events))
	}
	if events[0].Granted {
		t.Error("the recorded event says granted")
	}
}

// The idempotency short-circuit must compare against the *resolved* state, not against two
// fields of the stored entry.
//
// Demonstrated failure of the previous rule, which matched on (granted, version) alone: a
// bootstrap or repair script writes a grant with no timestamp, the user clicks "I consent"
// in settings, and Grant returns success having written nothing — so the state stays
// unresolvable and consent for that purpose becomes permanently unobtainable through the
// product, with no error anywhere to say so.
func TestGrantRepairsAStateThatDecodesButDoesNotResolve(t *testing.T) {
	cases := map[string]map[string]any{
		"grant with no timestamp":          eventAttrs(PurposeCorpusRetention, true, "", "v1"),
		"grant with an unusable timestamp": eventAttrs(PurposeCorpusRetention, true, "yesterday", "v1"),
		"grant with a local offset":        eventAttrs(PurposeCorpusRetention, true, "2026-08-04T10:30:00+05:30", "v1"),
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo := newResolver(t, baseAttrs())
			ctx := context.Background()
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, attrs)

			if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); d.Allowed() {
				t.Fatalf("fixture resolved as granted: %s", d)
			}
			d, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1")
			if err != nil {
				t.Fatalf("Grant: %v", err)
			}
			if !d.Allowed() || d.Version() != "v1" {
				t.Fatalf("Grant reported %s; the user's stated choice was not recorded", d)
			}
			if events := history(t, r, PurposeCorpusRetention); len(events) != 2 {
				t.Fatalf("log has %d events, want 2; the grant short-circuited on a state that does not resolve", len(events))
			}
			if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); !d.Allowed() {
				t.Fatalf("consent is still unobtainable after an explicit grant: %s (%v)", d, d.Err())
			}
		})
	}
}

// A withdrawal with nothing to withdraw is still recorded. A recorded refusal and
// "never asked" are the same in effect but not the same fact, and I14 is about what was
// recorded.
func TestWithdrawWithNoPriorGrantRecordsAnExplicitRefusal(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()

	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonNoConsentState)

	d, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	assertRefused(t, d, ReasonRefused)
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonRefused)

	events := history(t, r, PurposeCorpusRetention)
	if len(events) != 1 || events[0].TS != fixedTS || events[0].Version != "" {
		t.Fatalf("recorded refusal is %+v", events)
	}
}

// This is the §Phase 4 requirement: "a later consent withdrawal must be able to identify
// and purge exactly the affected records".
func TestWithdrawalPreservesEveryVersionEverGranted(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	clkAt := fixedNow

	steps := []func() error{
		func() error { _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); return err },
		func() error { _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); return err },
		func() error { _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v2"); return err },
		func() error { _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); return err },
	}
	for i, step := range steps {
		// Advance the clock between steps so the recorded timestamps differ, which is
		// what makes the log readable as a sequence by a human.
		clkAt = clkAt.Advance(time.Hour)
		r.clk = clkAt
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonRefused)

	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 2 || versions[0] != "v1" || versions[1] != "v2" {
		t.Fatalf("GrantedVersions returned %v, want [v1 v2]; a purge driven by this would miss records", versions)
	}

	events := history(t, r, PurposeCorpusRetention)
	if len(events) != 4 {
		t.Fatalf("log has %d events, want 4: %+v", len(events), events)
	}
	wantGranted := []bool{true, false, true, false}
	wantVersion := []string{"v1", "v1", "v2", "v2"}
	for i, ev := range events {
		if ev.Granted != wantGranted[i] || ev.Version != wantVersion[i] {
			t.Errorf("event %d is %+v, want granted=%v version=%q", i, ev, wantGranted[i], wantVersion[i])
		}
		if ev.Purpose != PurposeCorpusRetention || ev.Seq != i {
			t.Errorf("event %d is %+v", i, ev)
		}
	}
	// Sequence order, not timestamp order re-derived at read time — timestamps are
	// second-precision and two events in one second would reorder arbitrarily.
	for i := 1; i < len(events); i++ {
		if events[i].TS < events[i-1].TS {
			t.Errorf("event %d is older than its predecessor: %+v", i, events)
		}
	}
}

// GrantedVersions must not report a version that was only ever refused; a purge driven
// by it would delete records that were never covered by that consent.
func TestGrantedVersionsExcludesAPurposeNeverGranted(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("GrantedVersions returned %v after a refusal that withdrew from nothing", versions)
	}
}

// A withdrawal event carries the version it withdrew from, deliberately, so that the most
// recent affected version is recoverable. The previous implementation filtered withdrawal
// events out of the purge scope, turning that datum into ([], nil) — indistinguishable
// from "never granted", and a purge driven by it deletes nothing while every affected
// record survives the withdrawal.
func TestGrantedVersionsIncludesAVersionCarriedOnlyByAWithdrawal(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0,
		eventAttrs(PurposeCorpusRetention, false, fixedTS, "v1"))

	versions, err := r.GrantedVersions(context.Background(), tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != "v1" {
		t.Fatalf("GrantedVersions returned %v, want [v1]; the version a withdrawal names is a version records were stamped with", versions)
	}
}

// The purge scope must never come back as an empty list when the truth is "this could not
// be read". ([], nil) means one thing only: never granted.
func TestGrantedVersionsErrorsRatherThanReturningAnEmptyScope(t *testing.T) {
	cases := map[string]func(t *testing.T, repo *repository.Memory){
		"an event that does not decode": func(t *testing.T, repo *repository.Memory) {
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
				fieldPurpose: string(PurposeCorpusRetention),
				fieldTS:      fixedTS,
				fieldVersion: "v1",
			})
		},
		"a gap where an event used to be": func(t *testing.T, repo *repository.Memory) {
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 2, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v3"))
		},
		"an event whose key does not parse": func(t *testing.T, repo *repository.Memory) {
			putRawEvent(t, repo, tenantA, consentEventSKPrefix+"corpus_retention#x",
				eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo := newResolver(t, baseAttrs())
			seed(t, repo)
			versions, err := r.GrantedVersions(context.Background(), tenantA, PurposeCorpusRetention)
			if err == nil {
				t.Fatalf("GrantedVersions returned %v with a nil error; a purge would visit nothing and report success", versions)
			}
			if versions != nil {
				t.Errorf("GrantedVersions returned %v alongside an error", versions)
			}
			if _, err := r.History(context.Background(), tenantA, PurposeCorpusRetention); err == nil {
				t.Error("History silently accepted an unreadable log")
			}
		})
	}
}

// History is per purpose. A purge for one purpose must not sweep records collected under
// another.
func TestHistoryIsFilteredByPurpose(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "corpus-v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := r.Grant(ctx, tenantA, PurposeModelImprovement, "model-v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	corpus, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(corpus) != 1 || corpus[0] != "corpus-v1" {
		t.Fatalf("corpus_retention versions %v", corpus)
	}
	if events := history(t, r, PurposeModelImprovement); len(events) != 1 || events[0].Version != "model-v1" {
		t.Fatalf("model_improvement history %+v", events)
	}
}

// One unreadable purpose must not stop a different purpose resolving: the two logs are
// separate, and a corrupt event in one is not a reason to deny the other's recorded state.
func TestAnUnreadableLogIsIsolatedToItsPurpose(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	putEvent(t, repo, tenantA, PurposeModelImprovement, 0, map[string]any{
		fieldPurpose: string(PurposeModelImprovement),
		fieldGranted: "yes",
		fieldTS:      fixedTS,
	})

	ctx := context.Background()
	if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); !d.Allowed() {
		t.Fatalf("a corrupt event in another purpose's log blocked this one: %s (%v)", d, d.Err())
	}
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeModelImprovement), ReasonMalformedState)
}

// ---------------------------------------------------------------------------
// Concurrency — the defect this package was rewritten for
// ---------------------------------------------------------------------------

// hookRepo runs a hook immediately after a log query, which is exactly the window a
// read-modify-write left open. The hook performs the competing request and clears itself
// first so it fires once, which makes the interleaving deterministic rather than a race
// the test hopes to hit.
type hookRepo struct {
	*repository.Memory
	hook func()
}

func (h *hookRepo) QueryPrefix(ctx context.Context, pk, prefix string, limit int) ([]repository.Item, error) {
	items, err := h.Memory.QueryPrefix(ctx, pk, prefix, limit)
	if h.hook != nil {
		h.hook()
	}
	return items, err
}

// An acknowledged withdrawal must never be reverted by a grant that read the state before
// it, and must never vanish from the record.
//
// Reproduction of the reported failure: seed grant v1; request 1 calls Grant(v2) and reads
// the state; request 2 withdraws and returns "refused" to the user; request 1 then writes.
// Under the read-modify-write this ended with Resolve=granted v2 and a history of
// [granted v1, granted v2] — no withdrawal event at all, so neither the §Phase 4 purge nor
// §11.6's check could ever discover that the user withdrew.
func TestAConcurrentWithdrawalIsNeverErasedByAGrantThatReadBeforeIt(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	repo := &hookRepo{Memory: mem}
	r := New(repo, fixedNow, discardLogger())
	ctx := context.Background()

	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("seeding grant: %v", err)
	}

	var withdrawn Decision
	repo.hook = func() {
		repo.hook = nil // the competing request must not recurse into the hook
		d, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention)
		if err != nil {
			t.Errorf("competing Withdraw: %v", err)
		}
		withdrawn = d
	}

	granted, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v2")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if withdrawn.Allowed() {
		t.Fatalf("the competing withdrawal reported a grant: %s", withdrawn)
	}

	events := history(t, r, PurposeCorpusRetention)
	if len(events) != 3 {
		t.Fatalf("log holds %d events, want 3 (grant v1, withdraw, grant v2): %+v", len(events), events)
	}
	if events[1].Granted {
		t.Fatalf("the withdrawal event is missing from the log: %+v", events)
	}
	if events[1].Version != "v1" {
		t.Errorf("the withdrawal does not name the version it withdrew from: %+v", events[1])
	}

	// Whatever the serialised outcome, Grant's answer and the gate's answer must be the
	// same answer. The failure being guarded is a caller told "granted" while the gate
	// says otherwise, or the reverse.
	final := r.Resolve(ctx, tenantA, PurposeCorpusRetention)
	if final.Allowed() != granted.Allowed() || final.Version() != granted.Version() {
		t.Fatalf("Grant reported %s but the gate says %s", granted, final)
	}
	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 2 || versions[0] != "v1" || versions[1] != "v2" {
		t.Fatalf("GrantedVersions returned %v, want [v1 v2]", versions)
	}
}

// The mirror interleaving, which is the one the user notices: a withdrawal that reads the
// state before a competing grant lands must still take effect, and must report the
// refusal it produced rather than the one it expected to produce.
func TestAWithdrawalThatLosesTheRaceStillTakesEffect(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	repo := &hookRepo{Memory: mem}
	r := New(repo, fixedNow, discardLogger())
	ctx := context.Background()

	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("seeding grant: %v", err)
	}

	repo.hook = func() {
		repo.hook = nil
		if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v2"); err != nil {
			t.Errorf("competing Grant: %v", err)
		}
	}

	d, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	assertRefused(t, d, ReasonRefused)
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonRefused)

	events := history(t, r, PurposeCorpusRetention)
	if len(events) != 3 || events[2].Granted {
		t.Fatalf("the withdrawal did not end up last: %+v", events)
	}
	if events[2].Version != "v2" {
		t.Errorf("the withdrawal names version %q, want v2 — the version actually in force when it landed", events[2].Version)
	}
	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("GrantedVersions returned %v; a purge must cover both versions ever granted", versions)
	}
}

// One PATCH /v1/settings applying two purposes concurrently must not lose either. This
// needs no second human, and under the read-modify-write it erased one purpose's entry
// outright — leaving corpus records already stamped with its version with no consent
// state at all, reported as reason=purpose_absent.
func TestWritingOnePurposeNeverErasesAnother(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	repo := &hookRepo{Memory: mem}
	r := New(repo, fixedNow, discardLogger())
	ctx := context.Background()

	repo.hook = func() {
		repo.hook = nil
		if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "c1"); err != nil {
			t.Errorf("competing Grant: %v", err)
		}
	}

	if _, err := r.Grant(ctx, tenantA, PurposeModelImprovement, "m1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	corpus := r.Resolve(ctx, tenantA, PurposeCorpusRetention)
	if !corpus.Allowed() || corpus.Version() != "c1" {
		t.Fatalf("corpus_retention resolved as %s (%v); records stamped c1 would have no consent state", corpus, corpus.Err())
	}
	improvement := r.Resolve(ctx, tenantA, PurposeModelImprovement)
	if !improvement.Allowed() || improvement.Version() != "m1" {
		t.Fatalf("model_improvement resolved as %s (%v)", improvement, improvement.Err())
	}
	for _, p := range Purposes() {
		versions, err := r.GrantedVersions(ctx, tenantA, p)
		if err != nil {
			t.Fatalf("GrantedVersions(%q): %v", p, err)
		}
		if len(versions) != 1 {
			t.Fatalf("GrantedVersions(%q) = %v, want one version", p, versions)
		}
	}
}

// Genuinely concurrent writers to the same purpose. Every one that reports success must be
// in the log, and the log must read back dense — the PutOnce contention is resolved by
// re-reading and appending after the winner, never by overwriting it.
func TestConcurrentWritersToOnePurposeAllLandExactlyOnce(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()

	const writers = 5
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Grant(ctx, tenantA, PurposeCorpusRetention, fmt.Sprintf("v%d", i))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	events := history(t, r, PurposeCorpusRetention)
	if len(events) != writers {
		t.Fatalf("log holds %d events after %d concurrent grants: %+v", len(events), writers, events)
	}
	for i, ev := range events {
		if ev.Seq != i {
			t.Fatalf("log is not dense at position %d: %+v", i, events)
		}
	}
	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != writers {
		t.Fatalf("GrantedVersions returned %v; every version a writer reported success for must be purgeable", versions)
	}
}

// ---------------------------------------------------------------------------
// What a consent write must not disturb
// ---------------------------------------------------------------------------

// refusingPutRepo fails every unconditional write. Consent records are append-only, so
// nothing in this package may need one — and with the tenant record no longer carrying
// consent state, kms_key_id cannot be lost to a consent change even in principle (I8).
type refusingPutRepo struct{ *repository.Memory }

func (refusingPutRepo) Put(_ context.Context, item repository.Item) error {
	return fmt.Errorf("consent must not overwrite any item: %s / %s", item.Key.PK, item.Key.SK)
}

func TestConsentChangesNeverOverwriteAnyItem(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	r := New(refusingPutRepo{Memory: mem}, fixedNow, discardLogger())
	ctx := context.Background()

	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant needed an unconditional write: %v", err)
	}
	if _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); err != nil {
		t.Fatalf("Withdraw needed an unconditional write: %v", err)
	}
	if _, err := r.Grant(ctx, tenantA, PurposeModelImprovement, "m1"); err != nil {
		t.Fatalf("Grant needed an unconditional write: %v", err)
	}
}

func TestConsentChangesLeaveTheTenantRecordByteIdentical(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("Grant: %v", err)
		}
		if _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); err != nil {
			t.Fatalf("Withdraw: %v", err)
		}
	}

	key, err := keys.Tenant(tenantA)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	item, err := repo.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := baseAttrs()
	if len(item.Attrs) != len(want) {
		t.Fatalf("the tenant record now holds %d attributes, want %d: %v", len(item.Attrs), len(want), item.Attrs)
	}
	for k, v := range want {
		if item.Attrs[k] != v {
			t.Errorf("attribute %q is %v after consent changes, want %v; kms_key_id in particular must never go absent (§6.3, I8)", k, item.Attrs[k], v)
		}
	}
}

// Consent events must never expire and must never project into GSI1: a TTL would turn a
// recorded consent state into "never asked" silently, and §6.3 makes GSI1 sparse to
// Capture and Thread records only.
func TestConsentEventsCarryNoTTLAndNoIndexAttributes(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	tenantKey, err := keys.Tenant(tenantA)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	items, err := repo.QueryPrefix(ctx, tenantKey.PK, consentEventSKPrefix, 0)
	if err != nil {
		t.Fatalf("QueryPrefix: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("found %d consent event items, want 1", len(items))
	}
	if items[0].TTL != 0 {
		t.Errorf("consent event carries TTL %d; the version list must outlive every other retention window (§Phase 4)", items[0].TTL)
	}
	if items[0].GSI1PK != "" || items[0].GSI1SK != "" {
		t.Errorf("consent event projects into GSI1 (%q/%q); the index is sparse to Capture and Thread (§6.3)", items[0].GSI1PK, items[0].GSI1SK)
	}
}

// ---------------------------------------------------------------------------
// The §6.3 `consent` attribute is not an input
// ---------------------------------------------------------------------------

// A consent state written by anything other than this package must not grant. This is the
// reviewer's demonstrated case: a tenant provisioned with
// consent={"corpus_retention":{granted:true,...}} used to resolve as granted while
// GrantedVersions returned ([], nil), so records were stamped under a version no purge
// would ever visit. The safe reading of an attribute this package cannot keep truthful is
// no reading at all.
func TestAConsentAttributeOnTheTenantRecordDoesNotGrant(t *testing.T) {
	cases := map[string]any{
		"untyped map": map[string]any{
			string(PurposeCorpusRetention): map[string]any{"granted": true, "ts": fixedTS, "version": "v1"},
		},
		"typed grants": map[string]model.ConsentGrant{
			string(PurposeCorpusRetention): {Granted: true, TS: fixedTS, Version: "v1"},
		},
		"typed grants keyed by Purpose": map[Purpose]model.ConsentGrant{
			PurposeCorpusRetention: {Granted: true, TS: fixedTS, Version: "v1"},
		},
		"not a map at all": "granted",
	}
	for name, attr := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newResolver(t, withConsent(attr))
			ctx := context.Background()
			assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonNoConsentState)

			// And the report agrees with the gate, so nothing downstream can read a grant
			// out of the attribute either.
			state, err := r.State(ctx, tenantA)
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			assertRefused(t, state[PurposeCorpusRetention], ReasonNoConsentState)

			versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
			if err != nil {
				t.Fatalf("GrantedVersions: %v", err)
			}
			if len(versions) != 0 {
				t.Errorf("GrantedVersions returned %v from an attribute the gate refuses", versions)
			}
		})
	}
}

// The attribute is noticed even though it is not honoured: nothing writes it, so its
// presence means out-of-band mutation of backend state (I16) and an operator needs to
// hear about it. An absent or empty attribute must stay silent, or the warning becomes
// noise nobody reads.
func TestALegacyConsentAttributeIsReportedButNotHonoured(t *testing.T) {
	cases := map[string]struct {
		attr any
		want []string
	}{
		"absent":       {attr: nil, want: nil},
		"empty map":    {attr: map[string]any{}, want: nil},
		"one purpose":  {attr: map[string]any{"corpus_retention": map[string]any{"granted": true}}, want: []string{"corpus_retention"}},
		"typed map":    {attr: map[string]model.ConsentGrant{"model_improvement": {}}, want: []string{"model_improvement"}},
		"unreadable":   {attr: "granted", want: []string{"<string>"}},
		"purpose-keys": {attr: map[Purpose]model.ConsentGrant{PurposeCorpusRetention: {}}, want: []string{"corpus_retention"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			attrs := baseAttrs()
			if c.attr != nil {
				attrs[attrConsent] = c.attr
			}
			got := legacyConsentPurposes(&repository.Item{Attrs: attrs})
			if len(got) != len(c.want) {
				t.Fatalf("legacyConsentPurposes returned %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("legacyConsentPurposes[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// The log survives a writer that rebuilds the tenant record from a model.Tenant and drops
// every attribute it does not know about. That used to destroy the version list behind the
// §Phase 4 purge while leaving the gate answering "granted"; separate items make it
// impossible.
func TestRebuildingTheTenantRecordCannotDestroyTheLog(t *testing.T) {
	r, repo := newResolver(t, withConsent(map[string]any{
		string(PurposeCorpusRetention): map[string]any{"granted": true, "ts": fixedTS, "version": "v1"},
	}))
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// A future provisioning path writes the §6.3 attribute list and nothing else.
	putTenant(t, repo, tenantA, baseAttrs())

	if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); !d.Allowed() || d.Version() != "v1" {
		t.Fatalf("consent did not survive a tenant-record rebuild: %s (%v)", d, d.Err())
	}
	versions, err := r.GrantedVersions(ctx, tenantA, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != "v1" {
		t.Fatalf("GrantedVersions returned %v after a tenant-record rebuild", versions)
	}
}

// ---------------------------------------------------------------------------
// Ceilings
// ---------------------------------------------------------------------------

// At the per-purpose ceiling a grant is refused and a withdrawal is still recorded. The
// asymmetry is the point: the previous design shared the tenant item's 400KB ceiling with
// kms_key_id, so the operation that eventually became impossible was withdrawal — a
// ceiling that blocks the user from saying no is the failure this package exists to
// prevent.
func TestAtTheCeilingGrantsAreRefusedAndWithdrawalStillWorks(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i < maxEventsPerPurpose; i++ {
		putEvent(t, repo, tenantA, PurposeCorpusRetention, i, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	}

	// The identical grant is still a no-op rather than an error: the state in force
	// already resolves to it.
	if d, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil || !d.Allowed() {
		t.Fatalf("an idempotent grant at the ceiling returned %s / %v", d, err)
	}
	_, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v2")
	if err == nil {
		t.Fatal("a grant past the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), "withdrawal is still available") {
		t.Errorf("the refusal does not tell the operator what is still possible:\n%v", err)
	}

	if _, err := r.Withdraw(ctx, tenantA, PurposeCorpusRetention); err != nil {
		t.Fatalf("withdrawal was blocked at the ceiling, which is the one thing that must never happen: %v", err)
	}
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonRefused)
	if events := history(t, r, PurposeCorpusRetention); len(events) != maxEventsPerPurpose+1 {
		t.Fatalf("log holds %d events, want %d", len(events), maxEventsPerPurpose+1)
	}
}

// A log past what one query reads is a refusal, not a truncated read. Truncation drops the
// newest events, and the newest event is the one that decides — losing a withdrawal is the
// fail-open case.
func TestALogPastTheReadableCeilingRefusesRatherThanTruncating(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	for i := 0; i <= maxEventsTotal; i++ {
		putEvent(t, repo, tenantA, PurposeCorpusRetention, i, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	}
	assertRefused(t, r.Resolve(ctx, tenantA, PurposeCorpusRetention), ReasonTooManyEvents)
	if _, err := r.History(ctx, tenantA, PurposeCorpusRetention); err == nil {
		t.Error("History returned a truncated log")
	}
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v2"); err == nil {
		t.Error("Grant appended onto a log it cannot read")
	}
}

// A log this package cannot read is not something to append onto: appending would leave it
// unreadable, so the gate would go on refusing while the caller was told the change was
// recorded. Repair is an operational script (I16).
func TestRecordingRefusesOntoALogItCannotRead(t *testing.T) {
	cases := map[string]func(t *testing.T, repo *repository.Memory){
		"an event that does not decode": func(t *testing.T, repo *repository.Memory) {
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
				fieldPurpose: string(PurposeCorpusRetention),
				fieldGranted: "true",
				fieldTS:      fixedTS,
			})
		},
		"a gap in the sequence": func(t *testing.T, repo *repository.Memory) {
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
			putEvent(t, repo, tenantA, PurposeCorpusRetention, 3, eventAttrs(PurposeCorpusRetention, false, fixedTS, "v1"))
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo := newResolver(t, baseAttrs())
			seed(t, repo)
			ctx := context.Background()
			before := repo.Len()

			for _, op := range []struct {
				name string
				run  func() (Decision, error)
			}{
				{"Grant", func() (Decision, error) { return r.Grant(ctx, tenantA, PurposeCorpusRetention, "v9") }},
				{"Withdraw", func() (Decision, error) { return r.Withdraw(ctx, tenantA, PurposeCorpusRetention) }},
			} {
				d, err := op.run()
				if err == nil {
					t.Fatalf("%s reported success against an unreadable log", op.name)
				}
				if !strings.Contains(err.Error(), "I16") {
					t.Errorf("%s did not point at an operational repair:\n%v", op.name, err)
				}
				if d.Allowed() {
					t.Errorf("%s returned a permissive decision", op.name)
				}
			}
			if repo.Len() != before {
				t.Error("an event was appended onto an unreadable log")
			}
			// And the gate still refuses, so the user's position is unchanged.
			if d := r.Resolve(ctx, tenantA, PurposeCorpusRetention); d.Allowed() {
				t.Errorf("an unreadable log resolved as granted: %s", d)
			}
		})
	}
}

// flakyQueryRepo fails the log query from the nth call onward, so the window between the
// write landing and the state being read back can be exercised.
type flakyQueryRepo struct {
	*repository.Memory
	failFrom int
	calls    int
	err      error
}

func (f *flakyQueryRepo) QueryPrefix(ctx context.Context, pk, prefix string, limit int) ([]repository.Item, error) {
	f.calls++
	if f.calls >= f.failFrom {
		return nil, f.err
	}
	return f.Memory.QueryPrefix(ctx, pk, prefix, limit)
}

// A grant whose write landed but whose resulting state could not be read back reports
// both: a refusal, so nothing downstream retains content on an unconfirmed read, and an
// error, so the handler does not write an audit entry saying consent was granted on the
// strength of a read that did not happen.
func TestGrantReportsAWriteItCouldNotConfirm(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	boom := errors.New("dynamodb: throttled")
	// Call 1 is record's own read; call 2 is the read-back through the gate.
	repo := &flakyQueryRepo{Memory: mem, failFrom: 2, err: boom}
	r := New(repo, fixedNow, discardLogger())

	d, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1")
	if err == nil {
		t.Fatal("an unconfirmed write reported plain success")
	}
	if d.Allowed() {
		t.Fatal("an unconfirmed write reported consent as granted (I14)")
	}
	if d.Reason() != ReasonLookupFailed {
		t.Errorf("reason %q, want %q", d.Reason(), ReasonLookupFailed)
	}
	// The event did land, so the user's choice is not lost — only unconfirmed.
	r2 := New(mem, fixedNow, discardLogger())
	if got := r2.Resolve(context.Background(), tenantA, PurposeCorpusRetention); !got.Allowed() {
		t.Fatalf("the event that was written is not readable: %s (%v)", got, got.Err())
	}
}

// alwaysTakenRepo makes every conditional write lose, which is what a permanently
// contended sequence number looks like. Recording must give up with an error rather than
// spinning.
type alwaysTakenRepo struct {
	*repository.Memory
	calls int
}

func (a *alwaysTakenRepo) PutOnce(context.Context, repository.Item) error {
	a.calls++
	return fmt.Errorf("%w: contended", repository.ErrAlreadyExists)
}

func TestRecordingGivesUpAfterABoundedNumberOfLostRaces(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	repo := &alwaysTakenRepo{Memory: mem}
	r := New(repo, fixedNow, discardLogger())

	d, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1")
	if err == nil {
		t.Fatal("Grant reported success without writing anything")
	}
	if d.Allowed() {
		t.Error("a grant that never landed returned a permissive decision")
	}
	if repo.calls != maxAppendAttempts {
		t.Errorf("attempted the write %d times, want %d", repo.calls, maxAppendAttempts)
	}
}

// ---------------------------------------------------------------------------
// Tenant scoping (I11)
// ---------------------------------------------------------------------------

func TestConsentIsTenantScoped(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	putTenant(t, repo, tenantB, baseAttrs())

	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// The other tenant's consent state is untouched and still refuses. One tenant's
	// grant leaking into another's resolution would be the cross-tenant bug I11 exists
	// to prevent.
	assertRefused(t, r.Resolve(ctx, tenantB, PurposeCorpusRetention), ReasonNoConsentState)

	versions, err := r.GrantedVersions(ctx, tenantB, PurposeCorpusRetention)
	if err != nil {
		t.Fatalf("GrantedVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("tenant B reports versions %v from tenant A's grant", versions)
	}
}

// Every consent event lands in its tenant's partition, and that partition key comes from
// the key helper rather than from anything assembled here (I11).
func TestConsentEventsStayInTheTenantPartition(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := r.Withdraw(ctx, tenantA, PurposeModelImprovement); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	want, err := keys.Tenant(tenantA)
	if err != nil {
		t.Fatalf("keys.Tenant: %v", err)
	}
	for _, k := range repo.Keys() {
		if k.PK != want.PK {
			t.Errorf("consent wrote outside the tenant partition: %s / %s", k.PK, k.SK)
		}
	}
	// An empty tenant must not produce a key at all, so no consent event can land in a
	// partition that is not a tenant's.
	if _, err := consentEventKey("", PurposeCorpusRetention, 0); err == nil {
		t.Error("an empty tenant produced a consent event key")
	}
	if _, _, err := consentLogPrefix(" "); err == nil {
		t.Error("a blank tenant produced a consent log prefix")
	}
}

// The sequence must stay inside the sortable width, because a wider one sorts wrongly and
// the "latest event" rule then picks the wrong event.
func TestSequenceOutsideTheSortableRangeIsRefused(t *testing.T) {
	for _, seq := range []int{-1, maxSeq + 1} {
		if _, err := consentEventKey(tenantA, PurposeCorpusRetention, seq); err == nil {
			t.Errorf("sequence %d produced a key", seq)
		}
	}
	key, err := consentEventKey(tenantA, PurposeCorpusRetention, 7)
	if err != nil {
		t.Fatalf("consentEventKey: %v", err)
	}
	purpose, seq, err := splitEventSK(key.SK)
	if err != nil {
		t.Fatalf("splitEventSK(%q): %v", key.SK, err)
	}
	if purpose != PurposeCorpusRetention || seq != 7 {
		t.Errorf("round trip produced %q/%d", purpose, seq)
	}
}

// ---------------------------------------------------------------------------
// Read surfaces
// ---------------------------------------------------------------------------

// The §11.6 check ("consent state present wherever corpus records exist") and the
// settings surface both need every recognised purpose reported, not only those present
// in the log — an omitted purpose would read as "no opinion" rather than refusal.
func TestStateReportsEveryRecognisedPurpose(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))

	state, err := r.State(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(state) != len(Purposes()) {
		t.Fatalf("State returned %d purposes, want %d", len(state), len(Purposes()))
	}
	for _, p := range Purposes() {
		d, ok := state[p]
		if !ok {
			t.Fatalf("State omitted %q", p)
		}
		if p == PurposeCorpusRetention {
			if !d.Allowed() || d.Version() != "v1" {
				t.Errorf("%q reported %s", p, d)
			}
			continue
		}
		assertRefused(t, d, ReasonPurposeAbsent)
	}
}

// State is a report, not the gate: a report that says "refused" when the truth is "the
// record could not be read" is misleading, so it surfaces the error. Resolve fails
// closed independently and does not depend on this.
func TestStateSurfacesLookupFailuresRatherThanReportingRefusal(t *testing.T) {
	r, _ := newResolver(t, nil)
	if _, err := r.State(context.Background(), tenantA); err == nil {
		t.Fatal("State reported consent state for a tenant with no record")
	}

	r2, repo := newResolver(t, baseAttrs())
	repo.FailNext(errors.New("dynamodb: throttled"))
	if _, err := r2.State(context.Background(), tenantA); err == nil {
		t.Fatal("State swallowed a storage failure")
	}
}

// State must not flatten a corrupt event into a plain refusal: "the log is corrupt" and
// "the user said no" call for different operator responses.
func TestStateReportsAMalformedEventAsMalformed(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, map[string]any{
		fieldPurpose: string(PurposeCorpusRetention),
		fieldGranted: "yes",
	})
	state, err := r.State(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	assertRefused(t, state[PurposeCorpusRetention], ReasonMalformedState)
}

func TestHistoryRejectsUnrecognisedPurpose(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	if _, err := r.History(context.Background(), tenantA, "corpus"); err == nil {
		t.Error("History accepted an unrecognised purpose")
	}
	if _, err := r.GrantedVersions(context.Background(), tenantA, "corpus"); err == nil {
		t.Error("GrantedVersions accepted an unrecognised purpose")
	}
}

// History must hand back a copy: a caller truncating the returned slice must not be able
// to shorten the purge scope the next caller sees.
func TestHistoryReturnsACopy(t *testing.T) {
	r, _ := newResolver(t, baseAttrs())
	ctx := context.Background()
	if _, err := r.Grant(ctx, tenantA, PurposeCorpusRetention, "v1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	got := history(t, r, PurposeCorpusRetention)
	got[0].Version = "tampered"
	if again := history(t, r, PurposeCorpusRetention); again[0].Version != "v1" {
		t.Error("History exposes storage the caller can mutate")
	}
}

// The enumeration here and the constants in model must stay the same set: a purpose in
// one and not the other is either an unreachable consent screen or a purpose that fails
// closed forever without anyone knowing why.
func TestPurposesMatchTheModelConstants(t *testing.T) {
	want := map[Purpose]bool{
		Purpose(model.PurposeCorpusRetention):  false,
		Purpose(model.PurposeModelImprovement): false,
	}
	for _, p := range Purposes() {
		if _, ok := want[p]; !ok {
			t.Errorf("purpose %q is not one of the model constants", p)
			continue
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("model purpose %q is missing from Purposes()", p)
		}
	}
}

// A purpose name must not contain the separator the sort key uses, or its events would
// split in the wrong place and resolve under a different purpose.
func TestPurposeNamesAreUsableInASortKey(t *testing.T) {
	for _, p := range Purposes() {
		if strings.Contains(string(p), "#") {
			t.Errorf("purpose %q contains the key separator", p)
		}
	}
}

// Purposes() must hand back a copy: a caller appending to or truncating the returned
// slice must not be able to remove a purpose from the enumeration.
func TestPurposesReturnsACopy(t *testing.T) {
	got := Purposes()
	if len(got) == 0 {
		t.Fatal("Purposes() is empty")
	}
	got[0] = "tampered"
	if Purposes()[0] == "tampered" {
		t.Error("Purposes() exposes the package's own slice")
	}
}

// A resolver with no clock cannot write a timestamped record, and I14 requires the state
// to be timestamped — so it refuses rather than writing one that would resolve as
// refused and look like corruption.
func TestRecordingWithoutAClockIsRefused(t *testing.T) {
	mem := repository.NewMemory()
	putTenant(t, mem, tenantA, baseAttrs())
	r := New(mem, nil, discardLogger())
	if _, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1"); err == nil {
		t.Error("Grant wrote an untimestamped consent record (I14)")
	}
	if _, err := r.Withdraw(context.Background(), tenantA, PurposeCorpusRetention); err == nil {
		t.Error("Withdraw wrote an untimestamped consent record (I14)")
	}
}

func TestRecordingWithoutARepositoryIsRefused(t *testing.T) {
	r := New(nil, fixedNow, discardLogger())
	if _, err := r.Grant(context.Background(), tenantA, PurposeCorpusRetention, "v1"); err == nil {
		t.Error("Grant succeeded with no repository")
	}
	if _, err := r.Withdraw(context.Background(), tenantA, PurposeCorpusRetention); err == nil {
		t.Error("Withdraw succeeded with no repository")
	}
}

// Decisions are logged in this system, so their rendering must carry no user content
// (§9.2). Purpose, reason, and version are identifiers; nothing else is exposed.
func TestDecisionRenderingCarriesOnlyIdentifiers(t *testing.T) {
	r, repo := newResolver(t, baseAttrs())
	putEvent(t, repo, tenantA, PurposeCorpusRetention, 0, eventAttrs(PurposeCorpusRetention, true, fixedTS, "v1"))
	d := r.Resolve(context.Background(), tenantA, PurposeCorpusRetention)
	for _, want := range []string{"corpus_retention", "granted", "v1"} {
		if !strings.Contains(d.String(), want) {
			t.Errorf("String() %q omits %q", d.String(), want)
		}
	}
	if d.LogValue().Kind() != slog.KindGroup {
		t.Errorf("LogValue is %v, want a group", d.LogValue().Kind())
	}
}
