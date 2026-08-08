package repository

import (
	"context"
	"errors"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

var (
	// ErrNotFound is returned when an addressed item or object does not exist.
	ErrNotFound = errors.New("repository: not found")

	// ErrVersionConflict is returned when a conditional write loses to a
	// concurrent writer. The caller must re-read and reconcile.
	ErrVersionConflict = errors.New("repository: version conflict")

	// ErrPreconditionFailed is returned by PutIfMatch when the stored ETag is
	// not the one the caller read.
	ErrPreconditionFailed = errors.New("repository: precondition failed")

	// ErrIdempotencyInFlight is returned by BeginIdempotent when another attempt
	// holds the key and has not finished.
	ErrIdempotencyInFlight = errors.New("repository: idempotent request in flight")

	// ErrIdempotencyKeyReused is returned when a key is replayed with a
	// different request body. Replaying someone else's response would be worse
	// than failing.
	ErrIdempotencyKeyReused = errors.New("repository: idempotency key reused with a different request")
)

// Pagination bounds. A caller asking for nothing gets DefaultListLimit; a
// caller asking for the world gets MaxListLimit.
const (
	DefaultListLimit int32 = 50
	MaxListLimit     int32 = 200
)

// ListOptions bounds a list query. Cursor is opaque: it is the base64 encoding
// of the DynamoDB LastEvaluatedKey and is only valid for the tenant and query
// that produced it.
type ListOptions struct {
	Limit  int32  // 0 => DefaultListLimit; clamped to MaxListLimit
	Cursor string // opaque, base64 of LastEvaluatedKey
}

func (o ListOptions) limit() int32 {
	switch {
	case o.Limit <= 0:
		return DefaultListLimit
	case o.Limit > MaxListLimit:
		return MaxListLimit
	default:
		return o.Limit
	}
}

// Page is one page of a list query. Cursor is empty when the query is
// exhausted.
type Page[T any] struct {
	Items  []T    `json:"items"`
	Cursor string `json:"cursor,omitempty"`
}

// IdemRecord is the stored result of a completed idempotent request.
type IdemRecord struct {
	Key         string
	TenantID    string
	Fingerprint string
	Status      int
	Response    []byte
	Done        bool
	ExpiresAt   int64
}

// IdemTTL is how long an idempotency record is honoured.
const IdemTTL = 24 * time.Hour

// AppendClaimLease bounds how long one worker may hold an unfinished append
// claim before another attempt is allowed to take it over.
//
// It MUST stay strictly longer than CaptureQueue.VisibilityTimeout in
// infrastructure/template.yaml:330, currently 960 seconds. SQS returns an
// unfinished message at exactly that point; if the claim taken at T0 has
// already expired by T0+960s, the redelivery takes the claim over and appends
// the same paragraph again — three times over, at maxReceiveCount 3. Twenty
// minutes leaves four minutes of margin over the 960 s timeout while staying
// well inside Lambda's own 900 s ceiling on the work it protects.
//
// The inequality is checked by TestAppendClaimLeaseOutlastsTheQueueVisibilityTimeout
// in internal/pipeline, which restates the template's number. Go cannot read
// the template, so that test is the only thing tying the two together — but it
// is not what makes the append safe. Pipeline.append recognises its own
// interrupted attempt from the deterministic claim token and the note body, so
// a lease that is nonetheless too short costs a redelivery, not a duplicate
// paragraph.
const AppendClaimLease = 20 * time.Minute

// Store persists per-tenant settings, note indexes, and capture indexes.
//
// Every list method is cursor-paginated: an unpaginated Query silently
// truncates at ~1MB, which loses notes from a list and, worse, leaves orphaned
// audio behind when a cascade delete stops finding captures.
type Store interface {
	GetSettings(ctx context.Context, tenantID string) (model.Settings, error)
	PutSettings(ctx context.Context, tenantID string, s model.Settings) error

	// ListNotes returns active (non-archived) notes.
	ListNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error)
	// ListArchivedNotes returns archived notes that have not passed their purge
	// deadline. DynamoDB TTL removes them eventually; the filter keeps them out
	// of the UI in the meantime.
	ListArchivedNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error)
	GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error)
	// PutNote writes n conditionally on n.Version matching the stored version,
	// and returns the note with its new version. A losing write returns
	// ErrVersionConflict rather than silently discarding the other writer.
	PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error)
	DeleteNote(ctx context.Context, tenantID, noteID string) error

	PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error)
	GetCapture(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error)
	// ListCaptures returns every capture the tenant owns, newest first, from the
	// base table.
	//
	// Base table, not GSI1, and that is the whole point: a capture awaiting
	// disambiguation has no destination note, so it has no note partition to be
	// indexed under. Reaching the tenant's captures by walking their notes
	// therefore cannot see exactly the captures the user most needs to find.
	ListCaptures(ctx context.Context, tenantID string, opts ListOptions) (Page[model.CaptureIndex], error)
	// ListCapturesByNote queries GSI1 directly instead of scanning the tenant's
	// whole capture partition and filtering client-side.
	ListCapturesByNote(ctx context.Context, tenantID, noteID string, opts ListOptions) (Page[model.CaptureIndex], error)
	UpdateCaptureStatus(ctx context.Context, tenantID, captureID string, status model.CaptureStatus, errMsg string) error
	DeleteCapture(ctx context.Context, tenantID, captureID string) error

	// ClaimCaptureAppend conditionally records token as the owner of this
	// capture's single append. It returns claimed=true when the caller owns the
	// append and must perform it, and claimed=false with the current capture
	// when somebody else (including an earlier attempt of the same caller) owns
	// it. Inspect CaptureIndex.AppendedAt to tell "already done" from
	// "in progress".
	ClaimCaptureAppend(ctx context.Context, tenantID, captureID, token string) (claimed bool, c model.CaptureIndex, err error)
	// CompleteCaptureAppend flips status to appended and stamps AppendedAt, but
	// only for the holder of token.
	CompleteCaptureAppend(ctx context.Context, tenantID, captureID, token string) (model.CaptureIndex, error)

	// BeginIdempotent conditionally claims key for this tenant. It returns
	// (nil, nil) when the caller owns the key and should perform the work,
	// (record, nil) when a completed record exists and should be replayed, and
	// (nil, ErrIdempotencyInFlight) when another attempt holds it. Replaying a
	// key with a different fingerprint returns ErrIdempotencyKeyReused.
	BeginIdempotent(ctx context.Context, tenantID, key, fingerprint string) (*IdemRecord, error)
	CompleteIdempotent(ctx context.Context, tenantID, key string, status int, response []byte) error

	PutWebAuthnChallenge(ctx context.Context, c model.WebAuthnChallenge) error
	GetWebAuthnChallenge(ctx context.Context, challengeID string) (model.WebAuthnChallenge, error)
	DeleteWebAuthnChallenge(ctx context.Context, challengeID string) error

	PutWebAuthnCredential(ctx context.Context, c model.WebAuthnCredential) error
	GetWebAuthnCredential(ctx context.Context, credentialID string) (model.WebAuthnCredential, error)
	ListWebAuthnCredentials(ctx context.Context, opts ListOptions) (Page[model.WebAuthnCredential], error)
	ListWebAuthnCredentialsByUser(ctx context.Context, tenantID string, opts ListOptions) (Page[model.WebAuthnCredential], error)
	DeleteAllWebAuthnCredentials(ctx context.Context, tenantID string) error

	PutRefreshVault(ctx context.Context, v model.RefreshVault) error
	GetRefreshVault(ctx context.Context, tenantID string) (model.RefreshVault, error)
	DeleteRefreshVault(ctx context.Context, tenantID string) error
}

// Objects stores blob content addressed by S3-style keys.
type Objects interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (url string, err error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (url string, err error)

	// GetWithETag returns the body and the entity tag that a subsequent
	// PutIfMatch must present. A missing object returns ErrNotFound.
	GetWithETag(ctx context.Context, key string) (body []byte, etag string, err error)
	// PutIfMatch writes body only if the stored object still carries etag. An
	// empty etag means "the object must not exist" (If-None-Match: *). A lost
	// race returns ErrPreconditionFailed.
	PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error
}

// DrainPages walks every page of a paginated list, up to maxItems. It exists
// for the few callers that genuinely need the whole set — a cascade delete
// must see every capture or it orphans audio.
func DrainPages[T any](ctx context.Context, maxItems int, next func(context.Context, ListOptions) (Page[T], error)) ([]T, error) {
	var out []T
	opts := ListOptions{Limit: MaxListLimit}
	for {
		page, err := next(ctx, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.Cursor == "" || (maxItems > 0 && len(out) >= maxItems) {
			break
		}
		opts.Cursor = page.Cursor
	}
	if maxItems > 0 && len(out) > maxItems {
		out = out[:maxItems]
	}
	return out, nil
}

// DefaultSettings is the settings record a tenant has before saving any.
func DefaultSettings() model.Settings {
	return model.Settings{
		CleanupMode:   model.CleanupFaithful,
		RetentionDays: 0,
	}
}
