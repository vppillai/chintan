package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// emptySubjectVerifier accepts a token and reports no subject, which is what a
// verifier configured against the wrong pool can do.
type emptySubjectVerifier struct{}

func (emptySubjectVerifier) Verify(context.Context, string) (auth.Identity, error) {
	return auth.Identity{}, nil
}

// rejectingVerifier is a verifier that refuses the token, as the real one does
// for a bad signature or a stale key.
type rejectingVerifier struct{ err error }

func (v rejectingVerifier) Verify(context.Context, string) (auth.Identity, error) {
	return auth.Identity{}, v.err
}

// unverifiedIDToken builds a token whose base64 payload claims sub. It is
// unsigned, so anybody can make one; that is the point.
func unverifiedIDToken(sub string) string {
	payload, _ := json.Marshal(map[string]string{"sub": sub})
	return "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// This is the whole reason verifiedSub exists. v1 base64-decoded the payload
// and read `sub` with no signature check at all, so anyone who could reach the
// endpoint could name the user whose vault they were binding. The subject that
// counts is the verifier's, and the token's own claim is ignored.
func TestVerifiedSubTrustsTheVerifierAndNotTheTokenPayload(t *testing.T) {
	svc := &WebAuthnService{verifier: stubIDVerifier{sub: "user-1"}}

	got, err := svc.verifiedSub(context.Background(), unverifiedIDToken("attacker"))
	if err != nil {
		t.Fatalf("verifiedSub: %v", err)
	}
	if got != "user-1" {
		t.Fatalf("subject = %q, want user-1: the payload claims %q and must not be believed", got, "attacker")
	}
}

func TestVerifiedSubFailsWhenTheVerifierRejectsTheToken(t *testing.T) {
	boom := errors.New("auth: unauthenticated")
	svc := &WebAuthnService{verifier: rejectingVerifier{err: boom}}

	got, err := svc.verifiedSub(context.Background(), unverifiedIDToken("user-1"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the verifier's rejection", err)
	}
	if got != "" {
		t.Fatalf("subject = %q returned alongside the rejection", got)
	}
}

// A token that verifies but names nobody cannot bind a vault to anybody. An
// empty subject compared against an empty userID would otherwise pass the
// mismatch check.
func TestVerifiedSubFailsWhenTheVerifiedTokenHasNoSubject(t *testing.T) {
	svc := &WebAuthnService{verifier: emptySubjectVerifier{}}

	got, err := svc.verifiedSub(context.Background(), unverifiedIDToken(""))
	if err == nil {
		t.Fatal("verifiedSub accepted a verified token carrying no subject")
	}
	if got != "" {
		t.Fatalf("subject = %q returned alongside the rejection", got)
	}
}

// The RPID is the registrable domain the credential is scoped to. An origin
// with no host would produce an empty RPID, which scopes a passkey to nothing.
func TestNewWebAuthnServiceRefusesAnOriginItCannotDeriveAnRPIDFrom(t *testing.T) {
	for _, origin := range []string{"", "not-a-url", "/relative/path"} {
		svc, err := NewWebAuthnService(memory.NewStore(), origin, "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
		if err == nil {
			t.Errorf("NewWebAuthnService(%q) succeeded, want a refusal: there is no host to scope credentials to", origin)
		}
		if svc != nil {
			t.Errorf("NewWebAuthnService(%q) returned a service alongside the error", origin)
		}
	}
}

func TestNewWebAuthnServiceDerivesTheRPIDFromTheAllowedOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"https://vppillai.github.io":      "vppillai.github.io",
		"http://localhost:3000":           "localhost",
		"https://notes.example.com:8443/": "notes.example.com",
	} {
		got, err := rpIDFromOrigin(origin)
		if err != nil {
			t.Errorf("rpIDFromOrigin(%q): %v", origin, err)
			continue
		}
		if got != want {
			t.Errorf("rpIDFromOrigin(%q) = %q, want %q (host without the port)", origin, got, want)
		}
	}
}

func TestNewWebAuthnServiceNamesItselfWhenTheOperatorDidNot(t *testing.T) {
	svc, err := NewWebAuthnService(memory.NewStore(), "https://vppillai.github.io", "", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatalf("NewWebAuthnService: %v", err)
	}
	if svc.displayName != "Chintan" {
		t.Fatalf("display name = %q, want a default: the name is shown in the platform's biometric prompt and cannot be blank", svc.displayName)
	}
}

// The challenge carries a TTL. An expired one must read as "not found" rather
// than as a usable ceremony, or a captured response stays replayable for as
// long as the row survives.
func TestFinishRegistrationRefusesAnExpiredChallenge(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutWebAuthnChallenge(ctx, model.WebAuthnChallenge{
		ChallengeID: "stale",
		SessionData: `{"challenge":"abc"}`,
		UserID:      "user-1",
		CreatedAt:   time.Now().Add(-time.Hour).Unix(),
		ExpiresAt:   time.Now().Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("PutWebAuthnChallenge: %v", err)
	}

	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID: "stale", Credential: []byte("{}"), RefreshToken: "rt",
	})
	if err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("err = %v, want ErrWebAuthnChallengeNotFound for a challenge past its TTL", err)
	}
}

func TestFinishRegistrationRefusesAChallengeThatWasNeverIssued(t *testing.T) {
	svc, err := NewWebAuthnService(memory.NewStore(), "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}

	err = svc.FinishRegistration(context.Background(), "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID: "never-issued", Credential: []byte("{}"), RefreshToken: "rt",
	})
	if err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("err = %v, want ErrWebAuthnChallengeNotFound", err)
	}
}

// The verification failure is one opaque error whichever way it failed. Telling
// a caller which step of the ceremony broke is a probing oracle.
func TestFinishRegistrationRefusesACredentialItCannotParse(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	for name, credential := range map[string]string{
		"not json":            `{{{`,
		"empty object":        `{}`,
		"no attestation":      `{"id":"YQ","rawId":"YQ","type":"public-key","response":{}}`,
		"wrong credential id": `{"id":"!!!","rawId":"YQ","type":"public-key","response":{"clientDataJSON":"e30","attestationObject":"oA"}}`,
	} {
		// The challenge is consumed by each attempt, so each one gets its own.
		fresh, err := svc.BeginRegistration(ctx, "user-1")
		if err != nil {
			t.Fatalf("BeginRegistration: %v", err)
		}
		err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
			ChallengeID: fresh.ChallengeID, Credential: []byte(credential), RefreshToken: "rt",
		})
		if err != ErrWebAuthnVerification {
			t.Errorf("%s: err = %v, want ErrWebAuthnVerification", name, err)
		}
	}
	_ = options
}

// BeginRegistration stores the ceremony under an id the client hands back, with
// an expiry, so an abandoned registration cannot be finished a week later.
func TestBeginRegistrationStoresACeremonyThatExpires(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if options.ChallengeID == "" {
		t.Fatal("no challenge id was issued, so the client has nothing to return with the credential")
	}
	if len(options.Options) == 0 {
		t.Fatal("no creation options were returned, so navigator.credentials.create has nothing to be called with")
	}

	stored, err := store.GetWebAuthnChallenge(ctx, options.ChallengeID)
	if err != nil {
		t.Fatalf("the ceremony was not stored: %v", err)
	}
	if stored.UserID != "user-1" {
		t.Errorf("stored ceremony user = %q, want user-1: a registration ceremony belongs to the user who started it", stored.UserID)
	}
	if stored.ExpiresAt <= stored.CreatedAt {
		t.Errorf("ceremony expires at %d having been created at %d; a challenge with no TTL stays replayable", stored.ExpiresAt, stored.CreatedAt)
	}
	if got := time.Duration(stored.ExpiresAt-stored.CreatedAt) * time.Second; got != webAuthnChallengeTTL {
		t.Errorf("ceremony TTL = %s, want %s", got, webAuthnChallengeTTL)
	}
}

// The login route is unauthenticated, so it must not offer a ceremony before
// anything is enrolled: a discoverable login against an empty credential set
// can only end in a failure the caller cannot act on.
func TestBeginLoginRefusesWhenNothingIsEnrolled(t *testing.T) {
	svc, err := NewWebAuthnService(memory.NewStore(), "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}

	options, err := svc.BeginLogin(context.Background())
	if err != ErrWebAuthnNotEnrolled {
		t.Fatalf("err = %v, want ErrWebAuthnNotEnrolled", err)
	}
	if options != nil {
		t.Fatalf("options %+v were returned alongside the refusal", options)
	}
}

// A login ceremony is not bound to a user: the authenticator decides which
// credential it holds, so the stored ceremony has no user id to record.
func TestBeginLoginIssuesAnUnboundCeremonyOnceSomethingIsEnrolled(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID: "user-1", CredentialID: "c1", Credential: `{"ID":"Yw=="}`, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PutWebAuthnCredential: %v", err)
	}

	options, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	stored, err := store.GetWebAuthnChallenge(ctx, options.ChallengeID)
	if err != nil {
		t.Fatalf("the login ceremony was not stored: %v", err)
	}
	if stored.UserID != "" {
		t.Fatalf("login ceremony records user %q; the route is unauthenticated and must not claim to know who is logging in", stored.UserID)
	}
}

// Disabling biometric unlock destroys the vault as well as the credentials.
// Leaving a sealed Cognito refresh token behind after the user turned the
// feature off is a standing grant they believe they revoked.
func TestDisableDestroysTheVaultAsWellAsTheCredentials(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID: "user-1", CredentialID: "c1", Credential: `{"ID":"Yw=="}`,
	}); err != nil {
		t.Fatalf("PutWebAuthnCredential: %v", err)
	}
	if err := store.PutRefreshVault(ctx, model.RefreshVault{UserID: "user-1", Ciphertext: []byte("sealed")}); err != nil {
		t.Fatalf("PutRefreshVault: %v", err)
	}

	if err := svc.Disable(ctx, "user-1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if enrolled, _ := svc.Status(ctx, "user-1"); enrolled {
		t.Error("the credentials survived Disable")
	}
	if _, err := store.GetRefreshVault(ctx, "user-1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("GetRefreshVault after Disable = %v, want ErrNotFound: a sealed refresh token left behind is a grant the user thinks they revoked", err)
	}
}

// A half-done Disable must report failure. Reporting success with the vault
// still in place tells the user their token is gone when it is not.
func TestDisableReportsAFailureToDestroyTheCredentials(t *testing.T) {
	boom := errors.New("dynamodb: ConditionalCheckFailedException")
	store := errCredentialStore{Store: memory.NewStore(), err: boom}
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Disable(context.Background(), "user-1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the delete failure rather than a silent success", err)
	}
}

func TestStatusSurfacesACredentialReadFailure(t *testing.T) {
	boom := errors.New("dynamodb: dial tcp: connection refused")
	svc, err := NewWebAuthnService(credentialListErrStore{Store: memory.NewStore(), err: boom},
		"https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}

	enrolled, err := svc.Status(context.Background(), "user-1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's failure", err)
	}
	if enrolled {
		t.Fatal("Status = true when the credential list could not be read")
	}
}

// The stored credential is JSON the ceremony has to reconstruct. A record that
// will not decode must stop the ceremony rather than silently produce a user
// with fewer credentials than they enrolled.
func TestUserRefusesToBuildFromACredentialItCannotDecode(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID: "user-1", CredentialID: "c1", Credential: "this is not json",
	}); err != nil {
		t.Fatalf("PutWebAuthnCredential: %v", err)
	}

	if _, err := svc.user(ctx, "user-1"); err == nil {
		t.Fatal("user() accepted a stored credential that does not decode")
	}
}

// storeCredential and user are the two halves of one round trip. What goes into
// DynamoDB has to come back out as the same webauthn.Credential, because the
// public key inside it is what every future assertion is checked against.
func TestStoreCredentialAndUserRoundTripTheAuthenticatorRecord(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{}, stubIDVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	credential := &webauthn.Credential{
		ID:        []byte("credential-id"),
		PublicKey: []byte{1, 2, 3, 4},
	}
	credential.Authenticator.SignCount = 7
	if err := svc.storeCredential(ctx, "user-1", credential); err != nil {
		t.Fatalf("storeCredential: %v", err)
	}

	stored, err := store.GetWebAuthnCredential(ctx, base64.RawURLEncoding.EncodeToString(credential.ID))
	if err != nil {
		t.Fatalf("the credential was not stored under its base64url id: %v", err)
	}
	if stored.UserID != "user-1" {
		t.Errorf("stored credential belongs to %q, want user-1", stored.UserID)
	}
	if stored.SignCount != 7 {
		t.Errorf("sign count = %d, want 7: the counter is how a cloned authenticator is spotted", stored.SignCount)
	}

	user, err := svc.user(ctx, "user-1")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if string(user.WebAuthnID()) != "user-1" {
		t.Errorf("WebAuthnID = %q, want the Cognito subject: it is the user handle a discoverable login returns", user.WebAuthnID())
	}
	if user.WebAuthnName() != "user-1" {
		t.Errorf("WebAuthnName = %q, want user-1", user.WebAuthnName())
	}
	if user.WebAuthnDisplayName() != "Chintan" {
		t.Errorf("WebAuthnDisplayName = %q, want the configured display name", user.WebAuthnDisplayName())
	}
	creds := user.WebAuthnCredentials()
	if len(creds) != 1 {
		t.Fatalf("credentials = %d, want the one that was stored", len(creds))
	}
	if string(creds[0].ID) != "credential-id" {
		t.Errorf("credential id = %q, want it round-tripped intact", creds[0].ID)
	}
	if string(creds[0].PublicKey) != string(credential.PublicKey) {
		t.Errorf("public key = %v, want %v: every future assertion is checked against it", creds[0].PublicKey, credential.PublicKey)
	}
}

// credentialListErrStore fails the credential list Status reads.
type credentialListErrStore struct {
	repository.Store
	err error
}

func (s credentialListErrStore) ListWebAuthnCredentialsByUser(context.Context, string, repository.ListOptions) (repository.Page[model.WebAuthnCredential], error) {
	return repository.Page[model.WebAuthnCredential]{}, s.err
}
