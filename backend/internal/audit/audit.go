// Package audit records every access to user content (I13).
//
// I13: "Every access to user content writes an audit record. Append-only,
// tenant-scoped, never mutated."
//
// §2A.1 explains why this is Phase 0 work rather than something a compliance
// conversation can add later:
//
//	"You cannot reconstruct history you did not record. Any future SOC 2 or
//	enterprise conversation begins with 'show me the access log,' and a gap in it
//	is not repairable."
//
// **Every handler touching user content calls Auditor.Record.** There is one write
// path, deliberately, so that adding a handler cannot accidentally add an unaudited
// one (§Phase 0). Allowed and Denied are sugar over that same path, not alternatives
// to it — §9.1 requires a denial to be recorded as readily as a success, and a
// denial that needed an extra argument remembered is a denial that goes unrecorded.
//
// # Write-once
//
// §6.3: "Audit and Usage items are write-once. No update or delete path exists in
// application code." So repository.PutOnce is the only write, and this package
// exposes no update and no delete at all — not a guarded one, none. The record's TTL
// (retention.audit_days, ~7 years) expires items in DynamoDB's own sweeper, which is
// not an application delete path; the only code that removes an audit record is the
// separately permissioned tenant-erasure operation (§9.3, G-038).
//
// # Why no field here may carry content
//
// The obvious reason is §9.2: transcript content, audio, and PII stay out of logs.
// The less obvious one is that an audit record is the longest-retained object in the
// system. I14 forbids retaining user content without a recorded consent state, and
// no consent state covers the audit log — it is retained under I13 as an operational
// record of *identifiers*. Put a transcript fragment in the resource field and it
// becomes user content held for seven years with no consent basis, in the one store
// that has no delete path. So `resource` names WHAT was accessed — a key or an id —
// and never what it contained, and validate() refuses shapes that could not be an
// identifier rather than trusting each call site to be careful.
//
// # A refusal describes the rejected value; it never quotes it
//
// The corollary, and the one that was learned the hard way: a check that refuses content
// must not itself publish that content. Every refusal in this package reports the field,
// the byte length, and the rule that failed — never the bytes — and that holds for the
// error returned to the caller as much as for the log line, because a handler may put a
// returned error in an HTTP response body. See validationError.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// auditTTLDaysDefault is retention.audit_days (§7.4): 2555 days, ~7 years. Passed in
// from config rather than hardcoded — this constant is only the documented default,
// used when a caller supplies nothing usable.
const auditTTLDaysDefault = 2555

// Field bounds. These exist to make "no content in an audit record" structural rather
// than a review item: an actor is a subject identifier, a resource is a key or an id,
// and neither is ever long. A value past these bounds is not an identifier that grew
// — it is a body that was passed by mistake.
const (
	actorMax    = 128
	resourceMax = 512

	// actionMax matches actionRe's own upper bound (1 leading char + 63) and is checked
	// *before* the regexp. Without it the regexp is the only thing standing between a
	// mis-passed body and the error message: a 10KB transcript passed where an action
	// belongs failed the match and was then quoted in full into the returned error and
	// into a WARN line (§9.2). A length check first means the over-bound case has its
	// own content-free message, the same way `resource` has always had one.
	actionMax = 64

	// uaMax truncates rather than refuses, because the user agent is client-supplied
	// (see sanitize).
	uaMax = 256
)

// ipInvalid marks an address that was supplied but did not parse.
//
// Distinct from empty on purpose. Empty means no address was available — a script
// invocation or a queue-driven worker. This value means a caller claimed one and it
// was not an address, which is itself worth seeing: a forged X-Forwarded-For is a
// security signal, and silently storing the claim would launder it into the log as
// fact.
const ipInvalid = "invalid"

// actionRe is the accepted shape for an action name: lowercase dotted/colon-separated
// verbs like "capture.read", "item.update", "tenant.erase".
//
// Restrictive by intent. An action is a small closed vocabulary chosen by the
// developer, so refusing anything sentence-shaped costs a call site nothing and makes
// it structurally impossible to pass a transcript line where an action belongs. It is
// not an enumeration because the vocabulary grows with every handler, and a central
// enum that every phase must edit is how a handler ends up not auditing at all.
var actionRe = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,63}$`)

// Access is one access to user content: who, what, which resource, and the outcome.
//
// The field set is §6.3's Audit entity exactly. Nothing is added here — an audit
// record with per-call extras is a record whose shape varies by call site, which is
// the shape audit.sh cannot query.
type Access struct {
	// Tenant scopes the record (I11). Comes from the validated JWT claim, never from
	// a path, query, or body (§9.1).
	Tenant keys.TenantID

	// Actor identifies the subject. Convention: "user:{user_id}", "script:{name}",
	// "system:{component}".
	//
	// A user *id*, not an email. An email in a seven-year record is PII in the
	// longest-retained store in the system (§9.2), so validate() refuses one.
	Actor string

	// Action is what was attempted, e.g. "capture.read". Must match actionRe.
	Action string

	// Resource identifies what was accessed — a DynamoDB sort key, an S3 object key,
	// or an entity id. **Never its content** (see the package doc). A handler acting
	// on a collection rather than one entity names the collection ("captures"),
	// because an audit record with no resource cannot answer "who touched this
	// capture", which is the question the log exists for.
	Resource string

	// IP is the caller's address, or empty where there is none (a script, a
	// queue-driven worker). One address: a caller reading X-Forwarded-For must pick
	// the one it trusts, because an unresolved chain is recorded as ipInvalid rather
	// than stored as though it were a single verified address.
	IP string

	// UA is the client's user agent, or empty. Truncated and stripped of control
	// characters rather than refused (see sanitize).
	UA string

	// Result is model.AuditAllowed or model.AuditDenied. Set for you by Allowed and
	// Denied.
	Result string
}

// IDGenerator produces the sortable unique component of an audit key.
//
// A ULID, not a UUID: this value is the whole of the audit sort key past its prefix
// (§6.3), and the time-range query audit.sh needs (§11.4) depends on those records
// sorting chronologically. Random identifiers would make "the records from last
// Tuesday" require a secondary index, which §6.3 forbids projecting audit records into.
type IDGenerator interface {
	NewID() string
}

// Auditor writes audit records.
type Auditor struct {
	repo    repository.Repository
	clk     clock.Clock
	ids     IDGenerator
	log     *slog.Logger
	ttlDays int
}

// New builds an Auditor. ttlDays comes from retention.audit_days (§7.4).
func New(repo repository.Repository, clk clock.Clock, ids IDGenerator, log *slog.Logger, ttlDays int) *Auditor {
	if ttlDays <= 0 {
		ttlDays = auditTTLDaysDefault
	}
	return &Auditor{repo: repo, clk: clk, ids: ids, log: log, ttlDays: ttlDays}
}

// Allowed records a permitted access.
func (a *Auditor) Allowed(ctx context.Context, ev Access) error {
	ev.Result = model.AuditAllowed
	return a.Record(ctx, ev)
}

// Denied records a refused access.
//
// §9.1: "A cross-tenant access attempt is an audit event at WARN and returns 404, not
// 403 — a 403 confirms the resource exists." This is the call for that, and it exists
// as its own entry point because the refusal path is the one a handler is most likely
// to return early from without recording anything.
func (a *Auditor) Denied(ctx context.Context, ev Access) error {
	ev.Result = model.AuditDenied
	return a.Record(ctx, ev)
}

// Record writes one audit record. **This is the only write path in the package.**
//
// # Call it before the access, not after
//
// The record attests that access was authorised and attempted. A crash between
// serving content and recording it produces exactly the gap §2A.1 calls unrepairable,
// so the record goes first and an operation that then fails leaves a record of an
// access that returned nothing. That over-reports slightly, and that asymmetry is the
// intended one: a log that claims one access too many is auditable, a log missing one
// is not. §9.3 already requires this ordering for erasure ("writes an audit record
// *before* executing"); it is the general rule, not an erasure special case.
//
// # What a caller does when this returns an error
//
// **Do not serve user content when this returns non-nil.** Fail the request.
//
// The choice is real either way, so here is the reasoning rather than an assertion.
// Proceeding means an access to user content happened with no record of it, which is
// precisely the state I13 exists to make impossible and the one that cannot be
// repaired afterwards — there is nowhere to reconstruct it from. Refusing means a
// DynamoDB write failure turns a read into a 5xx, which costs availability. Three
// things make the availability side the cheaper one here:
//
//   - The audit write and the data it guards are the same table. A failure that stops
//     the audit write is overwhelmingly a failure that would have stopped the
//     operation anyway, so the divergence between the two policies is narrow in
//     practice.
//   - Nothing is lost by refusing. Audio is buffered locally until upload is
//     confirmed (I2) and every POST/PATCH is idempotency-keyed (§2A.1), so a failed
//     request is a retried request, not a lost thought. This is the same reasoning
//     meter.Record uses for failing a pipeline stage.
//   - Single-operator deployment. An outage is noticed; a silent audit gap is not
//     noticed until someone asks for the access log, which is the worst possible
//     moment to discover it.
//
// **One carve-out, and it runs the same direction: a failed Denied write never becomes
// an allow.** The access stays refused (404 per §9.1) and the error propagates for the
// operator's sake. Failing to record a refusal must not soften the refusal.
//
// Both failure branches — an unusable generated key and a failed write — are logged at
// ERROR as well as returned, because I13 does not permit this to be best-effort and a
// call site that mishandles the error would otherwise leave no trace of the gap at all.
// A rejected *record* is different: that is a caller defect, not a gap in the log, so it
// is WARN, and it is logged as structure rather than as a message (see below).
func (a *Auditor) Record(ctx context.Context, ev Access) error {
	ev = ev.sanitize()

	if err := ev.validate(); err != nil {
		// Neither the rejected value nor the error *message* reaches the log, and the
		// message is the part this got wrong: two validation messages interpolated the
		// value with %q, so a Cognito username passed as an actor and a 10KB transcript
		// passed as an action were written to CloudWatch at WARN — by the very check
		// whose job is keeping them out of the seven-year store (§9.2). This line
		// carries the field, the rule that refused it, and a redacted length, which is
		// the whole of what makes a rejection diagnosable. Logging structure rather
		// than the message is what makes it stay fixed: a %q added to a message later
		// cannot leak here, because this line never formats the message.
		l := logging.FromContext(ctx, a.log)
		var ve *validationError
		if errors.As(err, &ve) {
			l.Warn("audit record rejected", ve.logAttrs()...)
		} else {
			// Unreachable from validate() today. ErrorAttr rather than the message so
			// that an error path added there later fails closed on this branch too.
			l.Warn("audit record rejected", logging.ErrorAttr(err))
		}
		return err
	}

	now := a.clk.Now()

	key, err := keys.Audit(ev.Tenant, a.ids.NewID())
	if err != nil {
		// Logged at ERROR for the same reason the PutOnce failure is: I13 does not
		// permit this to be best-effort, and a call site that mishandles the error would
		// otherwise leave no trace of the gap at all. An IDGenerator returning an
		// unusable id (a future generator embedding a '#', which keys.identRe forbids)
		// produces exactly the same unrecorded access as a failed write.
		//
		// slog.Any is safe on this one error specifically: keys quotes the *generated*
		// id, which is machine-produced and cannot be user content. Everything on the
		// validation branch above goes through structure instead, because those values
		// are caller-supplied.
		logging.FromContext(ctx, a.log).Error("audit key generation failed; access must not proceed (I13)",
			slog.String("action", ev.Action),
			slog.String("result", ev.Result),
			slog.Any("error", err),
		)
		return fmt.Errorf("audit: %w", err)
	}

	rec := model.Audit{
		ID:       key.SK,
		Actor:    ev.Actor,
		Action:   ev.Action,
		Resource: ev.Resource,
		IP:       ev.IP,
		UA:       ev.UA,
		Result:   ev.Result,
		TS:       clock.RFC3339UTC(now),
		// Absolute epoch second. AddDate rather than a duration multiplication so the
		// window lands on the same wall-clock time 2555 days out, across every leap
		// day and DST transition in between.
		TTL: now.AddDate(0, 0, a.ttlDays).Unix(),
	}

	item := repository.Item{
		Key: key,
		Attrs: map[string]any{
			"actor":    rec.Actor,
			"action":   rec.Action,
			"resource": rec.Resource,
			"ip":       rec.IP,
			"ua":       rec.UA,
			"result":   rec.Result,
			"ts":       rec.TS,
			"ttl":      rec.TTL,
		},
		TTL: rec.TTL,
		// No GSI1 attributes. Audit is the highest-volume entity in the table — one
		// record per access to user content — and projecting it into the sparse index
		// makes that index a second copy of the table, paid for on every write (§6.3).
	}

	// PutOnce, not Put: audit records are write-once (§6.3, I13). A taken key means
	// the ID generator collided, and failing on that is the point — an overwrite would
	// destroy an existing record, which is the one thing an append-only log may never
	// do.
	if err := a.repo.PutOnce(ctx, item); err != nil {
		// ErrorAttr rather than the message: this error crossed the storage-provider
		// boundary, and AWS errors quote request detail back (§9.2).
		logging.FromContext(ctx, a.log).Error("audit write failed; access must not proceed (I13)",
			slog.String("action", rec.Action),
			slog.String("result", rec.Result),
			logging.ErrorAttr(err),
		)
		return fmt.Errorf("audit: writing audit record: %w", err)
	}

	// A denial is WARN because §9.1 requires it: "a cross-tenant access attempt is an
	// audit event at WARN". An allowed access is DEBUG — the durable record is the
	// DynamoDB item, and one INFO line per content access would make CloudWatch
	// ingestion at $0.50/GB a top line item (§10.1). Actor, action, and resource are
	// identifiers, not content, so they are safe to log and are the whole of what makes
	// the warning actionable; tenant and correlation come from the context.
	l := logging.FromContext(ctx, a.log)
	attrs := []any{
		slog.String("actor", rec.Actor),
		slog.String("action", rec.Action),
		slog.String("resource", rec.Resource),
		slog.String("result", rec.Result),
	}
	if rec.Result == model.AuditDenied {
		l.Warn("access denied", attrs...)
	} else {
		l.Debug("access", attrs...)
	}
	return nil
}

// sanitize normalises the two client-controlled fields.
//
// They are treated differently from the rest on purpose. Because Record's contract is
// that a caller fails the request when it returns an error, refusing a record over a
// client-supplied header would hand any client a way to make its own requests fail —
// a browser with an unusual user agent, or a proxy with an unresolved forwarded chain,
// would take itself offline. So client input is bounded and normalised, while the
// developer-supplied fields (actor, action, resource, result) are refused outright:
// those failures are defects, and they surface in a test rather than in production.
func (ev Access) sanitize() Access {
	ev.UA = sanitizeUA(ev.UA)
	ev.IP = sanitizeIP(ev.IP)
	return ev
}

func sanitizeUA(ua string) string {
	// Control characters are dropped, newlines included: the value comes from a
	// request header, and a header carrying a newline into a line-oriented log
	// processor downstream is a forged log entry.
	// unicode.IsControl covers C1 as well as C0, so U+0085 NEL goes too — see hasControl.
	ua = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ua)
	if len(ua) > uaMax {
		// ToValidUTF8 because a byte-length cut can land mid-sequence, and an invalid
		// UTF-8 attribute is rejected by DynamoDB — a truncation that made the write
		// fail would, under Record's contract, fail the user's request.
		ua = strings.ToValidUTF8(ua[:uaMax], "")
	}
	return ua
}

func sanitizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ipInvalid
	}
	// Canonical form, so the same address written two ways ("::1" and
	// "0:0:0:0:0:0:0:1") is one value in the log and an actor's history is not split
	// across spellings.
	return parsed.String()
}

// validationError refuses one field of an Access.
//
// # The rule this type exists to make structural
//
// **A message about a rejected value describes the value; it never quotes it.**
// Length, shape, and which rule failed are the diagnosis; the bytes are not. That
// holds for the *returned* error as much as for a log line, because a handler may put
// a returned error into an HTTP body, and because Record's own rejection log used to
// print the message verbatim — which is how the actor-email and action-shape checks
// came to write a Cognito username and a mis-passed transcript body to CloudWatch,
// each of them the exact value the check refuses to store (§9.2).
//
// Structured rather than a bare fmt.Errorf so the rejection log can name the field and
// the rule without formatting the message at all (see Record). The value is held only
// so that logAttrs can report it through logging.Redacted — placeholder plus length —
// and is never interpolated into msg.
type validationError struct {
	field string // "tenant", "actor", "action", "resource", "result"
	rule  string // content-free: "required", "over-bound", "invalid-utf8", ...
	value string // NEVER formatted into msg or Error(); see logAttrs
	msg   string // content-free by construction
	err   error  // wrapped cause, where one exists (keys)
}

func (e *validationError) Error() string { return "audit: " + e.msg }

// Unwrap keeps errors.Is working through the wrapped keys failures.
func (e *validationError) Unwrap() error { return e.err }

func (e *validationError) logAttrs() []any {
	return []any{
		slog.String("field", e.field),
		slog.String("rule", e.rule),
		// Redacted, not omitted: "the value was 10,169 bytes" is the line that tells an
		// operator a body was passed where an identifier belongs, and it says so without
		// carrying a byte of it (internal/logging).
		logging.Redacted("rejected", e.value),
	}
}

func refuse(field, rule, value, format string, args ...any) *validationError {
	return &validationError{field: field, rule: rule, value: value, msg: fmt.Sprintf(format, args...)}
}

func (ev Access) validate() error {
	// Tenant first: an unscoped audit record is an I11 violation, and it is the check
	// most likely to fire on a path that skipped JWT claim resolution.
	if _, err := keys.Tenant(ev.Tenant); err != nil {
		// keys' own message, which names the field; it quotes at most a tenant id from a
		// validated JWT claim, never a caller-supplied body. Wrapped so errors.Is still
		// reaches the keys error, and so the log takes the structured path like the rest.
		return &validationError{field: "tenant", rule: "invalid", value: string(ev.Tenant), msg: err.Error(), err: err}
	}
	if ev.Actor == "" {
		return refuse("actor", "required", "",
			"actor is required; an unattributed record is not an access log (I13)")
	}
	if len(ev.Actor) > actorMax {
		return refuse("actor", "over-bound", ev.Actor,
			"actor is %d bytes, over the %d-byte bound; an actor is a subject identifier, not a body (§9.2)", len(ev.Actor), actorMax)
	}
	if !utf8.ValidString(ev.Actor) {
		// DynamoDB refuses a string attribute that is not valid UTF-8, and under Record's
		// fail-closed contract a refused write fails the user's request. Refused here,
		// where the message names the field, rather than as an opaque marshalling error.
		return refuse("actor", "invalid-utf8", ev.Actor,
			"actor is not valid UTF-8 (%d bytes); a DynamoDB string attribute must be, and a rejected write fails the request", len(ev.Actor))
	}
	if strings.ContainsRune(ev.Actor, '@') {
		// Refused rather than trimmed: silently storing something else would hide the
		// defect until a subject-access request found the email seven years later. The
		// value is described, not quoted — the field name is the actionable part, and
		// quoting it here put the email into CloudWatch and into the returned error.
		return refuse("actor", "email-shaped", ev.Actor,
			"actor contains '@' and so looks like an email address (%d bytes); use the user id — PII must not enter the audit log, which is the longest-retained store in the system (§9.2)", len(ev.Actor))
	}
	if hasControl(ev.Actor) {
		return refuse("actor", "control-character", ev.Actor, "actor contains a control character")
	}
	if len(ev.Action) > actionMax {
		// Length before the regexp: see actionMax. Reports the length, never the value.
		return refuse("action", "over-bound", ev.Action,
			"action is %d bytes, over the %d-byte bound; an action is a short verb from a closed vocabulary, not a body (§9.2)", len(ev.Action), actionMax)
	}
	if !actionRe.MatchString(ev.Action) {
		return refuse("action", "shape", ev.Action,
			"action (%d bytes) is not an action name; expected a lowercase dotted verb such as \"capture.read\" (§6.3)", len(ev.Action))
	}
	if ev.Resource == "" {
		return refuse("resource", "required", "",
			"resource is required; a record with no resource cannot answer which capture was accessed (I13)")
	}
	if len(ev.Resource) > resourceMax {
		// Reports the length, never the value: a resource this long is a body rather
		// than an identifier, and an error message is a thing that gets logged.
		return refuse("resource", "over-bound", ev.Resource,
			"resource is %d bytes, over the %d-byte bound; resource names what was accessed (a key or an id), never its content (§9.2)", len(ev.Resource), resourceMax)
	}
	if !utf8.ValidString(ev.Resource) {
		// The realistic source is a path or query segment ({id} from /v1/captures/{id})
		// reaching resource unvalidated.
		return refuse("resource", "invalid-utf8", ev.Resource,
			"resource is not valid UTF-8 (%d bytes); a DynamoDB string attribute must be, and a rejected write fails the request", len(ev.Resource))
	}
	if hasControl(ev.Resource) {
		return refuse("resource", "control-character", ev.Resource,
			"resource contains a control character; a multi-line resource is content, not an identifier (§9.2)")
	}
	switch ev.Result {
	case model.AuditAllowed, model.AuditDenied:
	default:
		// Enumerated, so a typo cannot produce a third outcome that audit.sh's
		// result filter would silently never match. The two constants are ours to quote;
		// the rejected value is not, for the same reason as everywhere above.
		return refuse("result", "not-enumerated", ev.Result,
			"result (%d bytes) is neither %q nor %q", len(ev.Result), model.AuditAllowed, model.AuditDenied)
	}
	return nil
}

// hasControl reports a C0 or C1 control character.
//
// unicode.IsControl rather than a hand-written `r < 0x20 || r == 0x7f`, which passed the
// whole C1 range — U+0085 NEL among them, which some log processors treat as a line
// terminator. That is precisely the forged-log-entry risk the newline strip is here for,
// so the narrower test defeated its own purpose.
func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// ---------------------------------------------------------------------------
// Read path — audit.sh (§11.4)
// ---------------------------------------------------------------------------

// tsBoundRe is the accepted shape for a Query time bound: a date, or a full RFC3339
// UTC timestamp.
//
// Both work because the bound is compared lexicographically against the record's `ts`,
// and a date is a prefix of every timestamp within it — so `--since 2026-08-01` needs
// no date arithmetic in audit.sh. The Z suffix is mandatory for the same reason
// keys.GSI1 requires it: string order and chronological order coincide in one zone
// only, and a bound carrying a local offset would silently select the wrong window.
// The shape is necessary but not sufficient, which is why validateTSBound also parses.
var tsBoundRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}Z)?$`)

// dateOnlyLayout is the date form of a bound; tsBoundRe has already forced the Z suffix
// on the timestamp form, so time.RFC3339 cannot admit a local offset here.
const dateOnlyLayout = "2006-01-02"

// validateTSBound refuses a bound whose shape is right but whose calendar is not.
//
// The shape regexp alone accepts 2026-31-08, 2026-13-01 and 2026-00-00. Those compare
// lexicographically against a stored `ts` like any other string, so a day/month
// transposition — the most common date typo — returned an empty log and a nil error,
// and 2026-00-00 sorted below every record and silently meant "unbounded". Refusing is
// the same reasoning already applied to the Result filter one screen down: "a filter
// that matches no record is indistinguishable from a clean log, and 'the audit log is
// empty' is the wrong answer to report from a typo." An operator asking who accessed a
// capture is asking a compliance question, and a wrong answer that looks right is the
// worst possible outcome.
//
// time.Parse rather than keys' dateRe ranges because it also rejects 2026-02-30, and a
// bound is not a key so the no-literals rule does not apply.
func validateTSBound(field, v string) error {
	if !tsBoundRe.MatchString(v) {
		// The value is not quoted: an operator passing this on a command line already has
		// it, and a bound is one more field that could be handed the wrong string (§9.2).
		return fmt.Errorf("audit: %s must be yyyy-mm-dd or an RFC3339 UTC timestamp with a Z suffix", field)
	}
	layout := dateOnlyLayout
	if len(v) > len(dateOnlyLayout) {
		layout = time.RFC3339
	}
	if _, err := time.Parse(layout, v); err != nil {
		return fmt.Errorf("audit: %s has the right shape but is not a real date or time (%d bytes); "+
			"a bound that cannot exist would return an empty log and no error, which reads as 'nobody accessed anything'", field, len(v))
	}
	return nil
}

// Query selects audit records. Every field but Tenant is optional; a zero Query for a
// tenant returns that tenant's whole retained log.
//
// The four dimensions §11.4 requires of audit.sh — actor, action, resource, time range
// — are here so that the script has no reason to reach for the AWS CLI (I16).
type Query struct {
	Tenant keys.TenantID

	// From is inclusive, To is exclusive. Both are a date or an RFC3339 UTC
	// timestamp; empty means unbounded. Exclusive upper bound so that consecutive
	// windows tile without double-counting a record on the boundary.
	From string
	To   string

	// Actor, Action, Resource, Result are exact-match filters. Empty means any.
	Actor    string
	Action   string
	Resource string
	Result   string

	// Limit caps the returned records, 0 for unlimited. Applied after filtering and
	// after ordering, so a limit means "this many matches" rather than "this many
	// records examined" — and, with Newest unset, the *earliest* matches. Page.Truncated
	// reports whether it cut anything.
	Limit int

	// Newest orders the result most-recent-first.
	//
	// Here because the most natural question audit.sh asks — recent accesses to one
	// resource — got the wrong answer without it: a limited ascending query over a
	// seven-year log returns twenty accesses from the first week of the log and reads as
	// a complete answer. The alternative was making the caller compute a From bound,
	// which is the date arithmetic the date-prefix bound exists to avoid.
	Newest bool
}

// Page is a Query result: the matching records, plus whether Limit cut any.
//
// Truncated is not cosmetic. "Exactly N matches" and "N of thousands" are different
// compliance answers and a bare slice cannot tell them apart, which is the same class of
// wrong-but-plausible answer as an empty result from a typo'd bound.
type Page struct {
	Records   []model.Audit
	Truncated bool
}

// Query returns matching audit records, oldest first unless q.Newest is set.
//
// Read-only, and the only read path: audit.sh is a read-only script with no --apply
// (§11.4), and I16 requires the operational scripts to be the sole route to backend
// state — a script reaching for `aws dynamodb query` instead would be untested,
// unaudited, and unrepeatable.
//
// # Cost shape, stated honestly
//
// This reads the tenant's whole audit sort-key range and filters in process. That range
// is prefix plus ULID (§6.3) and ULIDs sort chronologically, so a narrower read is
// *possible* in principle — but repository.QueryPrefix exposes prefix reads only rather
// than >=/<= bounds, and deriving a shared ULID prefix for an arbitrary window narrows
// anything only when both ends happen to share leading characters. At the modelled
// volume — tens of
// captures a day (§10.7) — a tenant's log is small items and this is cheap. At
// commercial volume it is not, and the fix is a date-partitioned sort key plus a range
// read, which is why the ULID is there: records stay ordered under either scheme.
// meter.DayTotal makes the same trade for the same reason.
func (a *Auditor) Query(ctx context.Context, q Query) (Page, error) {
	pk, prefix, err := auditPrefix(q.Tenant)
	if err != nil {
		return Page{}, err
	}
	if q.From != "" {
		if err := validateTSBound("from", q.From); err != nil {
			return Page{}, err
		}
	}
	if q.To != "" {
		if err := validateTSBound("to", q.To); err != nil {
			return Page{}, err
		}
	}
	if q.Result != "" {
		switch q.Result {
		case model.AuditAllowed, model.AuditDenied:
		default:
			// Refused rather than returning nothing: a filter that matches no record
			// is indistinguishable from a clean log, and "the audit log is empty" is
			// the wrong answer to report from a typo. The value is described rather than
			// quoted, as everywhere else in this package (see validationError).
			return Page{}, fmt.Errorf("audit: result filter (%d bytes) is neither %q nor %q", len(q.Result), model.AuditAllowed, model.AuditDenied)
		}
	}

	// Limit 0: the repository's limit would cut before filtering, which would silently
	// drop matches that sit past the cut.
	items, err := a.repo.QueryPrefix(ctx, pk, prefix, 0)
	if err != nil {
		return Page{}, fmt.Errorf("audit: reading audit records: %w", err)
	}

	out := make([]model.Audit, 0, len(items))
	for _, it := range items {
		rec := decode(it)
		if q.From != "" && rec.TS < q.From {
			continue
		}
		if q.To != "" && rec.TS >= q.To {
			continue
		}
		if q.Actor != "" && rec.Actor != q.Actor {
			continue
		}
		if q.Action != "" && rec.Action != q.Action {
			continue
		}
		if q.Resource != "" && rec.Resource != q.Resource {
			continue
		}
		if q.Result != "" && rec.Result != q.Result {
			continue
		}
		out = append(out, rec)
		// No early break on Limit: Truncated has to be knowable, and the cut has to
		// happen after ordering or Newest would return the oldest matches reversed.
	}
	if q.Newest {
		slices.Reverse(out)
	}
	page := Page{Records: out}
	if q.Limit > 0 && len(out) > q.Limit {
		page.Records, page.Truncated = out[:q.Limit], true
	}
	return page, nil
}

// auditPrefix returns the partition key and the audit sort-key prefix for a tenant.
//
// Derived from keys.Audit rather than written out, because no key-prefix literal may
// appear outside internal/keys — check-tenant-keys.sh fails the build on one, and that
// check is what makes the key helper a control rather than a convention (I11). It
// greps sources as fixed strings, so this applies to a comment quoting the prefix as
// much as to code building a key with it.
//
// The keys package has UsageMonthPrefix but no audit equivalent; probing with a
// throwaway-but-valid identifier and trimming it back off keeps that package the single
// authority on the key's shape, so this cannot drift if the prefix ever changes.
func auditPrefix(t keys.TenantID) (pk string, skPrefix string, err error) {
	const probe = "0"
	k, err := keys.Audit(t, probe)
	if err != nil {
		return "", "", fmt.Errorf("audit: %w", err)
	}
	return k.PK, strings.TrimSuffix(k.SK, probe), nil
}

// decode reads a stored item back into model.Audit.
//
// The numeric type switch on ttl is not defensive padding: the in-memory repository
// hands back the int64 it was given, while a DynamoDB round trip goes through
// attributevalue and returns a JSON-ish number. A single type assertion would compile,
// pass against the fake, and silently report every record's TTL as zero in production —
// which reads as "audit retention is not configured".
func decode(it repository.Item) model.Audit {
	str := func(k string) string {
		v, _ := it.Attrs[k].(string)
		return v
	}
	rec := model.Audit{
		// The sort key, prefix included, exactly as meter stores a usage ID. It is the
		// value that identifies the record in the table, so an operator quoting an ID
		// from audit.sh output is quoting something addressable.
		ID:       it.Key.SK,
		Actor:    str("actor"),
		Action:   str("action"),
		Resource: str("resource"),
		IP:       str("ip"),
		UA:       str("ua"),
		Result:   str("result"),
		TS:       str("ts"),
	}
	switch v := it.Attrs["ttl"].(type) {
	case int64:
		rec.TTL = v
	case int:
		rec.TTL = int64(v)
	case float64:
		rec.TTL = int64(v)
	}
	if rec.TTL == 0 {
		rec.TTL = it.TTL
	}
	return rec
}
