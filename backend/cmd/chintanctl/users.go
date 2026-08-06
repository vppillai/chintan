// users.go renders the audit record for a scripts/users.sh invocation.
//
// # Why this is in Go at all, when users.sh is a bash script
//
// §11.4 files `users.sh` under *Users and access (bash)*, and it stays bash: its
// work is a handful of `cognito-idp` calls, which is lifecycle-shaped and needs no
// SDK. But §11.3 also puts it "across the line" — it takes `--tenant` and writes an
// audit record, "because [it] change[s] who can reach tenant data" — and an audit
// record cannot be built in bash for two independent reasons:
//
//   - I11 gives backend/internal/keys a monopoly on key construction, enforced by
//     scripts/checks/check-tenant-keys.sh, which fails the build on a key prefix
//     literal appearing in a shell script. There is deliberately no way to write
//     the audit partition key from bash.
//   - I13 makes the audit record's shape load-bearing for seven years, and §11.2 is
//     explicit that logic of that weight belongs in tested application code. A
//     second, hand-rolled item shape in bash is the passbook admin.sh drift the
//     whole §11.2 split exists to prevent.
//
// So this subcommand *renders* the record and users.sh writes it with
// `aws dynamodb put-item`. The split is what makes the write observable in the
// fake-AWS harness, which is what lets §11.5's mandatory --apply test actually
// assert that the audit record was written rather than trust that it was.
//
// # The record is produced by the audit package, not by a copy of it
//
// This file does not know the audit item's attribute set. It writes an Access
// through audit.Auditor into an in-memory repository and serialises whatever came
// out. Two consequences, both intended: the shape is by construction the one every
// handler writes, and if the audit package grows an attribute this renderer cannot
// encode — a nested map, a GSI1 projection — rendering FAILS rather than quietly
// dropping it. TestRenderedItemMatchesAuditPackage asserts that coupling so the
// drift is caught in CI rather than in a subject-access request.
//
// # No email reaches the record
//
// The subject of every users.sh operation is an email address, and an audit record
// is the longest-retained store in the system (§9.2) with no application delete
// path (§6.3). audit.validate() therefore refuses an actor containing '@' outright.
// The resource field carries a digest of the address instead — see subjectDigest for
// why a digest rather than the Cognito `sub`, and for what the digest does and does
// not protect.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const usersUsage = `chintanctl users audit-item --tenant <id> --operation <op> --mode <mode> [--subject <email>]

Render the audit record for one scripts/users.sh invocation (I13, §11.3).

Prints, on stdout, a JSON object carrying the action name, the subject digest, and
the DynamoDB item ready for 'aws dynamodb put-item --item'. Writes nothing itself:
users.sh performs the put, so the write is visible to the fake-AWS harness and
§11.5's --apply test can assert it happened.

This is not a general audit-writing entry point. It exists because users.sh is bash
(§11.4) and bash cannot construct a tenant-scoped key (I11) or the audit item shape
(§11.2). Handlers call internal/audit directly.

Flags:
  --tenant <id>       Tenant whose access is being changed. Required (I11, §11.3).
  --operation <op>    add | resend | remove | reset | list
  --mode <mode>       plan (a --dry-run invocation: reads only) or execute
  --subject <email>   The user. Required for every operation except list.
  --config <path>     Instance config, for retention.audit_days (§7.4). Required.
  --actor <id>        Defaults to script:users.sh (§6.3 actor convention).

Read-only with respect to AWS: no --apply, because it mutates nothing.

Example:
  chintanctl users audit-item --tenant t-vp --operation add --mode execute \
      --subject someone@example.com --config ../config/instances/dev.yaml
`

// defaultUsersActor follows §6.3's actor convention, "script:{name}". The script
// name and not the human: the operator's identity on an infrastructure call is
// attributable through CloudTrail (§9.5), and inventing a second, unverified notion
// of operator identity here would make the audit log's actor field mean two
// different things depending on which code wrote it.
const defaultUsersActor = "script:users.sh"

// usersCollectionResource names the whole pool for the one operation that acts on no
// single user. §6.3's audit doc requires a resource even then: "a handler acting on a
// collection rather than one entity names the collection, because an audit record
// with no resource cannot answer 'who touched this capture'".
const usersCollectionResource = "cognito-users"

// usersSubjectPrefix distinguishes a Cognito subject from the tenant's own User
// record, which is keyed on the Cognito `sub` (keys.User) and arrives in Phase 1.
// They are not the same identifier and an audit query that conflated them would
// answer the wrong question.
const usersSubjectPrefix = "cognito-user/"

// subjectDigestHexLen is how much of the SHA-256 reaches the record: 16 hex
// characters, 64 bits. Enough that a collision will not happen in a user pool of
// this size, short enough that an operator can compare two records by eye.
const subjectDigestHexLen = 16

// emailMax is RFC 5321's maximum reverse-path length. A longer value is not an
// address, and refusing it here keeps a mis-pasted body from reaching argv of an
// `aws cognito-idp` call.
const emailMax = 254

// usersAction maps an operation and a mode to the action recorded.
//
// **The mapping is here rather than in users.sh so that bash holds no policy
// (§11.2), and it is a table rather than string concatenation so that the whole
// vocabulary is visible in one place.**
//
// A --dry-run invocation records `user.read`, not the mutation it declined to
// perform. That is the honest record: a dry run reads the pool to build its plan and
// changes nothing, so recording `user.delete` for it would put a deletion in the
// access log that never happened — and the access log is the one place where an
// entry that overstates what occurred cannot be corrected afterwards. `user.read` is
// also not a throwaway: reading who can reach a tenant's data is itself an access
// worth recording, which is precisely why §11.3 puts users.sh on the audited side of
// the line.
//
// `list` is read-only in both modes (§11.3 gives it no --apply), so both map to
// `user.list`.
var usersAction = map[string]map[string]string{
	"add":    {"plan": "user.read", "execute": "user.create"},
	"resend": {"plan": "user.read", "execute": "user.invite_resend"},
	"remove": {"plan": "user.read", "execute": "user.delete"},
	"reset":  {"plan": "user.read", "execute": "user.password_reset"},
	"list":   {"plan": "user.list", "execute": "user.list"},
}

// usersOperations is the accepted --operation set, in the order --help lists them.
var usersOperations = []string{"add", "resend", "remove", "reset", "list"}

// auditItemResult is the --json contract users.sh parses (§11.3), so the caller reads
// structure rather than scraping the text of a plan.
type auditItemResult struct {
	// Action and Resource are echoed so users.sh can print the plan and the executed
	// operation from the same source of truth. A dry-run that describes a different
	// action from the one --apply records would be a dry-run that lies (§11.5).
	Action   string `json:"action"`
	Resource string `json:"resource"`

	// ApplyAction is the action an --apply invocation of the same operation records.
	// Reported so a dry run can describe the record --apply will write without a
	// second invocation of this binary — §11.5's assertion that "dry-run output
	// describes precisely what --apply then does" covers the audit record too.
	ApplyAction string `json:"apply_action"`

	// SubjectDigest is the stable correlator an operator feeds to audit.sh to find
	// every record for one user. Empty for the collection operations.
	SubjectDigest string `json:"subject_digest,omitempty"`

	// TTLDays is reported so an operator can see the retention actually applied came
	// from config rather than from a default (§7.4).
	TTLDays int `json:"ttl_days"`

	// Item is the DynamoDB item in AWS CLI attribute-value form, for
	// `aws dynamodb put-item --item`.
	Item map[string]any `json:"item"`
}

func runUsers(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usersUsage)
		return 2
	}
	switch args[0] {
	case "audit-item":
		return runUsersAuditItem(args[1:])
	case "-h", "--help":
		fmt.Print(usersUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "chintanctl users: unknown subcommand %q\n\n%s", args[0], usersUsage)
		return 2
	}
}

func runUsersAuditItem(args []string) int {
	fs := newFlagSet("users audit-item", usersUsage)
	tenant := fs.String("tenant", "", "tenant id (required)")
	operation := fs.String("operation", "", "add | resend | remove | reset | list")
	mode := fs.String("mode", "plan", "plan (dry run) or execute")
	subject := fs.String("subject", "", "user email; required for every operation except list")
	cfgPath := fs.String("config", "", "instance config path, for retention.audit_days")
	actor := fs.String("actor", defaultUsersActor, "audit actor")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	res, err := buildAuditItem(auditItemRequest{
		tenant:    *tenant,
		operation: *operation,
		mode:      *mode,
		subject:   *subject,
		cfgPath:   *cfgPath,
		actor:     *actor,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "chintanctl users audit-item: %v\n", err)
		// 2 for a usage error, 1 for an operational failure, matching main's
		// convention: a CI step needs to tell "I invoked this wrongly" from "the thing
		// I asked for failed".
		if isUsersUsageError(err) {
			return 2
		}
		return 1
	}
	emitJSON(res)
	return 0
}

type auditItemRequest struct {
	tenant    string
	operation string
	mode      string
	subject   string
	cfgPath   string
	actor     string
}

// usersUsageError marks the failures that are a wrong invocation rather than a
// broken environment. Typed rather than string-matched so the exit code cannot drift
// away from the message.
type usersUsageError struct{ msg string }

func (e *usersUsageError) Error() string { return e.msg }

func isUsersUsageError(err error) bool {
	var ue *usersUsageError
	return errors.As(err, &ue)
}

func usageErrf(format string, args ...any) error {
	return &usersUsageError{msg: fmt.Sprintf(format, args...)}
}

// buildAuditItem is the whole operation, separated from flag parsing so the tests
// exercise it directly rather than through argv.
func buildAuditItem(req auditItemRequest) (auditItemResult, error) {
	if req.tenant == "" {
		// Duplicated with require_tenant in scripts/lib/common.sh on purpose: users.sh
		// refuses first with the operator-facing message, and this refuses again so
		// that no future caller of the binary can reach an untenanted audit key (I11).
		// A guard that exists only in the front-end is a guard the second front-end
		// does not have.
		return auditItemResult{}, usageErrf("--tenant is required: no data operation runs untenanted (I11, §11.3)")
	}
	modes, ok := usersAction[req.operation]
	if !ok {
		return auditItemResult{}, usageErrf("--operation %q is not one of %s", req.operation, strings.Join(usersOperations, ", "))
	}
	action, ok := modes[req.mode]
	if !ok {
		return auditItemResult{}, usageErrf("--mode %q is neither \"plan\" nor \"execute\"", req.mode)
	}

	resource := usersCollectionResource
	digest := ""
	if req.operation != "list" {
		if err := validateEmail(req.subject); err != nil {
			return auditItemResult{}, err
		}
		digest = subjectDigest(req.subject)
		resource = usersSubjectPrefix + digest
	} else if req.subject != "" {
		// Refused rather than ignored. `list` takes no subject, and silently dropping
		// one would let an operator believe they had listed a single user.
		return auditItemResult{}, usageErrf("--subject is not accepted for --operation list, which acts on the whole pool")
	}

	ttlDays, err := auditTTLDays(req.cfgPath)
	if err != nil {
		return auditItemResult{}, err
	}

	item, err := renderAuditItem(keys.TenantID(req.tenant), req.actor, action, resource, ttlDays)
	if err != nil {
		return auditItemResult{}, err
	}
	return auditItemResult{
		Action:        action,
		ApplyAction:   modes["execute"],
		Resource:      resource,
		SubjectDigest: digest,
		TTLDays:       ttlDays,
		Item:          item,
	}, nil
}

// auditTTLDays reads retention.audit_days from the instance config (§7.4).
//
// Required, not defaulted. audit.New has a documented fallback, but taking it here
// would mean a users.sh record silently retained for a different window than every
// handler's record in the same table — and §Phase 0 is explicit that "a missing
// threshold must fail the deploy, never fall back to a hardcoded default". The
// config file is already resolved by users.sh for the region, so requiring it costs
// the caller nothing.
func auditTTLDays(path string) (int, error) {
	if path == "" {
		return 0, usageErrf("--config is required: retention.audit_days comes from the instance config, never from a default (§7.4)")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return 0, err
	}
	if cfg.Retention.AuditDays == nil || *cfg.Retention.AuditDays <= 0 {
		// Unreachable through config.Load, which validates the key. Kept because the
		// alternative to this branch is a nil dereference in an admin script.
		return 0, fmt.Errorf("retention.audit_days is missing from %s", path)
	}
	return *cfg.Retention.AuditDays, nil
}

// subjectDigest is the audit record's stand-in for the email address.
//
// # Why a digest and not the address
//
// §9.2 keeps PII out of logs, and audit.validate() enforces the stronger form for
// this store specifically: an actor containing '@' is refused, "PII must not enter
// the audit log, which is the longest-retained store in the system". A users.sh
// record still has to identify *which* user was added or removed, or it cannot
// answer the question the log exists for.
//
// # Why a digest and not the Cognito `sub`
//
// The `sub` would be the more natural identifier, and it is deliberately not used:
//
//   - It does not exist yet for the operation that most needs recording. `add`
//     writes its record BEFORE calling Cognito (audit.Record: "call it before the
//     access, not after", and §9.3 requires that ordering explicitly), and the `sub`
//     is minted by the create call. Recording it would mean recording after, which
//     is the ordering that leaves an unrepairable gap when the script dies mid-run.
//   - An operator investigating starts from an email — that is what a support
//     request contains. Resolving it to a `sub` requires the pool to still hold the
//     user, which is exactly false for the `remove` records anyone would be
//     investigating.
//
// The digest is reproducible from the address alone, in either direction of the
// investigation, and survives the user's deletion.
//
// # What it does and does not protect
//
// It is pseudonymisation, not anonymisation: an unsalted hash of a known address is
// confirmable by anyone holding the address, and a small dictionary recovers common
// ones. That is accepted, and it is the right trade here — the property being bought
// is that the seven-year store contains no address to leak, read, or export, not
// that an attacker holding a guess cannot confirm it. A salted or keyed digest would
// buy that stronger property and lose the one that makes this useful: an operator
// with an email and no access to a secret can still find the records. Where the
// stronger property is needed the answer is a keyed digest under the tenant's
// kms_key_id (§9.3), which is a commercial-phase provisioning change.
//
// The address is lower-cased and trimmed first, because Cognito treats the username
// as case-insensitive for a pool with UsernameAttributes: [email], so two spellings
// of the same address are the same user and must produce the same digest.
func subjectDigest(email string) string {
	sum := sha256.Sum256([]byte(normalizeEmail(email)))
	return hex.EncodeToString(sum[:])[:subjectDigestHexLen]
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// emailRe is the accepted shape for a subject.
//
// **Deliberately narrower than RFC 5321.** The RFC permits a quoted local part
// containing spaces, quotes, and backslashes; nothing in this system needs one, and
// every one of those characters is a quoting hazard on the two paths this value
// travels: the argv of an `aws cognito-idp` call, and the Cognito ListUsers filter
// expression `email = "..."`, which is itself double-quoted. Refusing the characters
// is a control; quoting them correctly in four places is a convention. The cost of
// the narrowness is an address no mail system in practice issues.
var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

// validateEmail refuses a subject that is not an address.
//
// Not pedantry: the user pool declares UsernameAttributes: [email], so a
// non-address reaches `admin-create-user` as a username that can never be verified
// and can never receive an invite — a user that exists, cannot sign in, and cannot be
// found by the operator who created it.
//
// **The refusal describes the value; it never quotes it.** Same rule as
// audit.validationError, and for the same reason: this message goes to stderr, and
// stderr on a CI step is a log (§9.2). Length and rule are the diagnosis; the address
// is not.
func validateEmail(email string) error {
	if strings.TrimSpace(email) == "" {
		return usageErrf("--subject is required for this operation: the user is identified by email address")
	}
	if len(email) > emailMax {
		return usageErrf("--subject is %d bytes, over RFC 5321's %d-byte maximum; that is not an address", len(email), emailMax)
	}
	if !utf8.ValidString(email) {
		return usageErrf("--subject is not valid UTF-8 (%d bytes)", len(email))
	}
	if !emailRe.MatchString(email) {
		// One message for every shape failure, because a per-rule message would have to
		// describe which characters were rejected — and naming them alongside a byte
		// count is how the value gets reconstructed from a log line.
		return usageErrf("--subject (%d bytes) is not an email address of the accepted shape: local@domain.tld, where the local part is letters, digits, and ._%%+- only. The pool's username IS the address (UsernameAttributes: [email]), and characters outside this set are a quoting hazard in the ListUsers filter expression", len(email))
	}
	return nil
}

// renderAuditItem produces the DynamoDB item for one audit record.
//
// **It does not know the record's shape.** It writes through audit.Auditor into
// repository.Memory and serialises whatever the audit package stored, so the item is
// the one every handler writes rather than a second copy that can drift from it
// (§11.2). Memory rejects exactly what the DynamoDB adapter rejects — validateItem is
// one function shared by both — so an item that renders here is an item that stores.
func renderAuditItem(tenant keys.TenantID, actor, action, resource string, ttlDays int) (map[string]any, error) {
	repo := repository.NewMemory()
	clk := clock.System{}

	// WARN and above, to stderr. audit.Record logs the allowed access at DEBUG and a
	// rejected record at WARN; stdout carries the JSON contract, so anything on it
	// would make the output unparseable for users.sh. Stderr rather than discarded
	// because a rejection's structured line names the field and the rule that failed,
	// which is the whole of what makes it diagnosable — and it is content-free by
	// construction (audit.validationError).
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	aud := audit.New(repo, clk, ids.NewGenerator(clk), logger, ttlDays)

	// Allowed, always. There is no --result flag, and the omission is deliberate:
	// `denied` in this vocabulary means an authorisation check refused an access, and
	// users.sh has no such check to fail — the operator's authority to touch the pool
	// IS the IAM policy on the cognito-idp call, and a refusal there is recorded by
	// CloudTrail (§9.5), which is the correct substrate for an infrastructure denial.
	//
	// The record is written before the Cognito call, so an IAM denial leaves an
	// `allowed` record for an operation that did not happen. That asymmetry is
	// audit.Record's documented and intended one: "a log that claims one access too
	// many is auditable, a log missing one is not."
	//
	// IP and UA are empty: there is no request. audit.sanitize accepts that (a script
	// invocation is the case it names), and inventing the operator's workstation
	// address would be recording a claim as a fact.
	if err := aud.Allowed(context.Background(), audit.Access{
		Tenant:   tenant,
		Actor:    actor,
		Action:   action,
		Resource: resource,
	}); err != nil {
		return nil, err
	}

	stored := repo.Keys()
	if len(stored) != 1 {
		// Unreachable unless audit.Record starts writing more than one item, in which
		// case rendering one of them would silently drop the rest.
		return nil, fmt.Errorf("audit package wrote %d items, expected exactly 1; this renderer must be updated", len(stored))
	}
	item, err := repo.Get(context.Background(), stored[0])
	if err != nil {
		return nil, err
	}
	return attributeValues(*item)
}

// attributeValues converts a repository.Item to AWS CLI attribute-value JSON.
//
// Mirrors repository.marshalItem, which is unexported. The duplication is one level
// of encoding only — the item's *shape* comes from the audit package (see
// renderAuditItem) — and it is bounded by failing closed: any value or index this
// cannot encode faithfully is an error naming the attribute, never a dropped field.
// A record that stores successfully while missing what the writer believed it stored
// is not recoverable from the record.
func attributeValues(item repository.Item) (map[string]any, error) {
	if item.GSI1PK != "" || item.GSI1SK != "" {
		// §6.3 keeps audit records out of GSI1 — it is the highest-volume entity and
		// projecting it makes the index a second copy of the table. If that ever
		// changes, this must learn to write the index attributes rather than drop them,
		// and the symptom of dropping them is a record that exists and never appears in
		// a listing.
		return nil, fmt.Errorf("item carries GSI1 attributes, which an audit record must not (§6.3); this renderer must be updated")
	}
	out := make(map[string]any, len(item.Attrs)+3)
	for k, v := range item.Attrs {
		av, err := attributeValue(v)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", k, err)
		}
		out[k] = av
	}
	// The key attribute names are repository's (PK, SK, ttl), and they are written
	// last so a stray same-named entry in Attrs cannot displace them — repository
	// refuses that case outright, and this at least cannot render a wrong key.
	out["PK"] = map[string]any{"S": item.Key.PK}
	out["SK"] = map[string]any{"S": item.Key.SK}
	if item.TTL > 0 {
		// Only when positive, matching repository.marshalItem: a ttl of 0 is epoch
		// 1970, which makes the item immediately eligible for TTL deletion.
		out["ttl"] = map[string]any{"N": strconv.FormatInt(item.TTL, 10)}
	}
	return out, nil
}

func attributeValue(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return map[string]any{"S": t}, nil
	case bool:
		return map[string]any{"BOOL": t}, nil
	case int:
		return map[string]any{"N": strconv.FormatInt(int64(t), 10)}, nil
	case int64:
		return map[string]any{"N": strconv.FormatInt(t, 10)}, nil
	default:
		// The alarm, not a limitation to work around: the audit record is strings and
		// one epoch second today, and an attribute of any other shape means the audit
		// package changed under this renderer.
		return nil, fmt.Errorf("value of type %T cannot be rendered as an AWS CLI attribute value; the audit record's shape changed and chintanctl users audit-item must be updated", v)
	}
}
