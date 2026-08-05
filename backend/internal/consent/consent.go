// Package consent resolves and records per-purpose consent (I14).
//
// I14: "No user content is retained for model training or corpus building without a
// recorded, timestamped consent state. **Absence of consent is treated as refusal.**"
// §Phase 0's deliverable is "consent state on the tenant record, with a resolver that
// fails closed", and it is in Phase 0 because it is irreversible: "consent obtained at
// collection time is clean; consent sought retroactively across a user base is
// expensive and often simply unavailable" (§2A.1).
//
// # Why the resolver does not return (bool, error)
//
// The obvious signature is `Granted(...) (bool, error)`, and it is the wrong one. It
// invites `granted, _ := ...; if granted {` at the one call site where an ignored error
// is a data-protection incident rather than a bug — and it invites the mirror-image
// mistake, a caller that treats a storage failure as a reason to abort the whole
// request when the correct response is simply not to retain the corpus triple.
//
// So Resolve returns a Decision and no error:
//
//   - Decision's fields are unexported and no exported constructor produces a granted
//     one, so **only this package can say yes**, and only after reading a stored grant
//     that passes every check in evaluate.
//   - The zero Decision is refusal. A forgotten assignment, a nil-map read, a struct
//     that never got populated — every one of those denies.
//   - There is no error to ignore. Every failure — invalid tenant, missing tenant
//     record, empty log, unparseable event, repository error — is folded into the
//     Decision as a refusal carrying a Reason, and the underlying error stays reachable
//     through Err() for logging and alerting without ever becoming an allow.
//
// Refusing on a storage error loses nothing that cannot be recovered: §Phase 4 keeps
// the audio and the L0 transcript regardless, so a corpus triple not written today can
// be written later, whereas a triple written without consent cannot be un-written.
//
// # One purpose per question
//
// Resolve takes exactly one Purpose and there is no "is consent granted" call that
// takes none. §Phase 4 draws a distinction that a single global boolean would erase:
//
//	"If consent is absent, the correction rules still work — they are derived
//	in-flight and only the rule is persisted, not the source pair. This distinction
//	matters: rule derivation is operating the service the user asked for; corpus
//	retention is a separate purpose requiring separate consent."
//
// Purpose is a named type over the enumerated purposes rather than a bare string, so an
// unrecognised purpose is a refusal (ReasonUnknownPurpose) instead of a lookup that
// happens to miss — a typo fails closed rather than resolving against a key nobody
// wrote.
//
// # Consent is an append-only log, not a field
//
// **This is the structural core of the package, and the first version got it wrong.**
//
// The obvious storage is the `consent` map on the tenant record (§6.3), updated in
// place. The repository seam has Get, Put, PutOnce, QueryPrefix and Delete (§6.3) and no
// conditional update, so "updated in place" means read-modify-write of the whole tenant
// item — and a read-modify-write has a window. Two demonstrated consequences, neither
// detectable afterwards:
//
//   - Grant reads the record, a concurrent Withdraw completes and returns "refused" to
//     the user, then Grant writes its stale snapshot. Retention continues after the user
//     was told consent was withdrawn, and the withdrawal event is gone from the record,
//     so nothing — not the §Phase 4 purge, not §11.6's "consent state present wherever
//     corpus records exist" check — can ever discover that it happened.
//   - Writing one purpose erases another purpose's entry outright. This needs no second
//     human: one PATCH /v1/settings handler applying two purposes concurrently does it
//     inside a single request, leaving corpus records already stamped with the erased
//     purpose's version with no consent state at all.
//
// Both are fail-open outcomes in the one component whose entire job is to fail closed.
// The fix is not a lock and not a new repository primitive: **every grant and every
// withdrawal is its own item, written with PutOnce**, which has no read-modify-write
// window at all. The sequence number is part of the sort key, so two concurrent writers
// contend for the same key, one gets ErrAlreadyExists, re-reads, and appends after the
// winner. Nothing is overwritten because nothing is ever written twice.
//
// The state in force for a purpose is the latest event in its log. That is also exactly
// what §Phase 4 needs independently — "a later consent withdrawal must be able to
// identify and purge exactly the affected records" is only answerable from a history
// that cannot lose an event, and GrantedVersions reads it.
//
// The audit log (I13) is not a substitute: audit records carry a TTL of about seven
// years (§6.3), and the set of versions ever granted has to outlive any TTL for as long
// as a single corpus record stamped with one survives. These records carry no TTL.
// Handlers still write their audit record for a consent change; this log exists for the
// gate and the purge.
//
// # What happened to the `consent` attribute on the tenant record
//
// §6.3 lists a `consent` map on the Tenant row and model.Tenant has a field for it.
// This package no longer reads it for any decision and never writes it, because it
// cannot be kept truthful without the conditional write the repository does not have:
// maintaining it as a projection reintroduces exactly the lost update above, and a
// projection that is stale in the "still granted" direction is a fail-open trap for any
// future reader that trusts it. An attribute this package cannot keep correct is worse
// than an absent one, because absent reads as refusal (§6.3: "absent purpose =
// refused") and stale reads as consent.
//
// It is still *noticed*: finding the attribute present logs a WARN naming the purposes
// it mentions, because nothing in this system writes it, so its presence means
// out-of-band mutation of backend state (I16). It is deliberately not honoured, not even
// as a veto — see loadLog.
//
// Consequence to know about: a tenant whose consent exists only in that attribute
// resolves as refused under this package. That is the safe direction, and moving such a
// tenant onto the log is a migration script (§11), not something to infer at read time.
package consent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// ---------------------------------------------------------------------------
// Purposes
// ---------------------------------------------------------------------------

// Purpose is one consent purpose (I14). A named type, so a caller cannot pass an
// arbitrary string and have it resolve; anything outside the enumeration is refused.
type Purpose string

// The recognised purposes. Declared from the model constants rather than re-typed, so
// the stored attribute values and these cannot drift apart (§6.3).
const (
	// PurposeCorpusRetention gates persisting the (audio, L0, L2) triples of §Phase 4.
	// It does **not** gate deriving a correction rule: that is operating the service
	// the user asked for and needs no separate consent.
	PurposeCorpusRetention Purpose = model.PurposeCorpusRetention

	// PurposeModelImprovement gates any future training use.
	PurposeModelImprovement Purpose = model.PurposeModelImprovement
)

// purposes is the complete recognised set, in the order settings surfaces should show
// them.
var purposes = []Purpose{PurposeCorpusRetention, PurposeModelImprovement}

// Purposes returns every recognised purpose, for GET /v1/settings (§6.6) so that the
// consent UI enumerates purposes from one place rather than hardcoding a list that can
// silently omit a new one.
func Purposes() []Purpose {
	out := make([]Purpose, len(purposes))
	copy(out, purposes)
	return out
}

func (p Purpose) known() bool {
	for _, q := range purposes {
		if p == q {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Decision
// ---------------------------------------------------------------------------

// Reason records why a Decision came out the way it did.
//
// Every value other than ReasonGranted is a refusal. They are distinguished because
// "the user declined" and "we could not read the record" call for different operator
// responses, and because a settings surface showing "never asked" is not the same as
// one showing "declined" — but they are identical in effect, which is what I14 requires.
type Reason string

const (
	// ReasonGranted is the only value on which Allowed() is true.
	ReasonGranted Reason = "granted"

	// ReasonRefused is a recorded granted:false — either an explicit decline or a
	// withdrawal of an earlier grant.
	ReasonRefused Reason = "refused"

	// ReasonPurposeAbsent is a tenant with consent events for other purposes but none
	// for this one. §6.3: "Absent purpose = refused."
	ReasonPurposeAbsent Reason = "purpose_absent"

	// ReasonNoConsentState is a tenant with no consent events at all — the user has
	// not been asked. Refusal, not a default (I14).
	ReasonNoConsentState Reason = "no_consent_state"

	// ReasonNoTenantRecord is no tenant record. Consent belongs to a provisioned
	// tenant, and this is also what a cross-tenant probe produces, since every key is
	// tenant-scoped (I11).
	ReasonNoTenantRecord Reason = "no_tenant_record"

	// ReasonInvalidTenant is an empty or malformed tenant. Fails closed here as well
	// as in the key helper, so neither is the only line of defence (I11).
	ReasonInvalidTenant Reason = "invalid_tenant"

	// ReasonUnknownPurpose is a purpose outside the enumeration — a typo, or a purpose
	// removed from the set. Never resolves against stored state.
	ReasonUnknownPurpose Reason = "unknown_purpose"

	// ReasonLookupFailed is a storage error. Refusal: a resolver that cannot read
	// consent has not got consent.
	ReasonLookupFailed Reason = "lookup_failed"

	// ReasonMalformedState is a stored consent event that does not decode. Deliberately
	// not coerced — a "true" string or a numeric 1 is a record written by something
	// that did not agree with this package about the schema, and guessing its intent is
	// how a refusal becomes a grant.
	ReasonMalformedState Reason = "malformed_state"

	// ReasonAmbiguousHistory is a log whose ordering cannot be trusted: a gap in the
	// sequence, which means an event was destroyed. The latest event might still be
	// determinate, but the version list behind the §Phase 4 purge is not, and a purge
	// that quietly misses records is worse than one that refuses to run.
	ReasonAmbiguousHistory Reason = "ambiguous_history"

	// ReasonTooManyEvents is a log past the ceiling this package will read in one
	// query. Refusal rather than a truncated read: a truncated log loses the newest
	// events, and losing a withdrawal is the fail-open case.
	ReasonTooManyEvents Reason = "too_many_events"

	// ReasonMissingVersion is granted:true with no version. Refused, because §Phase 4
	// requires a later withdrawal to "identify and purge exactly the affected
	// records" and a triple stamped with an empty version cannot be found again.
	ReasonMissingVersion Reason = "missing_version"

	// ReasonMalformedVersion is granted:true with a version that is not a token
	// (versionRe). Refused on the read side and not only on the write side: the purge
	// selects records by exact string match on this value, and a version carrying
	// whitespace or quoting is one a purge script will eventually fail to match — so
	// nothing may be stamped with it.
	ReasonMalformedVersion Reason = "malformed_version"

	// ReasonMissingTimestamp is a consent event with no timestamp. I14 requires a
	// "recorded, timestamped consent state"; an untimestamped one does not satisfy it,
	// and that holds for a recorded refusal as much as for a grant.
	ReasonMissingTimestamp Reason = "missing_timestamp"

	// ReasonMalformedTimestamp is a timestamp that is not RFC3339 in UTC. Refused
	// rather than accepted-and-ignored: the timestamp is the evidence of when consent
	// was given, and unparseable evidence is no evidence. UTC specifically, because
	// every other timestamp in this system is clock.RFC3339UTC and one carrying a local
	// offset sorts wrongly against all of them (see keys.GSI1).
	ReasonMalformedTimestamp Reason = "malformed_timestamp"
)

// reasonNone is the absence of a reason — the zero Decision's reason.
//
// Returned by the lookup helpers on success. Deliberately not ReasonGranted: in a
// package whose central claim is that only evaluate() produces a granted verdict, a
// second producer of that value is a future early return away from a Decision that
// denies while reporting reason="granted" to a dashboard that branches on the reason
// rather than on Allowed().
const reasonNone Reason = ""

// Decision is the answer to "may this tenant's content be retained for this purpose".
//
// All fields are unexported and no exported constructor yields a granted Decision, so
// **the only source of a yes is a stored grant that passed every check in evaluate**.
// The zero value is a refusal with an empty reason, which is what makes an
// unassigned or partially-built Decision safe.
type Decision struct {
	purpose Purpose
	granted bool
	version string
	ts      string
	reason  Reason

	// err is the cause when the refusal came from a failure rather than from the
	// user's answer. Exposed for logging and alerting; it can never flip granted.
	err error
}

// Allowed reports whether retention for this purpose is permitted. False for the zero
// value, and false for every failure.
func (d Decision) Allowed() bool { return d.granted }

// Purpose reports which purpose was asked about.
func (d Decision) Purpose() Purpose { return d.purpose }

// Version returns the consent version to stamp onto records retained under this
// decision, and is empty unless Allowed().
//
// Empty on a refusal by design: the version of a refusal is not a version anything may
// be stored under, and returning one would let a caller that skipped Allowed() stamp a
// plausible-looking version onto a record it had no consent to write. The purge path
// wants GrantedVersions, not this.
func (d Decision) Version() string {
	if !d.granted {
		return ""
	}
	return d.version
}

// GrantedAt returns the timestamp of the grant, empty unless Allowed(). For display in
// the settings surface (§6.6).
func (d Decision) GrantedAt() string {
	if !d.granted {
		return ""
	}
	return d.ts
}

// Reason reports why. Always populated except on the zero value.
func (d Decision) Reason() Reason { return d.reason }

// Err returns the underlying failure for ReasonLookupFailed and the malformed reasons,
// or nil. A caller may log or alert on it; it cannot make the decision permissive.
func (d Decision) Err() error { return d.err }

// String renders the decision for diagnostics. Content-free: a purpose, a verdict, a
// reason, and a version are identifiers, never user speech (§9.2).
func (d Decision) String() string {
	verdict := "refused"
	if d.granted {
		verdict = "granted"
	}
	s := fmt.Sprintf("consent(%s)=%s reason=%s", d.purpose, verdict, d.reason)
	if d.granted {
		s += " version=" + d.version
	}
	return s
}

// LogValue makes a Decision safe to log directly.
//
// §9.2's usual leak is a struct dumped at debug level, so the dump is defined here and
// contains no content — rather than left to whatever a call site reaches for.
func (d Decision) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("purpose", string(d.purpose)),
		slog.Bool("allowed", d.granted),
		slog.String("reason", string(d.reason)),
	}
	if d.granted {
		attrs = append(attrs, slog.String("version", d.version))
	}
	return slog.GroupValue(attrs...)
}

// refuse builds a refusal. The only Decision constructor that takes an arbitrary
// reason, and it cannot produce a grant.
func refuse(p Purpose, r Reason, err error) Decision {
	return Decision{purpose: p, reason: r, err: err}
}

// ---------------------------------------------------------------------------
// Stored shape (§6.3)
// ---------------------------------------------------------------------------

// Attribute and field names. `consent` on the tenant record is the map §6.3 specifies;
// this package only ever looks for its presence, never at its meaning (see the package
// comment).
const (
	attrConsent = "consent"

	fieldGranted = "granted"
	fieldTS      = "ts"
	fieldVersion = "version"
	fieldPurpose = "purpose"
)

// consentEventSKPrefix and seqWidth define the sort key of one consent event:
// {prefix}{purpose}#{seq:06d}, in the tenant's partition.
//
// **This is the one key segment in this system assembled outside internal/keys, and it
// is a temporary state, not a licence.** The constructor belongs there as
// keys.ConsentEvent(tenant, purpose, seq); this pass may not touch that package because
// sibling agents are editing it concurrently and a collision there corrupts every other
// package's build. Two things keep the exception honest until it is moved:
//
//   - The partition key comes from keys.Tenant and is never assembled here, so tenant
//     validation and tenant scoping remain that package's monopoly (I11). There is no
//     code path in this file that reaches storage without a key built from keys.Tenant.
//   - Only the sort-key suffix is local, and it lives in exactly one function
//     (consentEventKey) so the move is a one-line change rather than a search.
//
// seqWidth is zero-padded for the same reason keys.Segment zero-pads: DynamoDB range
// keys sort lexicographically only, so without the padding event 10 sorts before event
// 2 and the "latest event wins" rule silently picks the wrong one — which for a
// withdrawal followed by nine grants means retention continuing after a withdrawal.
const (
	consentEventSKPrefix = "CONSENT#"
	seqWidth             = 6
	maxSeq               = 999999
)

// Ceilings on the log.
//
// Nothing bounds how many times a client can toggle a settings switch, and the previous
// design appended every event onto the tenant item, where it shared the 400KB DynamoDB
// item ceiling with kms_key_id — so the operation that eventually became impossible was
// *withdrawal*. One item per event removes that failure entirely, and these ceilings
// exist for a different reason: Resolve reads the whole log for a purpose, so the log
// has to stay something a gate can read.
//
// The asymmetry is deliberate. At maxEventsPerPurpose a *grant* is refused, and a
// withdrawal is still recorded — refusing to grant is the fail-closed direction, and
// after a withdrawal a repeat withdrawal is a no-op, so growth stops. A ceiling that
// blocked withdrawal would be the bug this package exists to prevent.
const (
	maxEventsPerPurpose = 1024
	maxEventsTotal      = 4096
	maxAppendAttempts   = 8
)

// versionRe constrains a consent version to a token.
//
// The version is not part of a DynamoDB key, so this is not the injection concern the
// key helper has. It is a selector: the §Phase 4 purge finds records by the version
// stamped on them, and a version carrying whitespace or punctuation that a script has
// to quote is a version a purge will eventually fail to match. Enforced on write (Grant)
// **and** on read (evaluate) — enforcing it only on write leaves the read side handing a
// caller a version to stamp that Grant itself would have rejected.
var versionRe = regexp.MustCompile(`^[A-Za-z0-9._:+-]{1,64}$`)

// rfc3339UTCRe restricts a stored timestamp to the one representation this system
// writes (clock.RFC3339UTC), mirroring keys.validateRFC3339UTC. Not called across the
// package boundary because that helper is unexported there; kept identical on purpose,
// and asserted against clock.RFC3339UTC in the tests.
var rfc3339UTCRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// Event is one recorded consent change — one stored item, never modified after it is
// written.
type Event struct {
	Purpose Purpose `json:"purpose"`
	Granted bool    `json:"granted"`
	TS      string  `json:"ts"`

	// Version is the version granted, or for a withdrawal the version being withdrawn
	// from. Empty when a refusal was recorded without any prior grant.
	Version string `json:"version"`

	// Seq is this event's position in its purpose's log, taken from the sort key.
	//
	// Not stored as an attribute as well: two copies of the same fact can disagree, and
	// the sort key is the one DynamoDB orders by, so it is the one that decides.
	Seq int `json:"seq"`
}

// consentEventKey builds the key for one consent event. See consentEventSKPrefix for
// why the suffix is assembled here and why the partition key is not.
func consentEventKey(tenant keys.TenantID, purpose Purpose, seq int) (keys.DynamoKey, error) {
	key, err := keys.Tenant(tenant)
	if err != nil {
		return keys.DynamoKey{}, fmt.Errorf("consent: %w", err)
	}
	if !purpose.known() {
		// A purpose outside the enumeration must not reach a key: it would create a log
		// nothing can resolve against, since Resolve refuses an unknown purpose before
		// it reads anything.
		return keys.DynamoKey{}, fmt.Errorf("consent: purpose %q is not recognised, so it has no consent log", purpose)
	}
	if seq < 0 || seq > maxSeq {
		return keys.DynamoKey{}, fmt.Errorf("consent: event sequence %d is outside the %d-digit sortable range", seq, seqWidth)
	}
	key.SK = fmt.Sprintf("%s%s#%0*d", consentEventSKPrefix, purpose, seqWidth, seq)
	return key, nil
}

// consentLogPrefix returns the partition key and sort-key prefix covering every consent
// event for a tenant, across purposes.
//
// One query for all purposes rather than one per purpose: it is what lets a resolver
// distinguish "no consent state at all" from "this purpose was never asked about", which
// §6.6's settings surface shows differently even though I14 makes them identical in
// effect.
func consentLogPrefix(tenant keys.TenantID) (string, string, error) {
	key, err := keys.Tenant(tenant)
	if err != nil {
		return "", "", fmt.Errorf("consent: %w", err)
	}
	return key.PK, consentEventSKPrefix, nil
}

// eventItem renders one event as a stored item.
func eventItem(tenant keys.TenantID, ev Event) (repository.Item, error) {
	key, err := consentEventKey(tenant, ev.Purpose, ev.Seq)
	if err != nil {
		return repository.Item{}, err
	}
	return repository.Item{
		Key: key,
		Attrs: map[string]any{
			// Duplicated from the sort key deliberately, and cross-checked on read: an
			// item whose attribute and key disagree about its purpose was written by
			// something that is not this package, and resolving it under either reading
			// is a guess about whose consent it records.
			fieldPurpose: string(ev.Purpose),
			fieldGranted: ev.Granted,
			fieldTS:      ev.TS,
			fieldVersion: ev.Version,
		},
		// No TTL. The set of versions ever granted must outlive every other retention
		// window in the system, because a corpus record stamped with one can outlive
		// them (§Phase 4) — and an expiring consent record is a consent state that
		// silently becomes "never asked".
		//
		// No GSI1 attributes either: §6.3 makes the index sparse and only Capture and
		// Thread records may project into it.
	}, nil
}

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

// Resolver reads and records consent.
type Resolver struct {
	repo repository.Repository
	clk  clock.Clock
	log  *slog.Logger
}

// New builds a Resolver.
//
// Missing dependencies are tolerated rather than panicked on, because Resolve promises
// never to fail: a resolver wired with a nil repository refuses with
// ReasonLookupFailed and logs it, which is the same fail-closed outcome as a broken
// table and is preferable to a panic in a Lambda handler.
func New(repo repository.Repository, clk clock.Clock, log *slog.Logger) *Resolver {
	return &Resolver{repo: repo, clk: clk, log: log}
}

func (r *Resolver) logger() *slog.Logger {
	if r == nil || r.log == nil {
		return slog.Default()
	}
	return r.log
}

// Resolve answers whether one purpose is consented for one tenant. It never returns an
// error, and it never returns a grant it did not read (see the package comment).
//
// This is the gate §Phase 4 requires to be "checked before the first triple is
// written, not at read time".
func (r *Resolver) Resolve(ctx context.Context, tenant keys.TenantID, purpose Purpose) Decision {
	d := r.decide(ctx, tenant, purpose)

	// A refusal caused by a failure is logged, because it is operationally different
	// from the user having declined: nobody will otherwise discover that the corpus
	// stopped accumulating for a reason the user did not choose. WARN rather than
	// ERROR — nothing is lost (§Phase 4 keeps audio and L0 either way), so this does
	// not warrant paging.
	switch d.reason {
	case ReasonLookupFailed, ReasonMalformedState, ReasonAmbiguousHistory,
		ReasonTooManyEvents, ReasonMissingVersion, ReasonMalformedVersion,
		ReasonMissingTimestamp, ReasonMalformedTimestamp, ReasonInvalidTenant,
		ReasonUnknownPurpose, ReasonNoTenantRecord:
		logging.FromContext(ctx, r.logger()).Warn("consent could not be established; treating as refusal (I14)",
			slog.Any("decision", d),
			// ErrorAttr, not the message: a consent event holds no transcript text, but
			// an AWS SDK validation error echoes attribute values back, and the error
			// type alone is what makes this actionable (§9.2).
			logging.ErrorAttr(d.err))
	}
	return d
}

// decide is Resolve without the logging.
//
// Split out so that every return path is a Decision and the logging decision is taken in
// exactly one place: a refusal path that forgot to log would be invisible, and a refusal
// path added later cannot forget, because it cannot return without passing through
// Resolve.
func (r *Resolver) decide(ctx context.Context, tenant keys.TenantID, purpose Purpose) Decision {
	if !purpose.known() {
		return refuse(purpose, ReasonUnknownPurpose,
			fmt.Errorf("consent: purpose %q is not recognised; the enumeration in this package is the complete set (I14)", purpose))
	}
	log, reason, err := r.loadLog(ctx, tenant)
	if err != nil {
		return refuse(purpose, reason, err)
	}
	return log.resolve(purpose)
}

// evaluate turns one stored consent state into a Decision.
//
// **The single place a granted Decision is produced.** Grant() reads its own write back
// through this same function rather than reporting success directly, so a grant that
// would not resolve as granted — no version, no timestamp, a version the purge could not
// match — cannot be reported as granted by the call that wrote it.
//
// The timestamp is checked before the granted flag, so a *recorded refusal* is held to
// the same standard: I14 requires a "recorded, timestamped consent state", and an
// untimestamped refusal is not one. It refuses either way; the difference is that
// ReasonMissingTimestamp says the record is broken and ReasonRefused says the user
// declined, and the idempotency rule in record() only treats the second as settled.
func evaluate(purpose Purpose, g model.ConsentGrant) Decision {
	if strings.TrimSpace(g.TS) == "" {
		return refuse(purpose, ReasonMissingTimestamp,
			fmt.Errorf("consent: state for %q carries no timestamp; I14 requires a recorded, timestamped consent state", purpose))
	}
	if !rfc3339UTCRe.MatchString(g.TS) {
		return refuse(purpose, ReasonMalformedTimestamp,
			fmt.Errorf("consent: state for %q has timestamp %q, which is not RFC3339 in UTC; every timestamp in this system is clock.RFC3339UTC and a local offset sorts wrongly against all of them", purpose, g.TS))
	}
	if _, err := time.Parse(time.RFC3339, g.TS); err != nil {
		// The pattern admits 2026-02-31; time.Parse is what rejects a date that does not
		// exist. Both are needed: the pattern pins the zone, the parse pins the value.
		return refuse(purpose, ReasonMalformedTimestamp,
			fmt.Errorf("consent: state for %q has timestamp %q which is not a real instant: %w", purpose, g.TS, err))
	}
	if !g.Granted {
		return refuse(purpose, ReasonRefused, nil)
	}
	if strings.TrimSpace(g.Version) == "" {
		return refuse(purpose, ReasonMissingVersion,
			fmt.Errorf("consent: grant for %q carries no version, so records retained under it could never be identified for purge (§Phase 4)", purpose))
	}
	if !versionRe.MatchString(g.Version) {
		return refuse(purpose, ReasonMalformedVersion,
			fmt.Errorf("consent: grant for %q has version %q, which is not a token matching %s; the purge selects records by exact match on this value, so nothing may be stamped with it", purpose, g.Version, versionRe.String()))
	}
	return Decision{
		purpose: purpose,
		granted: true,
		version: g.Version,
		ts:      g.TS,
		reason:  ReasonGranted,
	}
}

// State returns a Decision for every recognised purpose.
//
// The read surface for GET /v1/settings (§6.6) and for verify.sh's "consent state
// present wherever corpus records exist" check (§11.6). Decisions rather than booleans,
// so a reporting surface cannot lose the distinction Resolve preserves.
//
// Unlike Resolve this returns an error, and the asymmetry is deliberate: Resolve is a
// gate and must fail closed silently, whereas State is a report, and a report that says
// "refused" when the truth is "the record could not be read" is a misleading report.
// The gate does not depend on this method, so surfacing the error here costs nothing.
func (r *Resolver) State(ctx context.Context, tenant keys.TenantID) (map[Purpose]Decision, error) {
	log, _, err := r.loadLog(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make(map[Purpose]Decision, len(purposes))
	for _, p := range purposes {
		out[p] = log.resolve(p)
	}
	return out, nil
}

// History returns the recorded consent events for one purpose, in the order they were
// recorded.
//
// Ordered by sequence number, never re-sorted by timestamp: timestamps are
// second-precision (clock.RFC3339UTC), so two events in the same second would reorder
// arbitrarily, whereas the sequence is the order that actually happened — it is the
// order PutOnce serialised them into.
func (r *Resolver) History(ctx context.Context, tenant keys.TenantID, purpose Purpose) ([]Event, error) {
	if !purpose.known() {
		return nil, fmt.Errorf("consent: purpose %q is not recognised", purpose)
	}
	log, _, err := r.loadLog(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if f := log.fault(purpose); f != nil {
		// An unreadable log is an error rather than a partial answer. Silently dropping
		// an event loses a version that the §Phase 4 purge would then never visit, and a
		// purge that quietly misses records is worse than one that refuses to run.
		return nil, fmt.Errorf("consent: consent log for %q is not readable (%s): %w", purpose, f.reason, f.err)
	}
	events := log.events[purpose]
	out := make([]Event, len(events))
	copy(out, events)
	return out, nil
}

// GrantedVersions returns every version this purpose was ever granted under, in the
// order first seen.
//
// This is what makes the §Phase 4 purge possible: "a later consent withdrawal must be
// able to identify and purge exactly the affected records", and the affected records are
// exactly those stamped with one of these versions. Reading the state in force instead
// would miss every version superseded by a later grant.
//
// A withdrawal event's version counts. Withdraw stamps the version it withdrew from
// onto the event precisely so the most recent affected version is recoverable, and the
// previous implementation filtered those events out — turning a datum recorded on
// purpose into ([], nil), which is indistinguishable from "never granted" and would have
// driven a purge that deleted nothing while every affected record survived.
//
// It returns an error, never a short list, for every case where the log cannot be read.
// The empty slice means one thing only: this purpose was never granted.
func (r *Resolver) GrantedVersions(ctx context.Context, tenant keys.TenantID, purpose Purpose) ([]string, error) {
	events, err := r.History(ctx, tenant, purpose)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := make(map[string]bool, len(events))
	for _, ev := range events {
		// A version is skipped only when there is none. A version that fails versionRe
		// is still returned: the records stamped with it exist, and a purge that cannot
		// match it needs to be told about it rather than left unaware.
		if ev.Version == "" || seen[ev.Version] {
			continue
		}
		seen[ev.Version] = true
		out = append(out, ev.Version)
	}
	return out, nil
}

// Grant records consent for one purpose under one version.
//
// Returns the Decision a subsequent Resolve produces, read back from storage after the
// write rather than reported from the value written — so a concurrent withdrawal that
// landed after this grant is reflected as a refusal instead of being papered over. A
// non-nil error means the change may not have been recorded; the Decision is then never
// permissive.
//
// The caller is the handler, which also writes the audit record for the change (I13);
// the log appended here serves the gate and the purge, not the audit trail.
func (r *Resolver) Grant(ctx context.Context, tenant keys.TenantID, purpose Purpose, version string) (Decision, error) {
	if !purpose.known() {
		return Decision{}, fmt.Errorf("consent: cannot grant unrecognised purpose %q", purpose)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		// Refusing an unversioned grant at the write is what keeps the read path's
		// ReasonMissingVersion a defence-in-depth check rather than the only one: a
		// grant with no version is unpurgeable, so it must not be recordable.
		return Decision{}, fmt.Errorf("consent: a grant needs the consent version it was collected under; a record retained under an unversioned grant could never be purged (§Phase 4)")
	}
	if !versionRe.MatchString(version) {
		return Decision{}, fmt.Errorf("consent: version %q is not a token matching %s; the purge selects records by this value", version, versionRe.String())
	}
	return r.record(ctx, tenant, purpose, true, version)
}

// Withdraw records a withdrawal or an explicit refusal.
//
// Takes no version: the version withdrawn from is read from the state in force, not
// supplied by the caller, because a caller passing the wrong one would corrupt the
// record of what was granted when — and that record is the only thing making the purge
// exact.
//
// Withdrawal never deletes the record of the earlier grant. It appends. Corpus records
// collected under the withdrawn version still exist at this point and are found through
// GrantedVersions; the purge that removes them is a separate, separately permissioned
// operation (§9.3, G-038).
func (r *Resolver) Withdraw(ctx context.Context, tenant keys.TenantID, purpose Purpose) (Decision, error) {
	if !purpose.known() {
		return Decision{}, fmt.Errorf("consent: cannot withdraw unrecognised purpose %q", purpose)
	}
	return r.record(ctx, tenant, purpose, false, "")
}

// record is the single write path for both Grant and Withdraw.
//
// Append-only: one PutOnce per event, at the next free sequence number. There is no
// read-modify-write of anything, so there is no window in which one writer's change can
// revert another's — which is what the previous implementation did, silently turning an
// acknowledged withdrawal back into a grant.
//
// A lost race is not an error. ErrAlreadyExists means a concurrent writer took this
// sequence number, so the loop re-reads and tries again: the retry re-derives the
// idempotency answer and the version-withdrawn-from against the *winner's* state, which
// is what makes two concurrent changes serialise rather than one overwriting the other.
func (r *Resolver) record(ctx context.Context, tenant keys.TenantID, purpose Purpose, granted bool, version string) (Decision, error) {
	if r == nil || r.repo == nil {
		return Decision{}, fmt.Errorf("consent: resolver has no repository")
	}
	if r.clk == nil {
		// I14 requires a *timestamped* state, so no clock means no valid record to
		// write. Refusing beats writing an untimestamped grant that would resolve as
		// refused anyway and look like corruption.
		return Decision{}, fmt.Errorf("consent: resolver has no clock; a consent record must be timestamped (I14)")
	}

	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		log, _, err := r.loadLog(ctx, tenant)
		if err != nil {
			// Includes the not-found case. Consent belongs to a provisioned tenant
			// (§6.3) and this package does not create one: a tenant record it invented
			// would have no kms_key_id, and §6.3 requires that attribute never be
			// absent (I8).
			return Decision{}, fmt.Errorf("consent: recording consent for %q: %w", purpose, err)
		}

		inForce := log.resolve(purpose)

		// Idempotent no-op when the state in force **already resolves to** the requested
		// outcome. Compared against the resolved Decision, not against two fields of the
		// stored entry: an entry that decodes but does not resolve — a grant with no
		// timestamp, a version the purge could not match — must not short-circuit, or
		// the user clicks "I consent", the call returns success, nothing is written, and
		// consent for that purpose becomes permanently unobtainable through the product.
		//
		// Without the no-op a client re-sending the same settings PATCH grows the log
		// without limit.
		if granted {
			if inForce.Allowed() && inForce.Version() == version {
				return inForce, nil
			}
		} else if inForce.Reason() == ReasonRefused {
			return inForce, nil
		}

		// A log that cannot be read is not something to append onto. Appending would
		// leave it unreadable, so the gate would go on refusing while the caller was told
		// the change was recorded — a false success, which is worse than a refusal.
		// Nothing is lost by refusing: an unreadable log already resolves as a refusal,
		// so a withdrawal is already in effect; what cannot happen is a grant. Repair is
		// an operational script (I16), which is the only thing that should be rewriting
		// consent records.
		switch inForce.Reason() {
		case ReasonMalformedState, ReasonAmbiguousHistory, ReasonTooManyEvents:
			return Decision{}, fmt.Errorf("consent: cannot record consent for %q: the consent log is not readable (%s); repair it with an operational script rather than appending onto it (I16): %w",
				purpose, inForce.Reason(), inForce.Err())
		}

		events := log.events[purpose]
		if !granted && len(events) > 0 {
			// Carry the version being withdrawn from onto the withdrawal event, so the
			// most recent affected version is recoverable from the state in force without
			// walking the whole log. Taken from the latest event whatever it says, not
			// only from a grant: a grant that failed to resolve still stamped records
			// with its version if anything ran before it stopped resolving.
			version = events[len(events)-1].Version
		}
		if granted && len(events) >= maxEventsPerPurpose {
			return Decision{}, fmt.Errorf("consent: cannot grant %q: its consent log already holds %d events, the ceiling a gate will read in one query; withdrawal is still available and archiving the log is an operational script (I16)",
				purpose, len(events))
		}

		// The log is dense (loadLog refuses a gap), so the next free sequence is its
		// length. Deriving it from the length rather than from lastSeq+1 means a log that
		// somehow disagreed with itself collides on PutOnce instead of skipping a number.
		seq := len(events)
		ts := clock.RFC3339UTC(r.clk.Now())
		ev := Event{Purpose: purpose, Granted: granted, TS: ts, Version: version, Seq: seq}

		item, err := eventItem(tenant, ev)
		if err != nil {
			return Decision{}, fmt.Errorf("consent: recording consent for %q: %w", purpose, err)
		}
		err = r.repo.PutOnce(ctx, item)
		if errors.Is(err, repository.ErrAlreadyExists) {
			continue
		}
		if err != nil {
			// The Decision stays the zero value — a failed write must never be reported
			// as a grant, and the caller's `if d.Allowed()` is then correct even if it
			// ignores the error.
			return Decision{}, fmt.Errorf("consent: writing consent event for %q: %w", purpose, err)
		}

		logging.FromContext(ctx, r.logger()).Info("consent recorded",
			slog.String("purpose", string(purpose)),
			slog.Bool("granted", granted),
			slog.String("version", version),
			slog.Int("seq", seq))

		// Read the state back through the gate rather than returning evaluate(ev): a
		// concurrent withdrawal may have appended a later event between the PutOnce and
		// here, and reporting "granted" then is exactly the fail-open the append-only log
		// exists to prevent.
		after := r.Resolve(ctx, tenant, purpose)
		if err := after.Err(); err != nil && !after.Allowed() {
			switch after.Reason() {
			case ReasonLookupFailed, ReasonNoTenantRecord, ReasonInvalidTenant:
				// The write landed but the resulting state could not be read. Report both,
				// so a handler does not record "consent granted" in its audit entry on the
				// strength of an unconfirmed read.
				return after, fmt.Errorf("consent: recorded consent for %q but could not read the resulting state back: %w", purpose, err)
			}
		}
		return after, nil
	}
	return Decision{}, fmt.Errorf("consent: recording consent for %q: gave up after %d attempts, each losing the next sequence number to a concurrent writer", purpose, maxAppendAttempts)
}

// loadTenant reads the tenant record, returning the Reason a failure maps to so callers
// do not each invent their own.
func (r *Resolver) loadTenant(ctx context.Context, tenant keys.TenantID) (*repository.Item, Reason, error) {
	if r == nil || r.repo == nil {
		return nil, ReasonLookupFailed, fmt.Errorf("consent: resolver has no repository")
	}
	// Through the key helper, so this path is tenant-scoped like every other (I11).
	// An empty tenant is refused here rather than reading some other partition.
	key, err := keys.Tenant(tenant)
	if err != nil {
		return nil, ReasonInvalidTenant, fmt.Errorf("consent: %w", err)
	}
	item, err := r.repo.Get(ctx, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ReasonNoTenantRecord, fmt.Errorf("consent: no tenant record: %w", err)
		}
		return nil, ReasonLookupFailed, fmt.Errorf("consent: reading tenant record: %w", err)
	}
	if item == nil {
		// A repository returning (nil, nil) would otherwise panic below. Fails closed
		// instead, because a resolver that cannot be crashed by a misbehaving
		// implementation is one less way to reach a permissive outcome.
		return nil, ReasonLookupFailed, fmt.Errorf("consent: repository returned no tenant record and no error")
	}
	return item, reasonNone, nil
}

// ---------------------------------------------------------------------------
// The log
// ---------------------------------------------------------------------------

// fault is a reason a purpose cannot be resolved from its log, with the cause.
//
// Carried per purpose rather than failing the whole read, because one unreadable event
// must not stop a *different* purpose resolving — but it must also not be flattened into
// a plain refusal, or "the log is corrupt" would be indistinguishable in a log line from
// "the user said no".
type fault struct {
	reason Reason
	err    error
}

// consentLog is one tenant's decoded consent state.
type consentLog struct {
	// events holds each purpose's events in sequence order. A purpose with a fault may
	// still have entries here; resolve reports the fault instead of using them.
	events map[Purpose][]Event

	faults map[Purpose]fault

	// global is a fault that cannot be attributed to one purpose — an event whose sort
	// key does not parse, or a log past the readable ceiling. It applies to every
	// purpose, because a log this package cannot enumerate is one it cannot claim the
	// latest event of.
	global *fault

	// total counts events across all purposes, which is what distinguishes "never
	// asked" from "this purpose was never asked about".
	total int
}

func (l *consentLog) fault(p Purpose) *fault {
	if l.global != nil {
		return l.global
	}
	if f, ok := l.faults[p]; ok {
		return &f
	}
	return nil
}

func (l *consentLog) addFault(p Purpose, reason Reason, err error) {
	if _, exists := l.faults[p]; exists {
		return
	}
	l.faults[p] = fault{reason: reason, err: err}
}

// resolve produces the Decision for one purpose: the latest event in its log, run
// through evaluate.
func (l *consentLog) resolve(p Purpose) Decision {
	if f := l.fault(p); f != nil {
		return refuse(p, f.reason, f.err)
	}
	events := l.events[p]
	if len(events) == 0 {
		if l.total == 0 {
			return refuse(p, ReasonNoConsentState,
				fmt.Errorf("consent: tenant has no consent events; absence of consent is refusal (I14)"))
		}
		return refuse(p, ReasonPurposeAbsent,
			fmt.Errorf("consent: purpose %q has no consent events; absent purpose is refused (§6.3)", p))
	}
	latest := events[len(events)-1]
	return evaluate(p, model.ConsentGrant{Granted: latest.Granted, TS: latest.TS, Version: latest.Version})
}

// loadLog reads and decodes a tenant's whole consent log.
func (r *Resolver) loadLog(ctx context.Context, tenant keys.TenantID) (*consentLog, Reason, error) {
	item, reason, err := r.loadTenant(ctx, tenant)
	if err != nil {
		return nil, reason, err
	}

	// Tripwire, not an input. Nothing in this system writes this attribute any more, so
	// finding it means backend state was mutated out of band (I16) — most likely a
	// provisioning path or a repair script that still believes §6.3's `consent` map is
	// where consent lives. It is deliberately not honoured in either direction: trusting
	// it would fail open on a stale grant, and vetoing on it would brick consent for a
	// tenant whose provisioner wrote an initial value. Purpose names are identifiers,
	// not content, so they are safe to log (§9.2).
	if names := legacyConsentPurposes(item); len(names) > 0 {
		logging.FromContext(ctx, r.logger()).Warn("tenant record carries a consent attribute, which nothing writes any more; it is ignored and the append-only consent log is authoritative (I16)",
			slog.String("attribute", attrConsent),
			slog.Any("purposes", names))
	}

	pk, prefix, err := consentLogPrefix(tenant)
	if err != nil {
		return nil, ReasonInvalidTenant, err
	}

	// One past the ceiling, so "exactly at the ceiling" and "truncated" are
	// distinguishable. A truncated read would silently drop the newest events, and the
	// newest event is the one that decides — losing a withdrawal is the fail-open case.
	items, err := r.repo.QueryPrefix(ctx, pk, prefix, maxEventsTotal+1)
	if err != nil {
		return nil, ReasonLookupFailed, fmt.Errorf("consent: reading the consent log: %w", err)
	}

	log := &consentLog{events: map[Purpose][]Event{}, faults: map[Purpose]fault{}}
	if len(items) > maxEventsTotal {
		log.global = &fault{
			reason: ReasonTooManyEvents,
			err:    fmt.Errorf("consent: tenant holds more than %d consent events, which is past the ceiling a gate reads in one query; archiving the log is an operational script (I16)", maxEventsTotal),
		}
		return log, reasonNone, nil
	}
	log.total = len(items)

	for _, it := range items {
		purpose, seq, err := splitEventSK(it.Key.SK)
		if err != nil {
			// Not attributable to a purpose, so it poisons every purpose. An event this
			// package cannot even place in a log is one that could be the withdrawal.
			if log.global == nil {
				log.global = &fault{reason: ReasonMalformedState, err: err}
			}
			continue
		}
		ev, err := decodeEvent(purpose, seq, it.Attrs)
		if err != nil {
			log.addFault(purpose, ReasonMalformedState, err)
			continue
		}
		log.events[purpose] = append(log.events[purpose], ev)
	}

	// Sequence density. QueryPrefix returns sort-key order and the sequence is
	// zero-padded, so each purpose's slice is ascending; a value that is not its own
	// index means an event between them is gone. PutOnce never skips a number, so this
	// can only be an out-of-band deletion (I16) — and the version it took with it is one
	// the §Phase 4 purge would never visit again, so the purpose refuses rather than
	// resolving from a log with a hole in it.
	for p, events := range log.events {
		for i, ev := range events {
			if ev.Seq != i {
				log.addFault(p, ReasonAmbiguousHistory,
					fmt.Errorf("consent: consent log for %q jumps to sequence %d at position %d, so an event between them is missing and the versions it recorded cannot be recovered", p, ev.Seq, i))
				break
			}
		}
	}
	return log, reasonNone, nil
}

// splitEventSK recovers the purpose and sequence from a consent event's sort key.
//
// The sort key is authoritative for both. It is what DynamoDB orders by, so a
// disagreement between it and an attribute is resolved in its favour — see decodeEvent,
// which refuses rather than picking.
func splitEventSK(sk string) (Purpose, int, error) {
	rest, ok := strings.CutPrefix(sk, consentEventSKPrefix)
	if !ok {
		return "", 0, fmt.Errorf("consent: sort key %q is not a consent event key", sk)
	}
	// LastIndex, not Index: a purpose containing the separator would otherwise split in
	// the wrong place. known() rejects such a purpose on write, and this makes the read
	// side independent of that.
	cut := strings.LastIndex(rest, "#")
	if cut < 0 {
		return "", 0, fmt.Errorf("consent: consent event sort key %q carries no sequence number", sk)
	}
	name, digits := rest[:cut], rest[cut+1:]
	if name == "" {
		return "", 0, fmt.Errorf("consent: consent event sort key %q names no purpose", sk)
	}
	if len(digits) != seqWidth {
		return "", 0, fmt.Errorf("consent: consent event sort key %q has a %d-digit sequence, want %d; a differently-padded sequence sorts wrongly and would make the latest event unidentifiable", sk, len(digits), seqWidth)
	}
	for _, c := range digits {
		// Checked digit by digit rather than left to Atoi, which accepts a leading sign:
		// "-00001" is seqWidth characters long and would parse.
		if c < '0' || c > '9' {
			return "", 0, fmt.Errorf("consent: consent event sort key %q has a non-numeric sequence", sk)
		}
	}
	seq, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, fmt.Errorf("consent: consent event sort key %q has an unreadable sequence: %w", sk, err)
	}
	return Purpose(name), seq, nil
}

// decodeEvent reads one stored event's attributes.
//
// Nothing is coerced: a granted field arriving as the string "true" or the number 1 is a
// record written by something that disagreed with this package about the schema, and
// inferring its intent is exactly how a refusal becomes a grant.
//
// An absent `granted` is an **error**, not false. For the state in force, absence
// reading as refusal is the safe reading; for a stored event it is not, because the
// consequence is not a refusal — it is a version quietly dropped from the list the
// §Phase 4 purge walks, so records stay behind after a withdrawal with nothing reporting
// it. The two omissions are equally damaging and are both errors here.
func decodeEvent(purpose Purpose, seq int, attrs map[string]any) (Event, error) {
	if attrs == nil {
		return Event{}, fmt.Errorf("consent: consent event %q/%d has no attributes", purpose, seq)
	}
	name, err := stringField(attrs, fieldPurpose)
	if err != nil {
		return Event{}, err
	}
	if name != string(purpose) {
		return Event{}, fmt.Errorf("consent: consent event %q/%d records purpose %q in its %s attribute; the key and the attribute disagree about whose consent this is", purpose, seq, name, fieldPurpose)
	}
	raw, present := attrs[fieldGranted]
	if !present {
		return Event{}, fmt.Errorf("consent: consent event %q/%d has no %s field, so it records neither a grant nor a refusal", purpose, seq, fieldGranted)
	}
	granted, ok := raw.(bool)
	if !ok {
		return Event{}, fmt.Errorf("consent: consent event %q/%d has %s of type %T, not a bool", purpose, seq, fieldGranted, raw)
	}
	ts, err := stringField(attrs, fieldTS)
	if err != nil {
		return Event{}, err
	}
	version, err := stringField(attrs, fieldVersion)
	if err != nil {
		return Event{}, err
	}
	return Event{Purpose: purpose, Granted: granted, TS: ts, Version: version, Seq: seq}, nil
}

// stringField reads an optional string attribute. Absent is empty; a wrong type is an
// error, because a version or timestamp stored as a number is not something to render
// into a string and hope.
func stringField(m map[string]any, field string) (string, error) {
	v, present := m[field]
	if !present || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("consent: %s is %T, not a string", field, v)
	}
	return s, nil
}

// legacyConsentPurposes names the purposes mentioned by the tenant record's `consent`
// attribute, for the tripwire in loadLog. Nil when the attribute is absent or empty.
//
// Tolerant of every shape the attribute could arrive in, because its job is to notice
// the attribute, not to interpret it — an unreadable one is still worth a warning, and
// the type name is content-free (§9.2).
func legacyConsentPurposes(item *repository.Item) []string {
	if item == nil || item.Attrs == nil {
		return nil
	}
	raw, ok := item.Attrs[attrConsent]
	if !ok || raw == nil {
		return nil
	}
	var names []string
	switch m := raw.(type) {
	case map[string]any:
		for k := range m {
			names = append(names, k)
		}
	case map[string]model.ConsentGrant:
		for k := range m {
			names = append(names, k)
		}
	case map[Purpose]model.ConsentGrant:
		for k := range m {
			names = append(names, string(k))
		}
	default:
		names = append(names, fmt.Sprintf("<%T>", raw))
	}
	sort.Strings(names)
	return names
}
