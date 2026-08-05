// Package keys is the only place in this system that constructs a DynamoDB key
// or an S3 object key.
//
// I11: "Every stored record and every query is tenant-scoped. No read or write
// path exists that is not qualified by tenant_id, including admin and migration
// scripts." Cross-tenant leakage is the one bug a multi-tenant product cannot
// survive, and the way it happens is not a deliberate unscoped query — it is one
// key built by hand, somewhere, months later.
//
// So tenancy is enforced structurally rather than by review:
//
//   - Every constructor takes a TenantID as its first argument and returns an
//     error if it is invalid. There is no way to build a key without one.
//   - TenantID is a named type, not a string, so an argument cannot be
//     transposed silently.
//   - scripts/checks/check-tenant-keys.sh fails the build if any key prefix
//     literal ("TENANT#", "CAPTURE#", "tenants/", ...) appears outside this
//     package. That check is what makes this package's monopoly real; without
//     it, this file is a convention.
//
// During the personal phase tenant_id == user_id, but nothing here assumes it.
// The point of building this while there is one tenant is that the unsafe path
// never gets written (§2A.1).
package keys

import (
	"fmt"
	"regexp"
	"strings"
)

// TenantID identifies a tenant. It is a distinct type so that a bare string —
// a user ID, a capture ID, a value read from a request body — cannot be passed
// where a tenant is expected. §6.6 requires that tenant_id comes from a
// validated JWT claim only, and a named type makes an accidental substitution a
// compile error rather than a data leak.
type TenantID string

// DynamoKey is a composite primary key for the single table (§6.3).
type DynamoKey struct {
	PK string
	SK string
}

// GSI1 attribute names for the sparse time-ordered index (§6.3). Only Capture
// and Thread records carry them: Segment, Usage, Audit, and Metric are
// high-volume and projecting them would make the index a second copy of the
// table.
const (
	GSI1PKAttr = "GSI1PK"
	GSI1SKAttr = "GSI1SK"
)

// Key prefixes. Every literal that participates in a key lives here and nowhere
// else, which is what check-tenant-keys.sh enforces.
const (
	tenantPrefix   = "TENANT#"
	userPrefix     = "USER#"
	capturePrefix  = "CAPTURE#"
	itemPrefix     = "ITEM#"
	threadPrefix   = "THREAD#"
	segmentInfix   = "#SEG#"
	sessionInfix   = "#SESSION#"
	ingestPrefix   = "INGEST#"
	rulePrefix     = "RULE#"
	usagePrefix    = "USAGE#"
	auditPrefix    = "AUDIT#"
	metricPrefix   = "METRIC#"
	idemPrefix     = "IDEM#"
	telegramPrefix = "TG#"
	updatedPrefix  = "UPDATED#"

	metaSK = "META"
	linkSK = "LINK"

	// s3TenantRoot is the per-tenant S3 prefix (§6.2). IAM policy conditions
	// restrict access by this prefix, so its shape is a security boundary and
	// not merely an organising convention (§9.1).
	s3TenantRoot = "tenants/"
)

// describeRejected characterises a rejected value without reproducing it.
//
// **A validation error must describe the value, never quote it.** §9.2 forbids transcript
// content, audio, or PII from appearing "in logs, error messages, exception traces, or
// third-party monitoring" — and note that "error messages" is on that list, because a
// caller logs an error, wraps it, or returns it in an HTTP body.
//
// This package is the reason that matters here rather than somewhere less central: every
// stored record's key comes through these validators (I11), so a handler that passes a
// transcript body, an email, or a search query where an identifier belongs gets that value
// back inside an error. The audit package hardened its own messages for exactly this reason
// and the leak survived one call frame below, in here — found by an adversarial review that
// probed with a 15KB transcript body as a tenant id.
//
// Length and the first offending character class are diagnostic enough to fix a call site.
// The bytes are not.
func describeRejected(v string) string {
	if len(v) > 64 {
		// Deliberately no sample of the content, not even a prefix: the first 64 bytes of a
		// transcript are still a transcript.
		return fmt.Sprintf("length %d, too long for an identifier", len(v))
	}
	for _, r := range v {
		switch {
		case r == '#':
			return fmt.Sprintf("length %d, contains the key delimiter '#'", len(v))
		case r == '/':
			return fmt.Sprintf("length %d, contains a path separator '/'", len(v))
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			return fmt.Sprintf("length %d, contains whitespace", len(v))
		case r > 127:
			return fmt.Sprintf("length %d, contains a non-ASCII character", len(v))
		}
	}
	return fmt.Sprintf("length %d", len(v))
}

// identRe is the accepted shape for every identifier that reaches a key.
//
// The restriction is deliberate and tighter than DynamoDB requires. A key
// segment containing "#" would let a caller forge a different entity's key by
// smuggling a delimiter through an ID — an ID of "x#SESSION#y" would otherwise
// produce a key indistinguishable from a legitimate session record. Rejecting
// the delimiter outright is cheaper than escaping it, and no legitimate
// identifier in this system needs it: IDs are ULIDs, emails, hex hashes, or
// short slugs.
var identRe = regexp.MustCompile(`^[A-Za-z0-9._@:+=-]{1,256}$`)

// validateTenant rejects an unusable tenant before it can produce a key.
//
// Fails closed: an empty tenant is an error, never a key that would read across
// the whole table. This is the check that makes "no unscoped path exists"
// enforceable at runtime as well as statically.
func validateTenant(t TenantID) error {
	if strings.TrimSpace(string(t)) == "" {
		return fmt.Errorf("keys: tenant_id is empty; every key must be tenant-scoped (I11)")
	}
	if !identRe.MatchString(string(t)) {
		return fmt.Errorf("keys: tenant_id is not a valid identifier (%s)", describeRejected(string(t)))
	}
	return nil
}

// validateIdent rejects an unusable identifier, naming the field so the error
// is actionable rather than a bare "invalid input".
func validateIdent(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("keys: %s is empty", field)
	}
	if !identRe.MatchString(v) {
		return fmt.Errorf("keys: %s contains characters not permitted in a key segment (%s)", field, describeRejected(v))
	}
	return nil
}

// pk builds the tenant partition key. Unexported: a caller wanting a partition
// key wants one of the entity constructors below.
func pk(t TenantID) string { return tenantPrefix + string(t) }

// ---------------------------------------------------------------------------
// DynamoDB keys (§6.3)
// ---------------------------------------------------------------------------

// Tenant returns the key for the tenant metadata record, which carries plan,
// region, kms_key_id, and consent state.
func Tenant(t TenantID) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: metaSK}, nil
}

// User returns the key for one user within a tenant.
func User(t TenantID, userID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("user_id", userID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: userPrefix + userID}, nil
}

// Capture returns the key for one capture — the transcript of one session.
func Capture(t TenantID, captureID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("capture_id", captureID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: capturePrefix + captureID}, nil
}

// Item returns the key for one extracted item.
func Item(t TenantID, itemID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("item_id", itemID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: itemPrefix + itemID}, nil
}

// Thread returns the key for one thread — the main working surface (§1.3).
func Thread(t TenantID, threadID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("thread_id", threadID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: threadPrefix + threadID}, nil
}

// Segment returns the key for one audio segment of a capture.
//
// seq is zero-padded to six digits so that lexicographic sort order — the only
// order DynamoDB range keys have — matches numeric order. Without the padding,
// segment 10 sorts before segment 2 and the segment map reassembles a
// transcript in the wrong order, which reads as a transcription fault rather
// than a key-format one.
func Segment(t TenantID, captureID string, seq int) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("capture_id", captureID); err != nil {
		return DynamoKey{}, err
	}
	if seq < 0 {
		return DynamoKey{}, fmt.Errorf("keys: segment seq %d is negative", seq)
	}
	// Six digits covers 999,999 segments — at a 28s target window that is
	// roughly nine months of continuous speech in one capture, so the ceiling
	// is unreachable rather than merely generous. Refuse past it instead of
	// emitting a key that would sort wrongly.
	if seq > 999999 {
		return DynamoKey{}, fmt.Errorf("keys: segment seq %d exceeds the six-digit sortable range", seq)
	}
	return DynamoKey{
		PK: pk(t),
		SK: fmt.Sprintf("%s%s%s%06d", capturePrefix, captureID, segmentInfix, seq),
	}, nil
}

// Session returns the key for the session record of a capture — how the audio
// arrived, as distinct from what was said (§1.3).
func Session(t TenantID, captureID, sessionID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("capture_id", captureID); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("session_id", sessionID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{
		PK: pk(t),
		SK: capturePrefix + captureID + sessionInfix + sessionID,
	}, nil
}

// Ingest returns the key for the ingestion record of one content hash, which is
// what makes re-import idempotent (§5A.3.4).
func Ingest(t TenantID, contentHash string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("content_hash", contentHash); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: ingestPrefix + contentHash}, nil
}

// Rule returns the key for one correction rule, keyed on its phonetic class
// rather than a literal string (§Phase 4).
func Rule(t TenantID, phoneticKey string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("phonetic_key", phoneticKey); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: rulePrefix + phoneticKey}, nil
}

// Usage returns the key for one metering record (I12).
//
// month is "yyyy-mm"; the month partitioning is what lets a monthly cost report
// and the daily spend breaker (§10.5.9) read a bounded range rather than scan.
func Usage(t TenantID, month, unit, id string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateMonth(month); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("unit", unit); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("id", id); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{
		PK: pk(t),
		SK: usagePrefix + month + "#" + unit + "#" + id,
	}, nil
}

// UsageMonthPrefix returns the SK prefix for every usage record in one month,
// for the cost report and the spend breaker.
func UsageMonthPrefix(t TenantID, month string) (string, string, error) {
	if err := validateTenant(t); err != nil {
		return "", "", err
	}
	if err := validateMonth(month); err != nil {
		return "", "", err
	}
	return pk(t), usagePrefix + month + "#", nil
}

// Audit returns the key for one audit record (I13). Write-once: no update or
// delete path exists in application code.
func Audit(t TenantID, id string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("id", id); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: auditPrefix + id}, nil
}

// Metric returns the key for one computed metric value (§11A).
//
// date is "yyyy-mm-dd".
func Metric(t TenantID, date, metricID string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateDate(date); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("metric_id", metricID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: metricPrefix + date + "#" + metricID}, nil
}

// Idempotency returns the key for an idempotency record (§2A.1). Backed by a
// short-TTL item, so a client retry cannot create a second capture.
func Idempotency(t TenantID, idempotencyKey string) (DynamoKey, error) {
	if err := validateTenant(t); err != nil {
		return DynamoKey{}, err
	}
	if err := validateIdent("idempotency_key", idempotencyKey); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: pk(t), SK: idemPrefix + idempotencyKey}, nil
}

// TelegramLink returns the key for the Telegram-sender-to-tenant mapping.
//
// This is the single record in the table whose partition key is not
// TENANT#{tenant_id}, and the exception is inherent rather than an oversight: it
// is the lookup that *resolves* a tenant from an inbound Telegram user ID
// (§6.3), so it cannot be qualified by the value it exists to discover. It
// holds no user content — only a tenant and user reference — so it is not a
// path to tenant data, it is the gate in front of one. §6.6's webhook rules
// require this record to resolve *before* any storage is touched, and an
// unmapped sender is rejected with a message that does not confirm the app
// exists.
//
// verify.sh asserts no other record type breaks the tenant-prefix rule (§11.6).
func TelegramLink(telegramUserID string) (DynamoKey, error) {
	if err := validateIdent("telegram_user_id", telegramUserID); err != nil {
		return DynamoKey{}, err
	}
	return DynamoKey{PK: telegramPrefix + telegramUserID, SK: linkSK}, nil
}

// ---------------------------------------------------------------------------
// GSI1 — time-ordered listing (§6.3)
// ---------------------------------------------------------------------------

// GSI1 returns the sparse index attributes for a record that participates in
// time-ordered listing. Only Capture and Thread records may carry these.
//
// updatedAt must be an RFC3339 timestamp: the index sorts lexicographically, and
// RFC3339 in UTC is the format where lexicographic and chronological order
// coincide. A local-offset timestamp sorts wrongly against a UTC one, which
// would silently mis-order the capture list.
func GSI1(t TenantID, updatedAt string) (pkVal string, skVal string, err error) {
	if err := validateTenant(t); err != nil {
		return "", "", err
	}
	if err := validateRFC3339UTC("updated_at", updatedAt); err != nil {
		return "", "", err
	}
	return pk(t), updatedPrefix + updatedAt, nil
}

// ---------------------------------------------------------------------------
// S3 object keys (§6.2)
// ---------------------------------------------------------------------------

// S3TenantPrefix returns the per-tenant S3 prefix. IAM conditions are written
// against this shape (§9.1), so it is a security boundary.
func S3TenantPrefix(t TenantID) (string, error) {
	if err := validateTenant(t); err != nil {
		return "", err
	}
	return s3TenantRoot + string(t) + "/", nil
}

// S3AudioSegment returns the key for one VAD-gated speech segment — the primary
// audio artifact.
func S3AudioSegment(t TenantID, captureID, segmentID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "audio")
	if err != nil {
		return "", err
	}
	if err := validateIdent("segment_id", segmentID); err != nil {
		return "", err
	}
	return prefix + "segments/" + segmentID + ".opus", nil
}

// S3AudioContinuous returns the key for the continuous safety copy (I2).
//
// Deleted as soon as every segment of its session transcribes successfully
// (§10.5.8); the 7-day lifecycle rule is only the backstop for a worker that
// died silently.
func S3AudioContinuous(t TenantID, captureID, sessionID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "audio")
	if err != nil {
		return "", err
	}
	if err := validateIdent("session_id", sessionID); err != nil {
		return "", err
	}
	return prefix + "continuous/" + sessionID + ".opus", nil
}

// S3CaptureContent returns the key for L2 — the current user-facing document
// and the source of truth (§6.1).
func S3CaptureContent(t TenantID, captureID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "captures")
	if err != nil {
		return "", err
	}
	return prefix + "content.md", nil
}

// S3CaptureAlignment returns the key for the block-ID-to-audio-position sidecar
// (§6.5). Keyed by block ID rather than character offset because offsets break
// on every edit and block IDs survive them.
func S3CaptureAlignment(t TenantID, captureID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "captures")
	if err != nil {
		return "", err
	}
	return prefix + "alignment.json", nil
}

// S3TranscriptL0 returns the key for one raw STT output object.
//
// The runID dimension is mandatory from the first write even though Phase 1
// produces exactly one run: a capture accumulates further L0 sets over its life
// — shadow mode (§7.2) and retranscribe.sh (§11.4) both add one — and a path
// without runID would make the second transcription of any capture overwrite
// the first, which is an I1 violation (§6.1).
func S3TranscriptL0(t TenantID, captureID, runID, segmentID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "captures")
	if err != nil {
		return "", err
	}
	if err := validateIdent("run_id", runID); err != nil {
		return "", err
	}
	if err := validateIdent("segment_id", segmentID); err != nil {
		return "", err
	}
	return prefix + "transcripts/L0/" + runID + "/" + segmentID + ".json", nil
}

// S3TranscriptL0RunPrefix returns the prefix for every object in one L0 run,
// used by the immutability proof in verify.sh (§11.6).
func S3TranscriptL0RunPrefix(t TenantID, captureID, runID string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "captures")
	if err != nil {
		return "", err
	}
	if err := validateIdent("run_id", runID); err != nil {
		return "", err
	}
	return prefix + "transcripts/L0/" + runID + "/", nil
}

// S3TranscriptL1 returns the key for one post-cleanup transcript version.
// Regenerable from the active L0 run plus the current rule set (§6.1).
func S3TranscriptL1(t TenantID, captureID, version string) (string, error) {
	prefix, err := s3CapturePrefix(t, captureID, "captures")
	if err != nil {
		return "", err
	}
	if err := validateIdent("version", version); err != nil {
		return "", err
	}
	return prefix + "transcripts/L1/" + version + ".json", nil
}

// S3ItemText returns the key for an oversized item body (§3A.4).
//
// Used only when a body would push the DynamoDB record past the 400KB item
// ceiling. A long verbatim prompt is the only realistic case, and it is exactly
// the content type that must not be truncated (§3A.3), so the overflow path is
// required rather than defensive.
func S3ItemText(t TenantID, itemID string) (string, error) {
	tenantPrefix, err := S3TenantPrefix(t)
	if err != nil {
		return "", err
	}
	if err := validateIdent("item_id", itemID); err != nil {
		return "", err
	}
	return tenantPrefix + "items/" + itemID + ".txt", nil
}

// S3EmbeddingsMatrix returns the key for the packed float32 embedding matrix
// (I7 — no managed vector database).
func S3EmbeddingsMatrix(t TenantID) (string, error) {
	prefix, err := S3TenantPrefix(t)
	if err != nil {
		return "", err
	}
	return prefix + "index/embeddings.f32", nil
}

// S3EmbeddingsMeta returns the key for the row-to-source sidecar that makes the
// packed matrix interpretable.
func S3EmbeddingsMeta(t TenantID) (string, error) {
	prefix, err := S3TenantPrefix(t)
	if err != nil {
		return "", err
	}
	return prefix + "index/embeddings.meta.json", nil
}

// s3CapturePrefix builds the per-capture prefix under a top-level area
// ("audio" or "captures"), which is the shape §6.2 lays out.
func s3CapturePrefix(t TenantID, captureID, area string) (string, error) {
	tenantPrefix, err := S3TenantPrefix(t)
	if err != nil {
		return "", err
	}
	if err := validateIdent("capture_id", captureID); err != nil {
		return "", err
	}
	switch area {
	case "audio", "captures":
	default:
		return "", fmt.Errorf("keys: unknown S3 area %q", area)
	}
	return tenantPrefix + area + "/" + captureID + "/", nil
}

// ---------------------------------------------------------------------------
// Format validation
// ---------------------------------------------------------------------------

var (
	monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	dateRe  = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$`)
	// RFC3339 restricted to a Z suffix. A key that sorts must be in one zone,
	// and UTC is the only zone every producer here can agree on.
	rfc3339UTCRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)
)

func validateMonth(m string) error {
	if !monthRe.MatchString(m) {
		return fmt.Errorf("keys: month %q is not yyyy-mm", m)
	}
	return nil
}

func validateDate(d string) error {
	if !dateRe.MatchString(d) {
		return fmt.Errorf("keys: date %q is not yyyy-mm-dd", d)
	}
	return nil
}

func validateRFC3339UTC(field, v string) error {
	if !rfc3339UTCRe.MatchString(v) {
		return fmt.Errorf("keys: %s %q must be an RFC3339 timestamp in UTC (Z suffix), "+
			"because GSI1 sorts lexicographically and a local offset would mis-order it", field, v)
	}
	return nil
}
