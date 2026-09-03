package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const webAuthnChallengeTTL = 5 * time.Minute

var (
	ErrWebAuthnChallengeNotFound = errors.New("webauthn challenge not found or expired")
	ErrWebAuthnVerification      = errors.New("webauthn verification failed")
	ErrWebAuthnNotEnrolled       = errors.New("no webauthn credentials enrolled")
	ErrWebAuthnSubMismatch       = errors.New("cognito subject does not match webauthn user")
	ErrWebAuthnMissingRefresh    = errors.New("refresh_token is required to enroll biometric unlock")
	// ErrWebAuthnReEnrolRequired means the assertion was good but the vault
	// behind it was sealed by a key this instance no longer holds — the
	// retired KMS CMK, or a key version that has gone. The entry has been
	// discarded, so enrolling again is the whole fix and it is needed once.
	ErrWebAuthnReEnrolRequired = errors.New("biometric unlock must be set up again on this device")
)

// TokenRefresher exchanges a Cognito refresh token for a new token set.
type TokenRefresher interface {
	Refresh(ctx context.Context, refreshToken string) (model.CognitoTokenSet, error)
}

// SealBox encrypts/decrypts the refresh-token vault payload.
type SealBox interface {
	Seal(ctx context.Context, plaintext []byte) ([]byte, error)
	Open(ctx context.Context, ciphertext []byte) ([]byte, error)
}

type webAuthnUser struct {
	id          []byte
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte                         { return u.id }
func (u *webAuthnUser) WebAuthnName() string                       { return u.name }
func (u *webAuthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// WebAuthnService implements passbook-style biometric enroll/unlock with a Cognito refresh vault.
type WebAuthnService struct {
	store       repository.Store
	wa          *webauthn.WebAuthn
	refresher   TokenRefresher
	box         SealBox
	displayName string
	// verifier validates the ID token Cognito returns from a refresh before its
	// subject is used to bind the KMS-sealed vault. v1 read that subject from
	// an unverified base64 parse.
	verifier auth.Verifier
}

// NewWebAuthnService builds the service. allowedOrigin is e.g. https://vppillai.github.io.
func NewWebAuthnService(store repository.Store, allowedOrigin, displayName string, refresher TokenRefresher, box SealBox, verifier auth.Verifier) (*WebAuthnService, error) {
	if displayName == "" {
		displayName = "Chintan"
	}
	rpID, err := rpIDFromOrigin(allowedOrigin)
	if err != nil {
		return nil, err
	}
	// Enforce the ceremony timeout at the relying party as well as advertising
	// it to the browser. Without Enforce the library records no deadline on the
	// session and validates a ceremony however old it is, leaving the DynamoDB
	// TTL sweep — best effort, documented as up to 48 h, and documented as still
	// returning expired items to reads — as the only bound. Both durations match
	// webAuthnChallengeTTL so the library's deadline and the stored ExpiresAt
	// cannot disagree.
	timeouts := webauthn.TimeoutConfig{
		Enforce:    true,
		Timeout:    webAuthnChallengeTTL,
		TimeoutUVD: webAuthnChallengeTTL,
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: displayName,
		RPOrigins:     []string{allowedOrigin},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        timeouts,
			Registration: timeouts,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn init: %w", err)
	}
	return &WebAuthnService{
		store:       store,
		wa:          wa,
		refresher:   refresher,
		box:         box,
		displayName: displayName,
		verifier:    verifier,
	}, nil
}

func rpIDFromOrigin(allowedOrigin string) (string, error) {
	u, err := url.Parse(allowedOrigin)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid ALLOWED_ORIGIN %q for webauthn RPID", allowedOrigin)
	}
	return u.Hostname(), nil
}

func (s *WebAuthnService) Status(ctx context.Context, userID string) (bool, error) {
	// One credential is enough to answer "is biometrics enrolled".
	creds, err := s.store.ListWebAuthnCredentialsByUser(ctx, userID, repository.ListOptions{Limit: 1})
	if err != nil {
		return false, err
	}
	return len(creds.Items) > 0, nil
}

func (s *WebAuthnService) BeginRegistration(ctx context.Context, userID string) (*model.WebAuthnOptionsResponse, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, err
	}

	rrk := protocol.ResidentKeyRequirementRequired
	sel := protocol.AuthenticatorSelection{
		AuthenticatorAttachment: protocol.Platform,
		ResidentKey:             rrk,
		RequireResidentKey:      protocol.ResidentKeyRequired(),
		UserVerification:        protocol.VerificationRequired,
	}
	exclusions := make([]protocol.CredentialDescriptor, 0, len(user.credentials))
	for _, c := range user.credentials {
		exclusions = append(exclusions, c.Descriptor())
	}

	creation, session, err := s.wa.BeginRegistration(
		user,
		webauthn.WithAuthenticatorSelection(sel),
		webauthn.WithExclusions(exclusions),
	)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}
	return s.persistAndRespond(ctx, session, creation, userID)
}

func (s *WebAuthnService) FinishRegistration(ctx context.Context, userID string, req *model.WebAuthnVerifyRequest) error {
	if strings.TrimSpace(req.RefreshToken) == "" {
		return ErrWebAuthnMissingRefresh
	}
	session, err := s.loadChallenge(ctx, req.ChallengeID)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.DeleteWebAuthnChallenge(ctx, req.ChallengeID) }()

	user, err := s.user(ctx, userID)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		return ErrWebAuthnVerification
	}
	credential, err := s.wa.CreateCredential(user, *session, parsed)
	if err != nil {
		return ErrWebAuthnVerification
	}

	// Everything that can reject the enrolment runs BEFORE anything is written,
	// so there is nothing to unwind. The previous order stored the credential
	// first and, on a rejected refresh token or a sub mismatch, called
	// DeleteAllWebAuthnCredentials — which also deleted every credential the
	// user had enrolled from other devices. Enrolling a laptop with an expired
	// refresh token silently un-enrolled the phone.
	tokens, err := s.refresher.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh token rejected: %w", err)
	}
	sub, err := s.verifiedSub(ctx, tokens.IDToken)
	if err != nil || sub != userID {
		return ErrWebAuthnSubMismatch
	}
	cipher, err := s.box.Seal(ctx, []byte(tokens.RefreshToken))
	if err != nil {
		return fmt.Errorf("seal refresh token: %w", err)
	}

	// Vault first, credential second. If the credential write fails the vault
	// holds a fresh refresh token for this same user, which is harmless; the
	// reverse order could leave a credential that unlocks nothing.
	if err := s.store.PutRefreshVault(ctx, model.RefreshVault{
		UserID:     userID,
		Ciphertext: cipher,
		UpdatedAt:  time.Now().Unix(),
	}); err != nil {
		return err
	}
	return s.storeCredential(ctx, userID, credential)
}

func (s *WebAuthnService) BeginLogin(ctx context.Context) (*model.WebAuthnOptionsResponse, error) {
	// This route is unauthenticated, so it reads one item rather than every
	// credential of every user just to test emptiness.
	creds, err := s.store.ListWebAuthnCredentials(ctx, repository.ListOptions{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(creds.Items) == 0 {
		return nil, ErrWebAuthnNotEnrolled
	}
	assertion, session, err := s.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("begin login: %w", err)
	}
	return s.persistAndRespond(ctx, session, assertion, "")
}

func (s *WebAuthnService) FinishLogin(ctx context.Context, req *model.WebAuthnVerifyRequest) (*model.CognitoTokenSet, error) {
	session, err := s.loadChallenge(ctx, req.ChallengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.store.DeleteWebAuthnChallenge(ctx, req.ChallengeID) }()

	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		return nil, ErrWebAuthnVerification
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		credID := base64.RawURLEncoding.EncodeToString(rawID)
		stored, err := s.store.GetWebAuthnCredential(ctx, credID)
		if err != nil {
			// Some authenticators report ID differently; try userHandle as sub.
			if len(userHandle) > 0 {
				return s.user(ctx, string(userHandle))
			}
			return nil, err
		}
		return s.user(ctx, stored.UserID)
	}

	credential, err := s.wa.ValidateDiscoverableLogin(handler, *session, parsed)
	if err != nil {
		return nil, ErrWebAuthnVerification
	}

	credID := base64.RawURLEncoding.EncodeToString(credential.ID)
	stored, err := s.store.GetWebAuthnCredential(ctx, credID)
	if err != nil {
		return nil, ErrWebAuthnVerification
	}
	userID := stored.UserID
	if err := s.storeCredential(ctx, userID, credential); err != nil {
		return nil, err
	}

	vault, err := s.store.GetRefreshVault(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("refresh vault: %w", err)
	}
	plain, err := s.box.Open(ctx, vault.Ciphertext)
	if err != nil {
		// A blob this key can never open — sealed by the retired KMS CMK, or
		// by a key version that is gone. Leaving it in place makes every later
		// unlock fail identically with nothing the user can do about it, so
		// the entry goes and they enrol once more. Only ErrVaultUnreadable:
		// an AccessDenied or a throttle might not recur, and destroying the
		// vault on one of those turns a blip into a forced re-enrolment.
		if errors.Is(err, ErrVaultUnreadable) {
			if delErr := s.store.DeleteRefreshVault(ctx, userID); delErr != nil {
				return nil, fmt.Errorf("discard unreadable vault: %w", delErr)
			}
			return nil, fmt.Errorf("%w: %v", ErrWebAuthnReEnrolRequired, err)
		}
		return nil, fmt.Errorf("open vault: %w", err)
	}
	tokens, err := s.refresher.Refresh(ctx, string(plain))
	if err != nil {
		return nil, fmt.Errorf("cognito refresh: %w", err)
	}
	sub, err := s.verifiedSub(ctx, tokens.IDToken)
	if err != nil || sub != userID {
		return nil, ErrWebAuthnSubMismatch
	}
	if tokens.RefreshToken != "" {
		cipher, err := s.box.Seal(ctx, []byte(tokens.RefreshToken))
		if err == nil {
			_ = s.store.PutRefreshVault(ctx, model.RefreshVault{
				UserID: userID, Ciphertext: cipher, UpdatedAt: time.Now().Unix(),
			})
		}
	}
	return &tokens, nil
}

func (s *WebAuthnService) Disable(ctx context.Context, userID string) error {
	if err := s.store.DeleteAllWebAuthnCredentials(ctx, userID); err != nil {
		return err
	}
	return s.store.DeleteRefreshVault(ctx, userID)
}

func (s *WebAuthnService) user(ctx context.Context, userID string) (*webAuthnUser, error) {
	stored, err := repository.DrainPages(ctx, 0, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.WebAuthnCredential], error) {
		return s.store.ListWebAuthnCredentialsByUser(ctx, userID, opts)
	})
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(stored))
	for _, c := range stored {
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(c.Credential), &cred); err != nil {
			return nil, fmt.Errorf("decode credential: %w", err)
		}
		creds = append(creds, cred)
	}
	return &webAuthnUser{
		id:          []byte(userID),
		name:        userID,
		displayName: s.displayName,
		credentials: creds,
	}, nil
}

func (s *WebAuthnService) storeCredential(ctx context.Context, userID string, credential *webauthn.Credential) error {
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	return s.store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID:       userID,
		CredentialID: base64.RawURLEncoding.EncodeToString(credential.ID),
		Credential:   string(raw),
		SignCount:    credential.Authenticator.SignCount,
		CreatedAt:    time.Now().Unix(),
	})
}

func (s *WebAuthnService) persistAndRespond(ctx context.Context, session *webauthn.SessionData, options interface{}, userID string) (*model.WebAuthnOptionsResponse, error) {
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	challengeID := uuid.New().String()
	now := time.Now()
	if err := s.store.PutWebAuthnChallenge(ctx, model.WebAuthnChallenge{
		ChallengeID: challengeID,
		SessionData: string(sessionJSON),
		UserID:      userID,
		CreatedAt:   now.Unix(),
		ExpiresAt:   now.Add(webAuthnChallengeTTL).Unix(),
	}); err != nil {
		return nil, err
	}
	return &model.WebAuthnOptionsResponse{ChallengeID: challengeID, Options: optionsJSON}, nil
}

func (s *WebAuthnService) loadChallenge(ctx context.Context, challengeID string) (*webauthn.SessionData, error) {
	entry, err := s.store.GetWebAuthnChallenge(ctx, challengeID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrWebAuthnChallengeNotFound
		}
		return nil, err
	}
	// Expiry is checked here, not only in the store. DynamoDB's TTL is a
	// best-effort sweep that AWS documents as taking up to 48 hours, and it
	// explicitly still returns expired items to reads in the meantime — so a
	// store that answers with the item is not evidence the ceremony is live.
	// An abandoned challenge_id that stays redeemable is a replay into a live
	// Cognito token set, or a re-binding of the KMS-sealed refresh vault.
	// Same semantics as the in-memory store: ExpiresAt of 0 means "unset".
	if entry.ExpiresAt > 0 && time.Now().Unix() > entry.ExpiresAt {
		return nil, ErrWebAuthnChallengeNotFound
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(entry.SessionData), &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// verifiedSub returns the subject of a Cognito ID token after full signature
// and claim verification.
//
// This guards the most sensitive binding in the system: which user a KMS-sealed
// refresh token belongs to. v1 base64-decoded the payload and read `sub` with no
// signature check at all. A missing verifier fails closed rather than falling
// back to that parse.
func (s *WebAuthnService) verifiedSub(ctx context.Context, idToken string) (string, error) {
	if s.verifier == nil {
		return "", fmt.Errorf("webauthn: no token verifier configured")
	}
	id, err := s.verifier.Verify(ctx, idToken)
	if err != nil {
		return "", err
	}
	if id.UserID == "" {
		return "", fmt.Errorf("webauthn: verified token has no subject")
	}
	return id.UserID, nil
}
