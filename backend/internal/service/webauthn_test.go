package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestWebAuthnStatusAndDisable(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ok, err := svc.Status(ctx, "user-1")
	if err != nil || ok {
		t.Fatalf("expected not enrolled, ok=%v err=%v", ok, err)
	}

	_ = store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID: "user-1", CredentialID: "c1", Credential: `{"ID":"Yw=="}`, CreatedAt: time.Now().Unix(),
	})
	_ = store.PutRefreshVault(ctx, model.RefreshVault{UserID: "user-1", Ciphertext: []byte("rt")})

	ok, err = svc.Status(ctx, "user-1")
	if err != nil || !ok {
		t.Fatalf("expected enrolled, ok=%v err=%v", ok, err)
	}
	if err := svc.Disable(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	ok, _ = svc.Status(ctx, "user-1")
	if ok {
		t.Fatal("expected disabled")
	}
	if _, err := store.GetRefreshVault(ctx, "user-1"); err != repository.ErrNotFound {
		t.Fatalf("vault should be deleted: %v", err)
	}
}

func TestWebAuthnBeginLoginNotEnrolled(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.BeginLogin(context.Background())
	if err != ErrWebAuthnNotEnrolled {
		t.Fatalf("got %v", err)
	}
}

func TestFinishRegistrationRequiresRefresh(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.FinishRegistration(context.Background(), "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID: "x", Credential: []byte("{}"),
	})
	if err != ErrWebAuthnMissingRefresh {
		t.Fatalf("got %v", err)
	}
}

// stubIDVerifier stands in for the Cognito verifier. FakeRefresher mints
// synthetic, unsigned ID tokens, which a real verifier correctly rejects.
type stubIDVerifier struct{ sub string }

func (s stubIDVerifier) Verify(context.Context, string) (auth.Identity, error) {
	if s.sub == "" {
		return auth.Identity{}, errors.New("no subject")
	}
	return auth.Identity{UserID: s.sub, TenantID: s.sub}, nil
}

// A service with no verifier must refuse to bind a vault rather than trusting
// the token's payload, which is what v1 did.
func TestVerifiedSubFailsClosedWithoutVerifier(t *testing.T) {
	svc := &WebAuthnService{}
	if _, err := svc.verifiedSub(context.Background(), "a.eyJzdWIiOiJ1c2VyLTEifQ.b"); err == nil {
		t.Fatal("expected failure with no verifier configured")
	}
}

// -------------------------------------------------- challenge expiry (M1)

// leakyChallengeStore reproduces DynamoDB's read semantics for a TTL'd item.
//
// The table TTL was the only thing bounding a challenge in production, and AWS
// documents that sweep as best effort — up to 48 hours — with expired items
// still returned to reads until it runs. The in-memory store filters on
// ExpiresAt itself, so every existing expiry test is really asserting the
// double's behaviour and cannot observe a challenge that outlived its TTL.
// This store returns what was written, unfiltered, exactly as
// DynamoStore.GetWebAuthnChallenge does.
type leakyChallengeStore struct {
	repository.Store
	mu         sync.Mutex
	challenges map[string]model.WebAuthnChallenge
}

func newLeakyChallengeStore() *leakyChallengeStore {
	return &leakyChallengeStore{
		Store:      memory.NewStore(),
		challenges: make(map[string]model.WebAuthnChallenge),
	}
}

func (s *leakyChallengeStore) PutWebAuthnChallenge(_ context.Context, c model.WebAuthnChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[c.ChallengeID] = c
	return nil
}

func (s *leakyChallengeStore) GetWebAuthnChallenge(_ context.Context, challengeID string) (model.WebAuthnChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[challengeID]
	if !ok {
		return model.WebAuthnChallenge{}, repository.ErrNotFound
	}
	return c, nil
}

func (s *leakyChallengeStore) DeleteWebAuthnChallenge(_ context.Context, challengeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.challenges, challengeID)
	return nil
}

// expire back-dates a stored ceremony, standing in for the wall clock moving on.
func (s *leakyChallengeStore) expire(t *testing.T, challengeID string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[challengeID]
	if !ok {
		t.Fatalf("no challenge %q to expire", challengeID)
	}
	c.CreatedAt = time.Now().Add(-48 * time.Hour).Unix()
	c.ExpiresAt = c.CreatedAt + int64(webAuthnChallengeTTL.Seconds())
	s.challenges[challengeID] = c
}

func (s *leakyChallengeStore) sessionOf(t *testing.T, challengeID string) webauthn.SessionData {
	t.Helper()
	entry, err := s.GetWebAuthnChallenge(context.Background(), challengeID)
	if err != nil {
		t.Fatalf("GetWebAuthnChallenge(%s): %v", challengeID, err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(entry.SessionData), &session); err != nil {
		t.Fatalf("decode session data: %v", err)
	}
	return session
}

func leakyService(t *testing.T) (*leakyChallengeStore, *WebAuthnService) {
	t.Helper()
	store := newLeakyChallengeStore()
	svc, err := NewWebAuthnService(store, testOrigin, "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatalf("NewWebAuthnService: %v", err)
	}
	return store, svc
}

// A challenge past its TTL must not load as a usable session even when the
// store still hands it over. Without this check the only bound on an abandoned
// ceremony is DynamoDB's sweep, so a challenge_id stays redeemable for days.
func TestLoadChallengeRefusesAnEntryThatOutlivedItsTTL(t *testing.T) {
	store, svc := leakyService(t)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	// Fresh, it loads.
	if _, err := svc.loadChallenge(ctx, options.ChallengeID); err != nil {
		t.Fatalf("loadChallenge(fresh) = %v, want a session", err)
	}

	store.expire(t, options.ChallengeID)
	session, err := svc.loadChallenge(ctx, options.ChallengeID)
	if !errors.Is(err, ErrWebAuthnChallengeNotFound) {
		t.Fatalf("loadChallenge(expired) err = %v, want ErrWebAuthnChallengeNotFound", err)
	}
	if session != nil {
		t.Error("an expired ceremony was handed back as a usable session")
	}
}

// The same property through the routes: an otherwise perfectly valid assertion,
// replayed against a ceremony that has outlived its TTL, must be refused before
// it can be exchanged for a live Cognito token set.
func TestFinishLoginRefusesAValidAssertionOnAnExpiredChallenge(t *testing.T) {
	store, svc := leakyService(t)
	ctx := context.Background()
	authenticator := newVirtualAuthenticator(t, testOrigin, testRPID)

	reg, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  reg.ChallengeID,
		Credential:   authenticator.register(t, store.sessionOf(t, reg.ChallengeID).Challenge),
		RefreshToken: "rt",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	login, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	assertion := authenticator.assert(t, store.sessionOf(t, login.ChallengeID).Challenge, []byte("user-1"))

	// The same assertion succeeds while the ceremony is live, so the refusal
	// below can only be the expiry.
	if _, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: login.ChallengeID, Credential: assertion,
	}); err != nil {
		t.Fatalf("FinishLogin(live) = %v, want tokens", err)
	}

	replay, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	assertion = authenticator.assert(t, store.sessionOf(t, replay.ChallengeID).Challenge, []byte("user-1"))
	store.expire(t, replay.ChallengeID)

	tokens, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: replay.ChallengeID, Credential: assertion,
	})
	if !errors.Is(err, ErrWebAuthnChallengeNotFound) {
		t.Fatalf("FinishLogin(expired) err = %v, want ErrWebAuthnChallengeNotFound", err)
	}
	if tokens != nil {
		t.Error("an expired ceremony produced a live Cognito token set")
	}
}

func TestFinishRegistrationRefusesAChallengeThatOutlivedItsTTL(t *testing.T) {
	store, svc := leakyService(t)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	store.expire(t, options.ChallengeID)

	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   []byte(`{"id":"YQ","rawId":"YQ","type":"public-key","response":{"clientDataJSON":"e30","attestationObject":"oA"}}`),
		RefreshToken: "rt",
	})
	if !errors.Is(err, ErrWebAuthnChallengeNotFound) {
		t.Fatalf("FinishRegistration(expired) err = %v, want ErrWebAuthnChallengeNotFound", err)
	}
}

// The stored ExpiresAt is one bound; the library's own Timeouts.*.Enforce is
// the other, and it is checked by different code on a different path. It is
// only armed when the deadline is recorded on the session at Begin time.
func TestBegunCeremoniesCarryAServerEnforcedDeadline(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		begin func(*WebAuthnService) (*model.WebAuthnOptionsResponse, error)
	}{
		{"registration", func(s *WebAuthnService) (*model.WebAuthnOptionsResponse, error) {
			return s.BeginRegistration(ctx, "user-1")
		}},
		{"login", func(s *WebAuthnService) (*model.WebAuthnOptionsResponse, error) {
			return s.BeginLogin(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, svc := leakyService(t)
			if err := store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
				UserID: "user-1", CredentialID: "c1", Credential: `{"ID":"Yw=="}`,
			}); err != nil {
				t.Fatalf("PutWebAuthnCredential: %v", err)
			}

			begun := time.Now()
			options, err := tc.begin(svc)
			if err != nil {
				t.Fatalf("begin %s: %v", tc.name, err)
			}
			expires := store.sessionOf(t, options.ChallengeID).Expires
			if expires.IsZero() {
				t.Fatalf("%s session carries no deadline: Timeouts.%s.Enforce is off, so the library validates this ceremony however old it is", tc.name, tc.name)
			}
			// The library's own default is 60s, so the deadline has to be the
			// configured TTL rather than whatever the library fell back to:
			// the session deadline and the stored ExpiresAt must not disagree.
			if drift := expires.Sub(begun.Add(webAuthnChallengeTTL)); drift < -time.Second || drift > time.Second {
				t.Errorf("%s session expires %s away from the %s challenge TTL", tc.name, drift, webAuthnChallengeTTL)
			}
		})
	}
}
