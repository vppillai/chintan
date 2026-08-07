package repository

import (
	"context"
	"errors"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

var ErrNotFound = errors.New("repository: not found")

// Store persists per-user settings, note indexes, and capture indexes.
type Store interface {
	GetSettings(ctx context.Context, userID string) (model.Settings, error)
	PutSettings(ctx context.Context, userID string, s model.Settings) error

	ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error)
	GetNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error)
	PutNote(ctx context.Context, userID string, n model.NoteIndex) error
	DeleteNote(ctx context.Context, userID, noteID string) error

	PutCapture(ctx context.Context, c model.CaptureIndex) error
	GetCapture(ctx context.Context, userID, captureID string) (model.CaptureIndex, error)
	ListCapturesByNote(ctx context.Context, userID, noteID string) ([]model.CaptureIndex, error)
	UpdateCaptureStatus(ctx context.Context, userID, captureID string, status model.CaptureStatus, errMsg string) error

	PutWebAuthnChallenge(ctx context.Context, c model.WebAuthnChallenge) error
	GetWebAuthnChallenge(ctx context.Context, challengeID string) (model.WebAuthnChallenge, error)
	DeleteWebAuthnChallenge(ctx context.Context, challengeID string) error

	PutWebAuthnCredential(ctx context.Context, c model.WebAuthnCredential) error
	GetWebAuthnCredential(ctx context.Context, credentialID string) (model.WebAuthnCredential, error)
	ListWebAuthnCredentials(ctx context.Context) ([]model.WebAuthnCredential, error)
	ListWebAuthnCredentialsByUser(ctx context.Context, userID string) ([]model.WebAuthnCredential, error)
	DeleteAllWebAuthnCredentials(ctx context.Context, userID string) error

	PutRefreshVault(ctx context.Context, v model.RefreshVault) error
	GetRefreshVault(ctx context.Context, userID string) (model.RefreshVault, error)
	DeleteRefreshVault(ctx context.Context, userID string) error
}

// Objects stores blob content addressed by S3-style keys.
type Objects interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (url string, err error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (url string, err error)
}

func defaultSettings() model.Settings {
	return model.Settings{
		CleanupMode:   model.CleanupFaithful,
		RetentionDays: 0,
	}
}
