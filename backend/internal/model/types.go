package model

import "encoding/json"

type CleanupMode string

const (
	CleanupFaithful CleanupMode = "faithful"
	CleanupPolished CleanupMode = "polished"
)

type Settings struct {
	CleanupMode   CleanupMode `json:"cleanup_mode"`
	RetentionDays int         `json:"retention_days"` // 0 = indefinite
}

type NoteIndex struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Aliases       []string `json:"aliases"`
	Snippet       string   `json:"snippet,omitempty"` // first ~500 chars of note for light match
	UpdatedAt     string   `json:"updated_at"`
	S3MarkdownKey string   `json:"s3_markdown_key"`
	S3MetaKey     string   `json:"s3_meta_key"`
}

type CaptureStatus string

const (
	StatusUploaded    CaptureStatus = "uploaded"
	StatusTranscribed CaptureStatus = "transcribed"
	StatusCleaned     CaptureStatus = "cleaned"
	StatusAppended    CaptureStatus = "appended"
	StatusFailed      CaptureStatus = "failed"
)

type CaptureIndex struct {
	ID        string        `json:"id"`
	NoteID    string        `json:"note_id"`
	UserID    string        `json:"user_id"`
	Status    CaptureStatus `json:"status"`
	Mode      CleanupMode   `json:"cleanup_mode"`
	AudioKey  string        `json:"audio_key"`
	RawKey    string        `json:"raw_key"`
	CleanKey  string        `json:"clean_key"`
	Error     string        `json:"error,omitempty"`
	CreatedAt string        `json:"created_at"`
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
