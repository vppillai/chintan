package model

import (
	"encoding/json"
	"time"
)

// TimeLayout stores timestamps with fixed-width fractional seconds.
//
// time.RFC3339Nano trims trailing zeros from the fraction, so "…:00Z" sorts
// above "…:00.1Z" ('Z' > '.') and lexicographic order stops being chronological
// order. Every timestamp written by the backend uses this layout instead, so a
// plain string comparison is a valid ordering.
const TimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTime renders t in UTC using TimeLayout.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// Now is FormatTime(time.Now()).
func Now() string {
	return FormatTime(time.Now())
}

// ParseTime parses a timestamp written by FormatTime, and also the RFC3339 and
// RFC3339Nano values written by earlier versions.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

type CleanupMode string

const (
	CleanupFaithful CleanupMode = "faithful"
	CleanupPolished CleanupMode = "polished"
)

// Theme is the rendering palette the client asks for. It is stored server-side
// so a second device opens in the theme the first one chose.
type Theme string

const (
	ThemeInk      Theme = "ink"
	ThemeNocturne Theme = "nocturne"
	ThemeSystem   Theme = "system"
)

// MaxRetentionDays bounds the audio retention setting. Beyond ten years the
// value is not a policy, it is a typo.
const MaxRetentionDays = 3650

type Settings struct {
	CleanupMode   CleanupMode `json:"cleanup_mode"`
	RetentionDays int         `json:"retention_days"` // 0 = indefinite
	// Theme is empty on records written before v2; readers substitute ThemeInk.
	Theme Theme `json:"theme,omitempty"`
	// DailySpendCapMicros is the tenant's own provider budget. 0 means "use the
	// instance default", which is itself 0 — metering without enforcement.
	DailySpendCapMicros int64 `json:"daily_spend_cap_micros,omitempty"`
}

type NoteIndex struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Aliases       []string `json:"aliases"`
	Tags          []string `json:"tags,omitempty"`
	Snippet       string   `json:"snippet,omitempty"` // first ~500 runes of note for light match
	CreatedAt     string   `json:"created_at,omitempty"`
	UpdatedAt     string   `json:"updated_at"`
	S3MarkdownKey string   `json:"s3_markdown_key"`
	S3MetaKey     string   `json:"s3_meta_key"`
	DeletedAt     string   `json:"deleted_at,omitempty"`
	PurgeAfter    string   `json:"purge_after,omitempty"`
	// Verbatim bypasses cleanup for this note entirely. Dictated content that
	// must not be reworded — a spec, a quote, a prompt — is otherwise silently
	// rewritten by polished mode.
	Verbatim bool `json:"verbatim,omitempty"`
	// PurgeAfterEpoch is the same instant as PurgeAfter as a Unix second count.
	// It is the DynamoDB TTL attribute, so expiry is performed by the table
	// rather than by a synchronous sweep on the read path.
	PurgeAfterEpoch int64 `json:"purge_after_epoch,omitempty"`
	// Version is the optimistic-concurrency counter. A write carries the version
	// it read; the store rejects it if the stored version has moved on.
	Version int64 `json:"version"`
}

// CaptureStatus is where a capture sits in the pipeline. It is a string type, so
// promoting a constant from another package into this one changes no stored
// value and no wire representation.
type CaptureStatus string

const (
	StatusUploaded    CaptureStatus = "uploaded"
	StatusTranscribed CaptureStatus = "transcribed"
	StatusCleaned     CaptureStatus = "cleaned"
	StatusAppended    CaptureStatus = "appended"
	StatusFailed      CaptureStatus = "failed"
	// StatusNeedsTarget means the transcript was understood but the destination
	// note is uncertain, so the user has to confirm before anything is written.
	StatusNeedsTarget CaptureStatus = "needs_target"
	// StatusNoContent means the recording was nothing but an instruction to the app,
	// such as "create a note called test123", so there was no dictation to write.
	StatusNoContent CaptureStatus = "no_content"

	// The five below arrived with the asynchronous pipeline and lived in
	// internal/service until the API surface landed. They are the in-progress
	// stages the frontend's progress card polls, plus the distinct outcome a
	// spend cap produces.

	// StatusTranscribing means the recording is with the speech provider.
	StatusTranscribing CaptureStatus = "transcribing"
	// StatusRouting means the destination note is being decided.
	StatusRouting CaptureStatus = "routing"
	// StatusCleaning means the transcript is with the cleanup model.
	StatusCleaning CaptureStatus = "cleaning"
	// StatusAppending means the append claim is held and the text is going into
	// the note body.
	StatusAppending CaptureStatus = "appending"
	// StatusSpendCapped means the tenant's daily provider spend cap stopped the
	// call. It is deliberately distinct from failed so the UI can explain a
	// budget decision rather than report a fault.
	StatusSpendCapped CaptureStatus = "spend_capped"
)

type CaptureIndex struct {
	ID        string        `json:"id"`
	NoteID    string        `json:"note_id"`
	UserID    string        `json:"user_id"`
	Status    CaptureStatus `json:"status"`
	Mode      CleanupMode   `json:"cleanup_mode"`
	AudioKey  string        `json:"audio_key"`
	RawKey    string        `json:"raw_key"`
	RoutedKey string        `json:"routed_key,omitempty"`
	CleanKey  string        `json:"clean_key"`
	Error     string        `json:"error,omitempty"`
	CreatedAt string        `json:"created_at"`

	// Routing suggestion, set when the destination could not be decided confidently.
	SuggestedNoteID string  `json:"suggested_note_id,omitempty"`
	SuggestedTitle  string  `json:"suggested_title,omitempty"`
	RouteConfidence float64 `json:"route_confidence,omitempty"`

	// Version is the optimistic-concurrency counter.
	Version int64 `json:"version"`
	// AppendToken is claimed before the capture's text is written into the note
	// and is what makes the append idempotent: a retry that finds its own token
	// already recorded must not append again.
	AppendToken string `json:"append_token,omitempty"`
	// AppendClaimedAt is when AppendToken was claimed. A claim older than
	// AppendClaimLease is assumed abandoned so a dead worker cannot strand the
	// capture forever.
	AppendClaimedAt int64 `json:"append_claimed_at,omitempty"`
	// AppendedAt is set only once the text is durably in the note body.
	AppendedAt int64 `json:"appended_at,omitempty"`

	DurationMS  int64  `json:"duration_ms,omitempty"`
	SegmentsKey string `json:"segments_key,omitempty"`
	PeaksKey    string `json:"peaks_key,omitempty"`
}

// WebAuthnChallenge is an in-flight ceremony (go-webauthn SessionData JSON).
// Stored under global pk/sk WACHAL#<challenge_id> so login can start without knowing sub.
type WebAuthnChallenge struct {
	ChallengeID string `json:"challenge_id"`
	SessionData string `json:"session_data"`
	UserID      string `json:"user_id,omitempty"` // set for registration ceremonies
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// WebAuthnCredential is one enrolled platform authenticator for a Cognito user.
type WebAuthnCredential struct {
	UserID       string `json:"user_id"`
	CredentialID string `json:"credential_id"` // base64url
	Credential   string `json:"credential"`    // JSON webauthn.Credential
	SignCount    uint32 `json:"sign_count"`
	CreatedAt    int64  `json:"created_at"`
}

// RefreshVault holds a KMS-encrypted Cognito refresh token for biometric login.
type RefreshVault struct {
	UserID     string `json:"user_id"`
	Ciphertext []byte `json:"ciphertext"`
	UpdatedAt  int64  `json:"updated_at"`
}

// WebAuthnOptionsResponse is returned by register/login *options* endpoints.
type WebAuthnOptionsResponse struct {
	ChallengeID string          `json:"challenge_id"`
	Options     json.RawMessage `json:"options"`
}

// WebAuthnVerifyRequest is the JSON body for register/login verify.
type WebAuthnVerifyRequest struct {
	ChallengeID  string          `json:"challenge_id"`
	Credential   json.RawMessage `json:"credential"`
	RefreshToken string          `json:"refresh_token,omitempty"` // register only
}

// CognitoTokenSet is returned to the SPA after biometric login (same shape as Hosted UI).
type CognitoTokenSet struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int32  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}
