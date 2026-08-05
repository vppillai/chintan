// Package kmsref is the per-tenant encryption key indirection (I8, §2A.1).
//
// I8: "All user data at rest is encrypted. Personal phase: AWS-managed keys
// (DynamoDB SSEEnabled, S3 AES256) — functionally equivalent at rest and free.
// Commercial phase: customer-managed KMS key, flipped via the kms_key_id
// indirection with no code change."
//
// §2A.1 explains why the *reference* is Phase 0 work while the key itself is not:
// "Moving from one shared CMK to per-tenant keys means re-encrypting the entire
// corpus. Storing a kms_key_id REFERENCE on the tenant record today — even when
// every tenant points at the same key — makes that a config change instead of a
// re-encryption. It also enables crypto-shredding (§9.3)."
//
// # What makes this indirection real rather than a stub
//
// §Phase 0 is explicit that "the resolver is on the real encryption path from the
// first write — an indirection that is only wired up when a CMK arrives is an
// indirection that has never been tested." Five properties are what deliver that,
// and each is a deliberate choice rather than an accident of implementation:
//
//  1. Its output is *consumed*, not merely computed. ForS3Put returns the
//     server-side-encryption parameters an S3 PutObject — or the signature of a
//     presigned PUT (I3) — must carry. A caller cannot write a tenant's object
//     without asking this package what to encrypt it under, so deleting the
//     resolver breaks today's writes rather than tomorrow's.
//
//  2. There is no default and no fallback. An absent, empty, or unparseable
//     kms_key_id is an error (ErrAbsent, ErrMalformed), never a substituted
//     service default. §6.3: it "is never null and never absent, because a
//     resolver with nothing to resolve is how the indirection quietly stops being
//     exercised." A tenant provisioned without a key reference therefore fails on
//     its first write, when someone is watching, instead of at CMK-flip time.
//
//  3. The classification changes caller behaviour *today*. §9.3's erasure
//     operation asks CryptoShreddable and gets false, which is what keeps its
//     report honest: object deletion does not reach S3 noncurrent versions,
//     DynamoDB PITR, or backups (G-021). A stub that answered "encrypted, fine"
//     would let a completed-erasure claim overclaim, and G-021's symptom is that
//     this is "discovered during audit, not during testing." The answer that
//     matters at flip time is not the classification alone but ErasureScope,
//     because a key that *can* be destroyed still does not cover everything the
//     tenant wrote before it was pointed at that key.
//
//  4. The commercial-phase branch is executed by tests now, including the two
//     mismatch refusals below. It is code that has run, not code that will be
//     written.
//
//  5. It makes no AWS calls. Classification is offline and syntactic, so this
//     package is exercised in the check suite that holds no credentials (§0.5A),
//     and the resolver is on the path there too. The cost of that choice is
//     stated honestly in Ref.ErasureCaveat: a bare key ID or key ARN cannot be
//     classified offline, and this package reports the resulting uncertainty
//     rather than guessing in the flattering direction. It also means no kms:*
//     permission is needed anywhere today, so neither the Lambda execution role
//     nor the agent boundary (§9.5) grows to accommodate the indirection.
//
// # The two silent failures it exists to catch
//
// Both are failures of the *flip*, which is the moment this package exists for —
// and both are invisible without a check, which is why they are refusals here
// rather than review items:
//
//   - S3 downgrade. Per-object SSE overrides a bucket default, so writing AES256
//     for a tenant whose bucket is configured with a CMK silently places that
//     object outside the CMK. It then survives crypto-shredding, and nothing
//     about the write looks wrong.
//   - DynamoDB table-level mismatch. DynamoDB encryption is configured on the
//     table, not per item, so a tenant record naming a CMK the table is not
//     encrypted under is a promise the storage layer cannot keep: destroying that
//     key shreds none of the tenant's DynamoDB records.
//
// Refusing a write is the right response to either, and refusing costs no audio:
// I2 keeps the local buffer until the server confirms upload, so a refused
// presigned PUT is retried once the misconfiguration is fixed rather than lost.
//
// # What this package does not do
//
// Resolving a key reference is not access to user content, so it writes no audit
// record (I13) — and neither is reading the two provenance attributes
// ErasureScope needs, which are infrastructure metadata about the key, not
// anything the user said; §9.3's erasure operation writes its own before
// executing. It
// performs no billable operation either, so it emits no metering event (I12) —
// KMS is $0.00 in the personal phase (§10.7), and once a CMK exists the
// per-request KMS charges are incurred *inside* S3 and DynamoDB and appear on the
// bill without passing through any adapter we own. Inventing a synthetic event
// here would make metering look complete while measuring nothing real; the honest
// home for that cost is the cost model.
//
// It also never writes. Provisioning a tenant's key reference — and the
// kms_key_id_since stamp that goes with it — is an out-of-band state change and
// belongs to an operational script with a dry run (I16).
package kmsref

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// TenantAttrKMSKeyID is the tenant record attribute holding the key reference
// (§6.3). Named here because this package is the only reader of it.
const TenantAttrKMSKeyID = "kms_key_id"

// TenantAttrKMSKeyIDSince is the RFC3339 UTC instant at which the tenant was
// pointed at its current kms_key_id.
//
// It exists because crypto-shredding is bounded in *time*, not just in kind, and
// nothing in a key reference records that boundary. An object written while the
// tenant pointed at alias/aws/s3 is SSE-S3 under a key the account cannot
// destroy; repointing the tenant at a CMK afterwards does not re-encrypt it.
// Destroying the CMK therefore leaves every pre-repoint object readable — the
// G-021 failure exactly ("a completed erasure request leaves recoverable data,
// discovered during audit, not during testing"), and invisible because the
// surviving objects are indistinguishable from the shredded ones.
//
// What erasure must do with it (§9.3):
//
//   - Objects whose last-modified time is at or after this instant are covered by
//     key destruction and may be reported as unrecoverable once the key is
//     actually destroyed.
//   - Objects older than it are not. They must be deleted and their retention
//     windows waited out, and the report must say so rather than folding them
//     into the crypto-shredding claim.
//   - If this attribute is absent or unparseable, *every* object is treated as
//     older. Absence is not evidence of a fresh key; it is absence of evidence,
//     and the same reasoning as I14's "absence of consent is refusal" applies.
//   - If it is not later than created_at, no object can predate it and the claim
//     covers all of the tenant's objects. That is the only proof of completeness
//     obtainable without enumerating objects, and it needs no clock read.
//
// Written only by the operational script that repoints a tenant, in the same
// update as kms_key_id (I16). A script that changes one without the other makes
// this package under-claim, which costs an over-cautious erasure report; the
// reverse ordering cannot make it over-claim.
const TenantAttrKMSKeyIDSince = "kms_key_id_since"

// TenantAttrCreatedAt is the tenant record's creation instant (§6.3), read here
// only as the proof described on TenantAttrKMSKeyIDSince: a tenant cannot have
// written an object before it existed, so a repoint that is not later than
// creation means there is no pre-repoint data to caveat.
const TenantAttrCreatedAt = "created_at"

// TenantAttrKMSKeyIDStampedFor records WHICH key reference kms_key_id_since was taken for.
//
// Without it the stamp outlives its subject: nothing ties kms_key_id_since to a particular
// kms_key_id, so repointing a tenant from one CMK to another leaves the stamp untouched and
// the completeness claim survives a change that invalidates it. An ordinary second
// provisioning is enough — no script bug required.
const TenantAttrKMSKeyIDStampedFor = "kms_key_id_stamped_for"

// The AWS-managed key aliases §6.3 names as the personal-phase value of
// kms_key_id.
//
// Exported so that tenant provisioning and deployment wiring reference this
// package instead of retyping a literal: I8's flip is only "a provisioning change
// with no code change" if exactly one place decides what a tenant's key reference
// says. Two places would drift, and the drift would surface as ErrKeyMismatch on
// a write rather than as a visible configuration difference.
//
// These are not an I5 violation. I5 governs provider, model, endpoint, and API
// version — everything that must be swappable at deploy time. These two strings
// are AWS's fixed names for its service-default keys, they are not swappable, and
// §6.3 names them literally.
const (
	AWSManagedS3       = "alias/aws/s3"
	AWSManagedDynamoDB = "alias/aws/dynamodb"
)

// Sentinel errors. Compared with errors.Is so a caller can branch on the reason:
// ErrAbsent is a provisioning defect for an operator to fix, ErrKeyMismatch is a
// refusal to write, and neither should be retried.
var (
	// ErrAbsent reports a tenant record with no usable kms_key_id.
	//
	// The message states the substitution that deliberately does not happen,
	// because the tempting "fix" for this error is a default (§6.3).
	ErrAbsent = errors.New("kmsref: kms_key_id is absent; §6.3 requires it never null and never absent, and no default is substituted")

	// ErrMalformed reports a value that is present but is not a KMS key
	// reference.
	ErrMalformed = errors.New("kmsref: value is not a recognised KMS key reference")

	// ErrKeyMismatch reports that a tenant's key reference and the storage
	// resource's actual encryption disagree, so the write is refused.
	ErrKeyMismatch = errors.New("kmsref: tenant key reference disagrees with the storage resource's configured key")
)

// Kind is what class of key a reference names. §9.3 depends on the distinction:
// crypto-shredding requires a key the account can schedule for deletion, which an
// AWS-managed key is not.
type Kind string

const (
	// KindAWSManaged is an AWS service-default key (alias/aws/...). The account
	// cannot delete it, so it can never be crypto-shredded.
	KindAWSManaged Kind = "aws_managed"

	// KindCustomerManaged is a key the account owns, named by an alias that is
	// not under the reserved alias/aws/ prefix.
	KindCustomerManaged Kind = "customer_managed"

	// KindUnverified is a bare key ID or key ARN.
	//
	// It is a distinct kind rather than an assumption because an AWS-managed key
	// and a customer-managed key have *identically shaped* key ARNs; only
	// kms:DescribeKey's KeyManager field separates them. Classifying such a
	// reference as customer-managed would be the optimistic answer — it would
	// promise crypto-shredding against a key that may not be deletable — so it
	// is treated as unverified and reported as not shreddable. Under-claiming is
	// recoverable; over-claiming is discovered during an audit (G-021).
	KindUnverified Kind = "unverified"
)

// Ref is a resolved, classified key reference. The zero value is unusable and
// every method that acts on one refuses it, so an unresolved Ref cannot become an
// accidental "encrypt with the service default".
type Ref struct {
	raw  string
	kind Kind
}

// S3ServiceDefault returns the reference for S3's AWS-managed key.
//
// This and DynamoDBServiceDefault are the single source of the personal-phase
// value, for tenant provisioning and for the Deployment passed to New. They
// return a Ref rather than a string so no call site has to Parse a constant and
// then discard the error — a discarded error there is how a zero Ref would reach
// a write path.
func S3ServiceDefault() Ref { return Ref{raw: AWSManagedS3, kind: KindAWSManaged} }

// DynamoDBServiceDefault returns the reference for DynamoDB's AWS-managed key.
func DynamoDBServiceDefault() Ref { return Ref{raw: AWSManagedDynamoDB, kind: KindAWSManaged} }

// Reference syntax. Validated syntactically and never against AWS, per property
// 5 in the package comment.
var (
	// arnRe splits a KMS ARN into its resource part. The partition is permissive
	// (aws, aws-us-gov, aws-cn) because a GovCloud or China deployment is a
	// region choice, not a code change (§2A.1's region attribute). The service
	// must be kms: an ARN for anything else in this field is a paste error, and
	// accepting it would produce a reference that no encryption call can use.
	arnRe = regexp.MustCompile(`^arn:aws[a-z0-9-]*:kms:([a-z0-9-]+):(\d{12}):(.+)$`)

	// aliasNameRe is the alias name after the "alias/" prefix, per KMS's own
	// rule: 1-256 characters of alphanumerics, colon, slash, underscore, hyphen.
	// Whitespace is excluded, so a value that looks like prose is rejected
	// rather than sent to KMS to fail there.
	//
	// The first character must be alphanumeric, which KMS itself does not
	// require. "/" is a legal alias character, so "alias//" and "alias//prod"
	// are alias names KMS would accept — and in practice they are a truncation
	// or a double-joined path, not a key someone created. Accepting one
	// classifies it KindCustomerManaged and promises crypto-shredding against
	// nothing (§9.3, G-021).
	aliasNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:/_-]{0,255}$`)

	// keyIDRe covers both single-region key IDs (a UUID) and multi-region key
	// IDs ("mrk-" plus 32 hex). Tighter than KMS requires so that a truncated or
	// mangled ID fails here instead of at the first PutObject.
	//
	// Matched against a lowercased copy (see Parse): the hex digits of a UUID
	// carry no case meaning, and this is a value operators paste between the KMS
	// console, tickets, and IaC. Rejecting an uppercased ARN as "does not end in
	// a KMS key ID" refuses every write for that tenant with a message that
	// misdescribes the problem, and per I2 the audio then sits in the client
	// buffer until someone diagnoses it.
	keyIDRe = regexp.MustCompile(`^(mrk-[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

	// awsServiceSegmentRe is the service segment of a reserved alias — the "s3"
	// of alias/aws/s3. Exactly one segment: every AWS-managed alias has that
	// shape, so "alias/aws/s3/extra" is a paste error rather than a key, and
	// recording it as a valid AWS-managed reference means the mismatch checks
	// compare against a value no resource is configured with.
	awsServiceSegmentRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

	// awsManagedAliasPrefix is reserved by AWS for service-default keys, which is
	// exactly what makes it a reliable offline signal for KindAWSManaged: a
	// customer cannot create an alias under it.
	awsManagedAliasPrefix = "alias/aws/"

	// aliasPrefix is the literal KMS requires. Case-sensitive: "Alias/..." is not
	// an alias to KMS, and naming the casing in the refusal is the difference
	// between a one-character fix and a puzzled hour.
	aliasPrefix = "alias/"
)

// Parse classifies a key reference.
//
// Accepted forms are an alias ("alias/name"), an alias ARN, a key ARN, and a bare
// key ID. Anything else is ErrMalformed rather than a best-effort guess: this
// value decides what a tenant's data is encrypted under and whether erasure can
// claim completeness, so an unrecognised value is a provisioning defect to fix,
// not an input to interpret.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, ErrAbsent
	}

	resource := s
	if strings.HasPrefix(s, "arn:") {
		m := arnRe.FindStringSubmatch(s)
		if m == nil {
			// Named separately from the generic malformed case because the two
			// realistic causes — an ARN for the wrong service, and an ARN whose
			// region or 12-digit account is missing — are both operator errors
			// that read as "the value looks right" at a glance.
			return Ref{}, fmt.Errorf("kmsref: %q is not a KMS ARN of the form arn:<partition>:kms:<region>:<account>:<resource>: %w", s, ErrMalformed)
		}
		resource = m[3]
	}

	// Classification decides on the lowercased form and *records* the verbatim
	// one. The two reasons differ: reserved-prefix detection has to be
	// case-insensitive so that a near-miss cannot slip into the customer-managed
	// branch, while key IDs are lowercased because their hex is case-neutral.
	lower := strings.ToLower(resource)

	// reservedRoot is awsManagedAliasPrefix without its trailing slash, so that
	// "alias/aws" — alias/aws/s3 truncated at the last slash — is recognised as
	// the reserved neighbourhood rather than read as a customer alias named "aws".
	reservedRoot := strings.TrimSuffix(awsManagedAliasPrefix, "/")

	switch {
	case lower == reservedRoot || strings.HasPrefix(lower, reservedRoot+"/"):
		// Anything in the reserved neighbourhood that is not *exactly* the
		// reserved prefix is refused, including case variants ("alias/AWS/s3")
		// and truncations ("alias/aws"). The alternative is what this replaced:
		// they fell through to the customer-managed branch, CryptoShreddable()
		// answered true for a personal-phase tenant with no CMK at all, and the
		// §9.3 report printed "crypto-shredding is available" while
		// ScheduleKeyDeletion failed against an alias that does not exist.
		// Refusing a genuine customer alias that differs from the reserved prefix
		// only in case costs an operator one edit and no audio (I2).
		if !strings.HasPrefix(resource, awsManagedAliasPrefix) {
			return Ref{}, fmt.Errorf("kmsref: %q is a truncated or differently-cased form of the reserved %s prefix, so it names no key; "+
				"classifying it as customer-managed would promise crypto-shredding against a key that does not exist (§9.3, G-021): %w",
				s, awsManagedAliasPrefix, ErrMalformed)
		}
		service := strings.TrimPrefix(resource, awsManagedAliasPrefix)
		if !awsServiceSegmentRe.MatchString(service) {
			return Ref{}, fmt.Errorf("kmsref: %q is under the reserved %s prefix but %q is not a service segment "+
				"(one lowercase segment, no further slash): %w", s, awsManagedAliasPrefix, service, ErrMalformed)
		}
		return Ref{raw: s, kind: KindAWSManaged}, nil

	case strings.HasPrefix(lower, aliasPrefix):
		if !strings.HasPrefix(resource, aliasPrefix) {
			return Ref{}, fmt.Errorf("kmsref: %q does not start with the case-sensitive %q prefix KMS requires: %w", s, aliasPrefix, ErrMalformed)
		}
		if !aliasNameRe.MatchString(strings.TrimPrefix(resource, aliasPrefix)) {
			return Ref{}, fmt.Errorf("kmsref: %q is not a valid KMS alias: %w", s, ErrMalformed)
		}
		return Ref{raw: s, kind: KindCustomerManaged}, nil

	case strings.HasPrefix(lower, "key/"):
		if !keyIDRe.MatchString(strings.TrimPrefix(lower, "key/")) {
			return Ref{}, fmt.Errorf("kmsref: %q does not end in a KMS key ID: %w", s, ErrMalformed)
		}
		return Ref{raw: s, kind: KindUnverified}, nil

	case keyIDRe.MatchString(lower):
		return Ref{raw: s, kind: KindUnverified}, nil
	}

	return Ref{}, fmt.Errorf("kmsref: %q is not an alias, an alias ARN, a key ARN, or a key ID: %w", s, ErrMalformed)
}

// ID returns the reference exactly as recorded, which is what an encryption call
// passes to AWS.
func (r Ref) ID() string { return r.raw }

// Kind reports the classification.
func (r Ref) Kind() Kind { return r.kind }

// String makes a Ref safe to interpolate into an error or a log line. A key
// reference is infrastructure metadata, not user content or PII, so §9.2 does not
// exclude it — unlike the tenant ID, which this package keeps out of its
// messages because in the personal phase tenant_id == user_id.
func (r Ref) String() string {
	if r.IsZero() {
		return "<unresolved>"
	}
	return r.raw
}

// IsZero reports an unresolved reference.
func (r Ref) IsZero() bool { return r.raw == "" || r.kind == "" }

// CryptoShreddable reports whether this key is one the account can destroy at
// all (§9.3).
//
// True only for KindCustomerManaged. False for an AWS-managed key because the
// account cannot delete one, and false for KindUnverified because a bare key ARN
// does not say who manages it.
//
// True is a necessary condition for a crypto-shredding claim and not a sufficient
// one, and reading it as sufficient is the G-021 failure: destroying a key reaches
// only what was encrypted under it, which excludes everything the tenant wrote
// before it was pointed at that key and excludes DynamoDB entirely unless the
// table is under the same key. §9.3 must ask Resolver.ErasureScope for the claim
// it prints; this method answers only whether key destruction is on the table.
func (r Ref) CryptoShreddable() bool { return r.kind == KindCustomerManaged }

// ErasureCaveat states what erasing under this key cannot guarantee, in a form
// §9.3's erasure report can quote directly.
//
// It is never empty — not even for a customer-managed key. G-021's failure is a
// "completed" erasure that left recoverable data, and the way that survives
// review is a caveat that is technically available but not printed. Returning a
// non-empty string in every case means the report has nothing to omit by
// accident.
//
// A Ref is a pure value: it knows the key, and it cannot know when this tenant
// was pointed at it or what the DynamoDB table is encrypted with. So the
// customer-managed caveat states the pre-repoint and table-level limits
// *unconditionally* rather than asserting coverage it has no basis for. A report
// that wants those limits resolved against the tenant record and the deployment
// calls Resolver.ErasureScope; this text is what remains true without them.
func (r Ref) ErasureCaveat() string {
	switch r.kind {
	case KindCustomerManaged:
		return "crypto-shredding is available for whatever is encrypted under this key: scheduling it for deletion renders those " +
			"objects unrecoverable, including copies in S3 noncurrent versions and backups that object deletion does not reach " +
			"(G-021). Which objects those are cannot be determined from this key reference, and two exclusions apply unless " +
			"proven otherwise: anything written before this tenant was pointed at this key is encrypted under the key in force " +
			"at the time and survives this key's destruction, and DynamoDB encrypts at table level, so records — and their PITR " +
			"and backups — are under the table's key rather than this one. Erasure must resolve both against the tenant's " +
			TenantAttrKMSKeyIDSince + " and the table's configured key before claiming completeness. KMS enforces a " +
			"pending-deletion window of at least 7 days, during which the deletion can be cancelled and the data is still " +
			"recoverable — erasure is not complete until the key is actually destroyed (§9.3)"
	case KindAWSManaged:
		return "crypto-shredding is unavailable: this is an AWS-managed key, which the account cannot destroy (I8). " +
			"Erasure is object deletion plus waiting out the retention windows — S3 noncurrent versions, DynamoDB PITR, " +
			"and backups retain copies that object-level deletion does not reach (§9.3, G-021)"
	case KindUnverified:
		return "crypto-shredding cannot be claimed: this is a bare key ID or key ARN, and an AWS-managed key has an " +
			"identically shaped ARN, so whether the account can destroy it is only knowable from kms:DescribeKey's " +
			"KeyManager field. Record a customer alias instead, or verify with DescribeKey before claiming erasure is " +
			"complete (§9.3, G-021)"
	}
	return "no key reference resolved, so no erasure guarantee can be stated at all (§6.3)"
}

// SSE header values for S3 (§6.2).
const (
	// SSES3 is SSE-S3 with the AWS-managed key: AES256, and free. §6.2 specifies
	// it for the personal phase.
	SSES3 = "AES256"

	// SSEKMS is SSE-KMS, which bills per request against the named key (G-020).
	SSEKMS = "aws:kms"
)

// S3 request header names for server-side encryption. Named here because the
// presigner must sign exactly these and the client must send exactly these — a
// presigned PUT whose headers do not match its signature is rejected with a 403
// that says nothing about encryption (I3 makes presigned PUT the only upload
// path, so this is the normal case, not an edge one).
const (
	HeaderSSE      = "x-amz-server-side-encryption"
	HeaderSSEKeyID = "x-amz-server-side-encryption-aws-kms-key-id"
)

// S3Encryption is the server-side-encryption parameters for one S3 write.
//
// Fields are unexported and there is no literal constructor, so the only way to
// obtain a usable one is ForS3Put. This is the same fail-closed shape Ref has, and
// for the same reason: this is the value that reaches the wire, and an
// S3Encryption assembled by hand — or a zero one left behind by an ignored error —
// is how an object gets written under terms nothing recorded. A caller that wants
// "encrypt with whatever the bucket does" cannot express it here.
type S3Encryption struct {
	// sse is SSES3 or SSEKMS — the ServerSideEncryption field of a PutObject, or
	// the value of HeaderSSE on a presigned PUT.
	sse string

	// kmsKeyID is set only for SSEKMS.
	//
	// Deliberately empty in the AWS-managed case even though a key reference was
	// resolved. Passing "alias/aws/s3" as an SSE-KMS key ID is accepted by S3 and
	// converts free SSE-S3 into billed SSE-KMS on every object — a per-request
	// charge on the highest-volume write in the system, contradicting I8's "free"
	// and G-020's budget reasoning, with no change in protection at rest.
	kmsKeyID string
}

// SSE reports the server-side-encryption algorithm: SSES3 or SSEKMS.
func (e S3Encryption) SSE() string { return e.sse }

// KMSKeyID reports the key ID to send with SSEKMS, empty for SSES3.
func (e S3Encryption) KMSKeyID() string { return e.kmsKeyID }

// IsZero reports encryption parameters that were never resolved.
func (e S3Encryption) IsZero() bool { return e.sse == "" && e.kmsKeyID == "" }

// Headers returns the request headers this encryption requires, so the presigner
// and the client-side upload contract derive from one source instead of two.
//
// The SSE header is emitted even in the AES256 case, where a bucket default would
// also cover it. Relying on the default makes every object's encryption depend on
// bucket configuration that nothing in the write path can see; stating it means a
// bucket-policy or default change cannot silently write an object under different
// terms than the tenant's key reference records.
//
// It returns an error rather than a map alone because the failure it guards is
// silent in both directions: a presigner given HeaderSSE with an empty value
// either 403s the client's PUT with a signature complaint that never mentions
// encryption, or — with a header-normalising signer — drops the empty header and
// lets the object take the bucket default. That default is the
// SSE-KMS-billed-under-a-key-nothing-recorded outcome G-020 exists to prevent, and
// no error is raised anywhere along the way.
func (e S3Encryption) Headers() (map[string]string, error) {
	switch {
	case e.sse != SSES3 && e.sse != SSEKMS:
		return nil, fmt.Errorf("kmsref: server-side encryption is %q, not %s or %s, so there is nothing to sign; "+
			"an empty SSE header signs away to the bucket default (§6.2, G-020): %w", e.sse, SSES3, SSEKMS, ErrAbsent)
	case e.sse == SSEKMS && e.kmsKeyID == "":
		return nil, fmt.Errorf("kmsref: %s was requested with no key ID, which S3 reads as the bucket or account default key "+
			"rather than this tenant's (§9.3, G-020): %w", SSEKMS, ErrAbsent)
	case e.sse == SSES3 && e.kmsKeyID != "":
		// Unreachable through ForS3Put, and checked anyway: SSE-S3 with a key ID
		// is the combination that turns the free path into the billed one, and it
		// is exactly what a future edit to ForS3Put would produce by mistake.
		return nil, fmt.Errorf("kmsref: %s carries a key ID, which would bill per object for identical protection at rest "+
			"(I8, G-020): %w", SSES3, ErrKeyMismatch)
	}
	h := map[string]string{HeaderSSE: e.sse}
	if e.kmsKeyID != "" {
		h[HeaderSSEKeyID] = e.kmsKeyID
	}
	return h, nil
}

// ForS3Put returns the encryption parameters for writing this tenant's object to
// a bucket whose default encryption is bucketKey.
//
// bucketKey is required because per-object SSE *overrides* the bucket default.
// Once a CMK exists, writing AES256 for a tenant still recorded as AWS-managed
// would place that object outside the CMK, where crypto-shredding cannot reach
// it, and the write itself would look entirely normal. Refusing is the only point
// at which that is visible.
func (r Ref) ForS3Put(bucketKey Ref) (S3Encryption, error) {
	if r.IsZero() {
		return S3Encryption{}, fmt.Errorf("kmsref: no tenant key reference resolved: %w", ErrAbsent)
	}
	if bucketKey.IsZero() {
		return S3Encryption{}, fmt.Errorf("kmsref: bucket key reference is unset, so a tenant reference cannot be checked against it: %w", ErrAbsent)
	}

	if r.kind == KindAWSManaged {
		if bucketKey.kind != KindAWSManaged {
			return S3Encryption{}, fmt.Errorf("kmsref: tenant resolves to the AWS-managed key %s while the bucket default is %s; "+
				"writing AES256 would override that default and put the object beyond crypto-shredding (§9.3, G-021): %w",
				r, bucketKey, ErrKeyMismatch)
		}
		return S3Encryption{sse: SSES3}, nil
	}

	// Customer-managed and unverified both name a specific key, so the object is
	// encrypted under it regardless of the bucket default. No mismatch check
	// applies in this direction: a per-tenant CMK differing from the bucket
	// default is the intended commercial-phase arrangement, not a fault.
	return S3Encryption{sse: SSEKMS, kmsKeyID: r.raw}, nil
}

// CheckDynamoPut reports whether writing this tenant's records to a table
// encrypted under tableKey keeps the promise the tenant's key reference makes.
//
// There is nothing to return: DynamoDB encryption is configured on the table, not
// per item, so a write passes no key. That is precisely why a check is needed
// rather than a directive — the write will succeed under whatever the table uses,
// so a disagreement between the tenant record and the table produces no error of
// its own and no wrong-looking data. It surfaces later as an erasure claim that
// destroyed a key which shredded nothing.
//
// This is the call that puts the resolver on the DynamoDB write path today. It
// passes trivially in the personal phase — every tenant and the table are all
// AWS-managed — which is the §Phase 0 pattern for checks whose subject arrives in
// a later phase: wired now, trivially passing, never skipped.
func (r Ref) CheckDynamoPut(tableKey Ref) error {
	if r.IsZero() {
		return fmt.Errorf("kmsref: no tenant key reference resolved: %w", ErrAbsent)
	}
	if tableKey.IsZero() {
		return fmt.Errorf("kmsref: table key reference is unset, so a tenant reference cannot be checked against it: %w", ErrAbsent)
	}

	switch {
	case r.kind == KindAWSManaged && tableKey.kind == KindAWSManaged:
		// Today's path. The service segment may differ — a tenant recording
		// alias/aws/s3 against a table recording alias/aws/dynamodb — and that
		// is not a fault: with an AWS-managed key there is nothing to pass per
		// request and nothing to shred, so the two references assert the same
		// fact. Comparing them textually here would fail every write for a
		// reason that has no consequence.
		return nil

	case r.kind == KindAWSManaged:
		return fmt.Errorf("kmsref: tenant resolves to the AWS-managed key %s while the table is encrypted under %s; "+
			"DynamoDB encrypts at table level, so the tenant's records are under the table's key and its own key "+
			"reference misdescribes where its data is: %w", r, tableKey, ErrKeyMismatch)

	case tableKey.kind == KindAWSManaged:
		return fmt.Errorf("kmsref: tenant resolves to %s but the table is encrypted under the AWS-managed key %s; "+
			"DynamoDB encrypts at table level, so destroying the tenant's key would shred none of its records and "+
			"erasure would overclaim (§9.3, G-021): %w", r, tableKey, ErrKeyMismatch)

	case r.raw != tableKey.raw:
		// Textual comparison, and deliberately strict. An alias and the key ARN
		// it points at may name the same key, but proving that needs a KMS call
		// this package does not make (property 5). Refusing a semantically-equal
		// pair recorded in two different forms costs an operator one edit;
		// accepting a genuinely different pair costs a tenant's erasure
		// guarantee.
		return fmt.Errorf("kmsref: tenant resolves to %s but the table is encrypted under %s; "+
			"per-tenant DynamoDB keys need a table per key, and matching an alias to a key ARN requires "+
			"kms:DescribeKey, which this package does not call: %w", r, tableKey, ErrKeyMismatch)
	}
	return nil
}

// Deployment records what the storage resources are *actually* encrypted with, as
// declared in IaC (§6.2 bucket AES256, §6.3 table SSEEnabled).
//
// It is separate from the per-tenant reference because they are two different
// facts that the flip to a CMK changes at different times, and the window between
// them is where both silent failures in the package comment live.
type Deployment struct {
	// Bucket is the bucket's default encryption.
	Bucket Ref

	// Table is the DynamoDB table's SSE configuration.
	Table Ref
}

// ErasureScope is what §9.3's erasure operation needs in order to state what
// destroying a tenant's key actually reaches.
//
// It exists because Ref.CryptoShreddable answers a narrower question than the
// report asks. Crypto-shredding is bounded three ways, and a key reference knows
// only the first:
//
//   - by kind — an AWS-managed key cannot be destroyed by the account at all;
//   - in time — objects written before the tenant was pointed at this key are
//     encrypted under the previous one and survive its destruction, which is the
//     transition this whole package exists for and the one where an unqualified
//     claim is exactly G-021's "completed erasure request leaves recoverable
//     data";
//   - by store — DynamoDB encrypts at table level, so destroying a tenant key
//     shreds no items unless the table itself is under that key. CheckDynamoPut
//     exists because those two can differ.
//
// So the scope pairs the reference with the tenant record's kms_key_id_since and
// the deployment's table key. Every unknown resolves against the claim rather than
// for it: no recorded repoint instant means every object is treated as
// pre-repoint. Under-claiming costs an over-cautious erasure report; over-claiming
// is discovered by an auditor.
type ErasureScope struct {
	ref   Ref
	table Ref

	// since is the recorded repoint instant, verbatim, or "" when absent or
	// unusable.
	since string

	// allObjectsUnderKey records the one proof of completeness obtainable
	// offline: a repoint no later than the tenant's creation.
	allObjectsUnderKey bool

	// provenanceDefect names a kms_key_id_since that is present but unusable, so
	// the report shows a provisioning defect instead of quietly degrading to the
	// unknown case.
	provenanceDefect string

	// aliasCannotProve records that the reference is an alias, so no offline stamp can
	// establish which objects a destruction would reach. Distinct from provenanceDefect:
	// nothing is misconfigured, the proof is simply unobtainable.
	aliasCannotProve bool
}

// newErasureScope classifies the provenance attributes. Pure, and it reads no
// clock: the only comparison is between two stored timestamps, which is what makes
// the completeness proof available to the credential-free check suite (§0.5A).
func newErasureScope(ref, table Ref, attrs map[string]any) ErasureScope {
	s := ErasureScope{ref: ref, table: table}

	raw, sinceT, defect := timestampAttr(attrs, TenantAttrKMSKeyIDSince)
	if defect != "" {
		s.provenanceDefect = defect
		return s
	}
	if raw == "" {
		return s
	}
	s.since = raw

	// **An alias can never support a completeness claim, however early it was stamped.**
	//
	// An alias is a mutable pointer: "aws kms update-alias" repoints it at a different CMK
	// with no change to any tenant record and no event this package can observe. So a stamp
	// naming an alias proves nothing about the key that will actually be destroyed — and in
	// this design every customer-managed reference IS an alias (see KindCustomerManaged), so
	// the widening below is unreachable today.
	//
	// That is the honest outcome rather than a shortcoming. The previous version flipped
	// CoversAllObjects() true the moment a tenant was repointed onto a CMK and asserted that
	// destroying it reached "copies in S3 noncurrent versions, DynamoDB PITR, and backups" —
	// false for every object written before the repoint, which are SSE-S3 under the
	// AWS-managed key and survive untouched. Exactly the over-claim G-021 describes: "A
	// completed erasure request leaves recoverable data. Discovered during audit, not during
	// testing."
	//
	// Supporting the claim would need an immutable key identity recorded at stamp time AND a
	// live KMS lookup confirming the alias still resolves to it. Neither is available offline,
	// and erasure must not claim what it cannot verify.
	if strings.HasPrefix(ref.ID(), "alias/") {
		s.aliasCannotProve = true
		return s
	}

	created, createdT, defect := timestampAttr(attrs, TenantAttrCreatedAt)
	if defect != "" || created == "" {
		// Without a usable created_at there is nothing to compare against, so the
		// repoint instant stands as a boundary rather than as proof of
		// completeness. A malformed created_at is not reported as a provenance
		// defect here: it is not this package's attribute to validate, and the
		// consequence — an under-claim — is already the safe direction.
		return s
	}
	// Not After, rather than Before: a tenant provisioned straight onto its key
	// records the same instant for both, and requiring strictly-earlier would
	// make that ordinary case caveat data that cannot exist.
	s.allObjectsUnderKey = !sinceT.After(createdT)
	return s
}

// timestampAttr reads an RFC3339 string attribute, returning it both verbatim and
// parsed so that nothing downstream parses it a second time — a second parse means
// a second failure branch that no test can reach and no reader can trust.
//
// Returns a zero raw value when the attribute is absent or NULL, and a defect
// description — naming the attribute, never its value or the tenant (§9.2) — when
// it is present but unusable.
func timestampAttr(attrs map[string]any, name string) (string, time.Time, string) {
	v, ok := attrs[name]
	if !ok || v == nil {
		return "", time.Time{}, ""
	}
	s, ok := v.(string)
	if !ok {
		return "", time.Time{}, fmt.Sprintf("the tenant record's %s attribute is not a string", name)
	}
	if strings.TrimSpace(s) == "" {
		return "", time.Time{}, fmt.Sprintf("the tenant record's %s attribute is empty", name)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", time.Time{}, fmt.Sprintf("the tenant record's %s attribute is not an RFC3339 timestamp", name)
	}
	return s, t, ""
}

// Ref returns the tenant's key reference.
func (s ErasureScope) Ref() Ref { return s.ref }

// CryptoShreddable reports whether the key can be destroyed by the account. See
// Ref.CryptoShreddable: necessary, not sufficient.
func (s ErasureScope) CryptoShreddable() bool { return s.ref.CryptoShreddable() }

// CoversAllObjects reports whether destroying this key reaches every S3 object the
// tenant has ever written.
//
// True only when the key is destroyable *and* the recorded repoint is no later
// than the tenant's creation, which is the only way to know no object predates the
// key without enumerating objects. False whenever the repoint instant is unknown.
func (s ErasureScope) CoversAllObjects() bool {
	return s.CryptoShreddable() && s.allObjectsUnderKey
}

// CoversDynamoDB reports whether destroying this key reaches the tenant's DynamoDB
// items, their PITR window, and table backups.
//
// Delegates the comparison to CheckDynamoPut so that the erasure claim and the
// write-time refusal can never disagree: if a write under this pair would be
// refused, the pair cannot support a shredding claim either.
func (s ErasureScope) CoversDynamoDB() bool {
	return s.CryptoShreddable() && s.ref.CheckDynamoPut(s.table) == nil
}

// PreFlipBoundary returns the recorded instant this tenant was pointed at its
// current key — the boundary erasure must sort objects against.
//
// Empty means the tenant record does not record one, in which case erasure must
// treat every object as older than the key (see TenantAttrKMSKeyIDSince).
// Meaningful only when CoversAllObjects is false.
func (s ErasureScope) PreFlipBoundary() string { return s.since }

// ProvenanceDefect names a kms_key_id_since that is present but unusable, or "".
//
// Reported rather than swallowed: both cases fail closed, but a malformed stamp is
// a provisioning bug that would otherwise look identical to a tenant that has
// simply never been repointed, and it makes every future erasure for that tenant
// permanently under-claim.
func (s ErasureScope) ProvenanceDefect() string { return s.provenanceDefect }

// Caveat is the sentence §9.3's report quotes. Never empty, and never a claim
// wider than what the fields above establish.
func (s ErasureScope) Caveat() string {
	if !s.CryptoShreddable() {
		// Nothing the tenant record says can widen a key the account cannot
		// destroy, so the reference-level text is already the whole truth.
		return s.ref.ErasureCaveat()
	}

	var b strings.Builder
	b.WriteString("crypto-shredding is available: scheduling this key for deletion renders every object encrypted under it " +
		"unrecoverable, including copies in S3 noncurrent versions and backups that object deletion does not reach (G-021). ")

	switch {
	case s.allObjectsUnderKey:
		// The one assumption left in a completeness claim, stated rather than
		// relied on silently: the inference is sound only because every write
		// asks this resolver what to encrypt under (§Phase 0, property 1).
		b.WriteString("That is all of this tenant's objects: " + TenantAttrKMSKeyIDSince + " is no later than " +
			TenantAttrCreatedAt + ", so no object predates the current key, given that every write resolves its encryption " +
			"through this indirection (§Phase 0). ")
	case s.since != "":
		b.WriteString("It does NOT cover objects written before " + s.since + ", when this tenant was pointed at this key: " +
			"those are encrypted under the key in force at the time and survive this key's destruction. Erasure must delete " +
			"them and wait out the S3 noncurrent-version and backup retention windows, and must not report them as " +
			"unrecoverable. ")
	default:
		b.WriteString("It cannot be claimed for any particular object: the tenant record records no " +
			TenantAttrKMSKeyIDSince + ", so the instant this tenant was pointed at this key is unknown and every object may " +
			"predate it — anything written under a previous key survives this key's destruction. Erasure must treat all " +
			"objects as delete-and-wait-out-retention. ")
		if s.provenanceDefect != "" {
			b.WriteString("Provisioning defect to fix: " + s.provenanceDefect + ". ")
		}
	}

	if s.CoversDynamoDB() {
		b.WriteString("DynamoDB items are covered: the table is encrypted under this same key, so its PITR window and backups " +
			"become unreadable with it. ")
	} else {
		b.WriteString("DynamoDB items are NOT covered: DynamoDB encrypts at table level and this table is encrypted under " +
			s.tableDescription() + ", so destroying this key shreds no items and does not reach PITR or table backups; those " +
			"require item deletion plus waiting out the PITR retention window (§9.3, G-021). ")
	}

	b.WriteString("KMS enforces a pending-deletion window of at least 7 days, during which the deletion can be cancelled and " +
		"the data is still recoverable — erasure is not complete until the key is actually destroyed (§9.3)")
	return b.String()
}

// tableDescription names the table's key for the caveat. A key reference is
// infrastructure metadata rather than content, so it is safe to print (see
// Ref.String).
func (s ErasureScope) tableDescription() string {
	if s.table.IsZero() {
		return "no recorded key"
	}
	return s.table.String()
}

// Resolver reads a tenant's key reference and applies it to a write path.
type Resolver struct {
	repo repository.Repository
	dep  Deployment
}

// New builds a Resolver.
//
// Both Deployment references are required. There is no personal-phase default
// here on purpose: §Phase 0 requires that "a missing threshold must fail the
// deploy, never fall back to a hardcoded default", and a defaulted deployment key
// would make the CMK flip a change to this package rather than to the wiring that
// owns it. Wiring passes S3ServiceDefault and DynamoDBServiceDefault today.
func New(repo repository.Repository, dep Deployment) (*Resolver, error) {
	if repo == nil {
		return nil, fmt.Errorf("kmsref: repository is nil")
	}
	if dep.Bucket.IsZero() {
		return nil, fmt.Errorf("kmsref: deployment bucket key reference is unset: %w", ErrAbsent)
	}
	if dep.Table.IsZero() {
		return nil, fmt.Errorf("kmsref: deployment table key reference is unset: %w", ErrAbsent)
	}
	return &Resolver{repo: repo, dep: dep}, nil
}

// Deployment reports the configured storage keys, for the health and cost
// surfaces that need to state what encryption is actually in force.
func (rs *Resolver) Deployment() Deployment { return rs.dep }

// Resolve returns the tenant's key reference.
//
// Reads the tenant record on every call rather than caching. A cache here would
// be a correctness hazard, not just a staleness one: after a tenant is repointed
// at a new key, a stale entry keeps encrypting new objects under the old key, and
// destroying the new key then shreds everything except the objects written during
// the stale window — the failure is invisible and the objects that survive are
// unidentifiable. At the modelled volume (~45 segments/day, §10.7) a strongly
// consistent Get of one small item is not a cost worth that risk. Where the read
// does become hot, the safe form is to Resolve once per request and pass the Ref
// down — every method that uses one is pure — rather than a TTL cache.
func (rs *Resolver) Resolve(ctx context.Context, tenant keys.TenantID) (Ref, error) {
	ref, _, err := rs.resolve(ctx, tenant)
	return ref, err
}

// resolve is Resolve plus the rest of the tenant record, which ErasureScope needs
// so that a report does not read the same item twice and risk reading it either
// side of a repoint.
func (rs *Resolver) resolve(ctx context.Context, tenant keys.TenantID) (Ref, map[string]any, error) {
	// Via keys.Tenant, so an empty or malformed tenant is refused by the one
	// component that defines what a usable tenant is (I11). There is no path
	// through this package that reads a tenant record without it.
	key, err := keys.Tenant(tenant)
	if err != nil {
		// keys quotes the tenant_id it rejected, which in the personal phase is
		// the user's identity, so the cause travels for errors.Is and its text
		// does not (§9.2).
		return Ref{}, nil, &withheldError{
			msg:   "kmsref: tenant_id is empty or not a valid key identifier, so no key reference can be resolved (I11)",
			cause: err,
		}
	}

	item, err := rs.repo.Get(ctx, key)
	if err != nil {
		// The cause is wrapped for errors.Is/errors.As — that is what
		// distinguishes "no such tenant" from "tenant exists but has no key
		// reference" (different fixes: unprovisioned versus partially
		// provisioned) and a retryable throttle from neither — but its *message*
		// is not interpolated. repository embeds the partition key in every Get
		// error it returns, including ErrNotFound, and that key is the prefix plus
		// the tenant_id — which in the personal phase is the user's email — so
		// "%w" here puts a user identity in a string a handler logs or returns
		// (§9.2). Dropping the text while keeping the type is the same trade
		// logging.ErrorAttr makes for provider errors.
		if errors.Is(err, repository.ErrNotFound) {
			return Ref{}, nil, &withheldError{
				msg:   "kmsref: no tenant record exists, so there is no key reference to resolve (§6.3)",
				cause: err,
			}
		}
		return Ref{}, nil, &withheldError{
			msg:   "kmsref: reading the tenant record failed; the underlying message is withheld because repository errors embed the tenant partition key (§9.2)",
			cause: err,
		}
	}

	// Errors below name the attribute but never the tenant. In the personal
	// phase tenant_id == user_id, and §9.2 keeps PII out of logs and error
	// messages; the caller already knows which tenant it asked about.
	v, ok := item.Attrs[TenantAttrKMSKeyID]
	if !ok {
		return Ref{}, nil, fmt.Errorf("kmsref: tenant record has no %s attribute: %w", TenantAttrKMSKeyID, ErrAbsent)
	}
	if v == nil {
		// A DynamoDB NULL arrives as a nil any, and §6.3 names null first: "it is
		// never null and never absent". Both are the same provisioning defect
		// with the same fix, so both are ErrAbsent — a repair script branching on
		// ErrAbsent to mean "this tenant needs its key reference provisioned"
		// would otherwise skip exactly the tenant §6.3 warns about and report a
		// value-format problem instead.
		return Ref{}, nil, fmt.Errorf("kmsref: tenant record has a null %s attribute: %w", TenantAttrKMSKeyID, ErrAbsent)
	}
	raw, ok := v.(string)
	if !ok {
		return Ref{}, nil, fmt.Errorf("kmsref: %s attribute is %T, not a string: %w", TenantAttrKMSKeyID, v, ErrMalformed)
	}
	ref, err := Parse(raw)
	if err != nil {
		return Ref{}, nil, fmt.Errorf("kmsref: tenant %s attribute: %w", TenantAttrKMSKeyID, err)
	}
	return ref, item.Attrs, nil
}

// withheldError carries a foreign error's identity without its message.
//
// Unwrap keeps errors.Is and errors.As working over the cause — the sentinel, the
// AWS error type, everything a caller branches on — while Error() returns only
// text this package composed. That is the one shape that satisfies §9.2 and the
// sentinel taxonomy at once: the alternatives are wrapping with %w (leaks the
// tenant into every log line that prints the error) or dropping the cause
// (silently makes a throttle indistinguishable from a missing record, so the
// retry decision disappears).
type withheldError struct {
	msg   string
	cause error
}

func (e *withheldError) Error() string { return e.msg }
func (e *withheldError) Unwrap() error { return e.cause }

// ForS3Put returns the encryption parameters for writing one of this tenant's
// objects. This is the call an S3 PutObject or a presigned-PUT signer makes, and
// it is what puts the resolver on the audio and transcript write paths from the
// first write (§Phase 0).
func (rs *Resolver) ForS3Put(ctx context.Context, tenant keys.TenantID) (S3Encryption, error) {
	ref, err := rs.Resolve(ctx, tenant)
	if err != nil {
		return S3Encryption{}, err
	}
	return ref.ForS3Put(rs.dep.Bucket)
}

// CheckDynamoPut refuses a DynamoDB write whose tenant key reference disagrees
// with the table's encryption. See Ref.CheckDynamoPut for why this is a check
// rather than a directive.
func (rs *Resolver) CheckDynamoPut(ctx context.Context, tenant keys.TenantID) error {
	ref, err := rs.Resolve(ctx, tenant)
	if err != nil {
		return err
	}
	return ref.CheckDynamoPut(rs.dep.Table)
}

// ErasureScope returns what §9.3's erasure operation may claim for this tenant.
//
// This is the call the erasure report must make — not Resolve followed by
// Ref.ErasureCaveat, which cannot see the tenant's repoint stamp or the table's
// key and therefore cannot bound the claim in time or by store. One Get serves
// both the reference and the provenance, so the report cannot describe a key from
// one read and a boundary from another.
func (rs *Resolver) ErasureScope(ctx context.Context, tenant keys.TenantID) (ErasureScope, error) {
	ref, attrs, err := rs.resolve(ctx, tenant)
	if err != nil {
		return ErasureScope{}, err
	}
	return newErasureScope(ref, rs.dep.Table, attrs), nil
}
