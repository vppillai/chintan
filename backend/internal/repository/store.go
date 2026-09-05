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
	// IncludeSearchText asks a NOTE list to carry NoteIndex.SearchText. Only
	// ListNotes and ListArchivedNotes honour it. It is opt-in because the field
	// is up to 32 KB per note and the notes list is fetched constantly by a
	// client that renders none of it; search and the offline corpus are the two
	// readers that want it.
	IncludeSearchText bool
	// IncludeCleanedBody asks a NOTE list to carry NoteIndex.CleanedBody, the
	// whole-note cleaned view. Opt-in for the same reason as IncludeSearchText,
	// and more so: it is up to 200 KB per note, and the export is the one
	// reader that wants it for every note at once.
	IncludeCleanedBody bool
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

// TenantNote is a note together with the tenant that owns it, for the one
// read that crosses tenants: the expiry sweep.
type TenantNote struct {
	TenantID string
	Note     model.NoteIndex
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

// IdemTTL is how long a COMPLETED idempotency record is honoured.
const IdemTTL = 24 * time.Hour

// IdemClaimLease is how long an idempotency key stays claimed without a
// recorded response before another attempt may take it over.
//
// The handler abandons a claim explicitly when the request 5xxs, so the lease
// is only the backstop for what no defer can reach — the Lambda being killed
// mid-request. The API function's Timeout is 29 s, so no legitimate attempt can
// still be running at 60 s; before this, an unfinished claim kept the full
// 24-hour TTL and one transient 500 answered every retry with "an identical
// request is still in flight" for a day.
const IdemClaimLease = 60 * time.Second

// AppendClaimLease bounds how long one worker may hold an unfinished append
// claim before another attempt is allowed to take it over.
//
// The claim is a mutex and this is how a dead holder releases it. It has to be
// longer than the longest time a LIVE worker can hold the claim, which is the
// Lambda timeout (900 s): a claim taken over while its holder is still writing
// is the one case the marker check in Pipeline.appendToNote cannot cover.
// Twenty minutes clears that with margin.
//
// It is not tied to any retry schedule, and there is no number elsewhere it
// has to stay above or below. Lambda retries a failed asynchronous invocation
// at about one and two minutes — both inside this lease — and that is fine:
// a retry that finds its own token claimed looks for the capture's marker in
// the note body (Pipeline.append). Marker present means the paragraph landed
// and the bookkeeping is finished; marker absent means the invocation fails
// again, and the attempt after the lease takes the claim over and appends
// once. What keeps the append exactly-once is the deterministic token plus the
// marker, not this number.
const AppendClaimLease = 20 * time.Minute

// Store persists per-tenant settings, note indexes, and capture indexes.
//
// Every list method is cursor-paginated: an unpaginated Query silently
// truncates at ~1MB, which loses notes from a list and, worse, leaves orphaned
// audio behind when a cascade delete stops finding captures.
type Store interface {
	GetSettings(ctx context.Context, tenantID string) (model.Settings, error)
	PutSettings(ctx context.Context, tenantID string, s model.Settings) error

	// ListNotes returns active (non-archived) notes, most recently touched
	// first.
	ListNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error)
	// ListArchivedNotes returns archived notes that have not passed their purge
	// deadline. The expiry sweep removes them once it has; the filter keeps
	// them out of the UI in the meantime.
	ListArchivedNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error)
	// ExpiredNotes returns every archived note, in every tenant, whose purge
	// deadline had passed at asOf (Unix seconds). It is the input to the
	// weekly expiry sweep, which unlinks each note's objects and captures and
	// then deletes the note; DynamoDB TTL is only the backstop.
	ExpiredNotes(ctx context.Context, asOf int64) ([]TenantNote, error)
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
	// AbandonIdempotent releases a claimed key whose request did not produce a
	// recordable response, so the caller's retry runs instead of being told the
	// original is still in flight. A completed record is left alone.
	AbandonIdempotent(ctx context.Context, tenantID, key string) error
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

	// Exists reports whether key holds an object, without fetching it. It is
	// what the worker asks about the client-uploaded peaks.json once the
	// pipeline is done: the API records the peaks key when it *issues* the
	// presigned PUT, and only the bucket knows whether the client ever used it.
	Exists(ctx context.Context, key string) (bool, error)

	// MarkProcessed tags the object so the retention lifecycle rule is allowed
	// to expire it, without disturbing whatever tags it already carries — in
	// particular the tenant's retention tier, set once at upload and never
	// touched again. A missing object is not an error: there is nothing left
	// to protect or to expire.
	//
	// This exists because a capture that never gets a chance to run — a lost
	// upload notification, a dead worker — must not have its audio quietly
	// expire while it waits for a chance that never comes. The lifecycle rules
	// in infrastructure/template.yaml require this tag in addition to the
	// retention tag, so an object nobody has called this on is kept
	// indefinitely regardless of age.
	MarkProcessed(ctx context.Context, key string) error
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
