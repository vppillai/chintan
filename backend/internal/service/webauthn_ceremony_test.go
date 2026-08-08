package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// The vault binding — the branch deciding which user a KMS-sealed Cognito
// refresh token belongs to — sits behind credential verification, so a test
// that reached it by calling the internals directly would prove the binding is
// reachable rather than that it is reached. The authenticator below is
// therefore a real one: an ES256 key pair and the CBOR a platform authenticator
// would hand the browser, verified by the same library the production path
// uses.
type virtualAuthenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	origin string
	rpID   string
}

// Authenticator data flags, from §6.1 of the WebAuthn specification.
const (
	flagUserPresent          byte = 0x01
	flagUserVerified         byte = 0x04
	flagAttestedCredentialID byte = 0x40
)

func newVirtualAuthenticator(t *testing.T, origin, rpID string) *virtualAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate authenticator key: %v", err)
	}
	credID := make([]byte, 16)
	if _, err := rand.Read(credID); err != nil {
		t.Fatalf("generate credential id: %v", err)
	}
	return &virtualAuthenticator{key: key, credID: credID, origin: origin, rpID: rpID}
}

// cborHead writes a CBOR type header. Nothing else in this module encodes CBOR,
// so the handful of shapes an attestation object needs are written out here
// rather than pulling a decoder's dependency into the test binary.
func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	default:
		return []byte{major<<5 | 26, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

func cborBytes(b []byte) []byte   { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte    { return append(cborHead(3, uint64(len(s))), s...) }
func cborNegative(v int64) []byte { return cborHead(1, uint64(-v-1)) }

// coseES256 renders a P-256 public key as the COSE_Key an authenticator embeds
// in attested credential data.
func coseES256(pub *ecdsa.PublicKey) []byte {
	out := cborHead(5, 5) // map of five labels
	out = append(out, cborHead(0, 1)...)
	out = append(out, cborHead(0, 2)...) // kty: EC2
	out = append(out, cborHead(0, 3)...)
	out = append(out, cborNegative(-7)...) // alg: ES256
	out = append(out, cborNegative(-1)...)
	out = append(out, cborHead(0, 1)...) // crv: P-256
	out = append(out, cborNegative(-2)...)
	out = append(out, cborBytes(pub.X.FillBytes(make([]byte, 32)))...)
	out = append(out, cborNegative(-3)...)
	out = append(out, cborBytes(pub.Y.FillBytes(make([]byte, 32)))...)
	return out
}

func (a *virtualAuthenticator) authData(flags byte) []byte {
	rpIDHash := sha256.Sum256([]byte(a.rpID))
	out := append([]byte{}, rpIDHash[:]...)
	out = append(out, flags)
	out = append(out, 0, 0, 0, 0) // signature counter
	if flags&flagAttestedCredentialID != 0 {
		out = append(out, make([]byte, 16)...) // AAGUID: this authenticator claims none
		out = append(out, byte(len(a.credID)>>8), byte(len(a.credID)))
		out = append(out, a.credID...)
		out = append(out, coseES256(&a.key.PublicKey)...)
	}
	return out
}

func (a *virtualAuthenticator) clientData(t *testing.T, ceremony, challenge string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":      ceremony,
		"challenge": challenge,
		"origin":    a.origin,
	})
	if err != nil {
		t.Fatalf("encode client data: %v", err)
	}
	return raw
}

// register produces the attestation response for a registration challenge. The
// attestation format is "none", which is what a platform authenticator sends
// when the relying party asks for no attestation.
func (a *virtualAuthenticator) register(t *testing.T, challenge string) []byte {
	t.Helper()
	clientData := a.clientData(t, "webauthn.create", challenge)
	authData := a.authData(flagUserPresent | flagUserVerified | flagAttestedCredentialID)

	attestation := cborHead(5, 3)
	attestation = append(attestation, cborText("fmt")...)
	attestation = append(attestation, cborText("none")...)
	attestation = append(attestation, cborText("attStmt")...)
	attestation = append(attestation, cborHead(5, 0)...)
	attestation = append(attestation, cborText("authData")...)
	attestation = append(attestation, cborBytes(authData)...)

	body, err := json.Marshal(map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
		},
	})
	if err != nil {
		t.Fatalf("encode registration response: %v", err)
	}
	return body
}

// assert produces the assertion response for a login challenge, signed with the
// same key the credential was registered with.
func (a *virtualAuthenticator) assert(t *testing.T, challenge string, userHandle []byte) []byte {
	t.Helper()
	clientData := a.clientData(t, "webauthn.get", challenge)
	authData := a.authData(flagUserPresent | flagUserVerified)

	clientDataHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
			"userHandle":        base64.RawURLEncoding.EncodeToString(userHandle),
		},
	})
	if err != nil {
		t.Fatalf("encode assertion response: %v", err)
	}
	return body
}

const (
	testOrigin = "https://vppillai.github.io"
	testRPID   = "vppillai.github.io"
)

// enrolFixture builds a service and the authenticator that will enrol against
// it.
func enrolFixture(t *testing.T, refresher TokenRefresher, box SealBox, verifier stubIDVerifier) (*memory.Store, *WebAuthnService, *virtualAuthenticator) {
	t.Helper()
	store := memory.NewStore()
	svc, err := NewWebAuthnService(store, testOrigin, "Chintan", refresher, box, verifier)
	if err != nil {
		t.Fatalf("NewWebAuthnService: %v", err)
	}
	return store, svc, newVirtualAuthenticator(t, testOrigin, testRPID)
}

// challengeOf reads back the ceremony the service stored, which is the only
// place the challenge the authenticator must sign over is written down.
func challengeOf(t *testing.T, store *memory.Store, challengeID string) string {
	t.Helper()
	entry, err := store.GetWebAuthnChallenge(context.Background(), challengeID)
	if err != nil {
		t.Fatalf("GetWebAuthnChallenge(%s): %v", challengeID, err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(entry.SessionData), &session); err != nil {
		t.Fatalf("decode session data: %v", err)
	}
	return session.Challenge
}

// Enrolment binds the vault only after Cognito accepts the refresh token, and
// what is sealed into it is the token Cognito returned, not the one the client
// sent. Sealing the client's token would leave the vault holding a credential
// that may already have been rotated away.
func TestFinishRegistrationSealsTheRefreshTokenCognitoReturned(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Sub: "user-1", RefreshToken: "rotated-refresh-token"},
		PlainBox{},
		stubIDVerifier{sub: "user-1"},
	)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "the-token-the-client-sent",
	})
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	vault, err := store.GetRefreshVault(ctx, "user-1")
	if err != nil {
		t.Fatalf("no vault was written for the enrolled user: %v", err)
	}
	plain, err := PlainBox{}.Open(ctx, vault.Ciphertext)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	if string(plain) != "rotated-refresh-token" {
		t.Fatalf("vault holds %q, want the refresh token Cognito returned", plain)
	}
	if vault.UpdatedAt == 0 {
		t.Error("the vault carries no update time, so nothing can tell a fresh binding from a stale one")
	}

	enrolled, err := svc.Status(ctx, "user-1")
	if err != nil || !enrolled {
		t.Fatalf("Status = %v, %v; want the user reported as enrolled", enrolled, err)
	}
}

// This is the binding that decides which user a KMS-sealed refresh token
// belongs to. If the verified subject is not the authenticated user, the
// enrolment is refused and the credentials stored moments earlier are destroyed
// — leaving them would enrol an authenticator against a vault that was never
// written, so the next unlock fails with no way for the user to see why.
func TestFinishRegistrationRefusesAndUnwindsWhenTheVerifiedSubjectIsSomebodyElse(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Sub: "user-1"},
		PlainBox{},
		stubIDVerifier{sub: "somebody-else"},
	)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "a-real-refresh-token",
	})

	if err != ErrWebAuthnSubMismatch {
		t.Fatalf("err = %v, want ErrWebAuthnSubMismatch: the verified token's subject is not the authenticated user", err)
	}
	if _, err := store.GetRefreshVault(ctx, "user-1"); err == nil {
		t.Fatal("a refresh vault was written despite the subject mismatch")
	}
	enrolled, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if enrolled {
		t.Fatal("Status = true after a refused enrolment; the credentials stored before the check were not destroyed, so the user is enrolled against a vault that does not exist")
	}
}

// A refresh token Cognito rejects is not an enrolment. The credentials go with
// it for the same reason.
func TestFinishRegistrationUnwindsWhenCognitoRejectsTheRefreshToken(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Err: errStub("NotAuthorizedException: Refresh Token has been revoked")},
		PlainBox{},
		stubIDVerifier{sub: "user-1"},
	)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "a-revoked-token",
	})

	if err == nil {
		t.Fatal("FinishRegistration succeeded with a refresh token Cognito rejected")
	}
	enrolled, _ := svc.Status(ctx, "user-1")
	if enrolled {
		t.Fatal("Status = true after Cognito rejected the refresh token; the credentials were left behind")
	}
}

// A KMS that cannot seal is not an enrolment either. Reporting success here
// would leave the user believing biometric unlock works with no vault to
// unlock.
func TestFinishRegistrationUnwindsWhenTheVaultCannotBeSealed(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Sub: "user-1"},
		errBox{err: errStub("kms: KMSInvalidStateException")},
		stubIDVerifier{sub: "user-1"},
	)
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "a-real-refresh-token",
	})

	if err == nil {
		t.Fatal("FinishRegistration succeeded with a seal box that cannot seal")
	}
	if _, err := store.GetRefreshVault(ctx, "user-1"); err == nil {
		t.Fatal("a refresh vault was written despite the seal failing")
	}
	enrolled, _ := svc.Status(ctx, "user-1")
	if enrolled {
		t.Fatal("Status = true after the seal failed; the credentials were left behind")
	}
}

// The challenge is single-use: it is deleted whichever way the ceremony ends,
// so a captured registration response cannot be replayed.
func TestFinishRegistrationConsumesTheChallengeWhicheverWayItEnds(t *testing.T) {
	store, svc, auth := enrolFixture(t, &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	credential := auth.register(t, challengeOf(t, store, options.ChallengeID))
	req := &model.WebAuthnVerifyRequest{
		ChallengeID: options.ChallengeID, Credential: credential, RefreshToken: "rt",
	}
	if err := svc.FinishRegistration(ctx, "user-1", req); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	if err := svc.FinishRegistration(ctx, "user-1", req); err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("replaying the same registration response gave %v, want ErrWebAuthnChallengeNotFound", err)
	}
}

// Unlock opens the vault, refreshes against Cognito, and only then hands tokens
// back — and it checks the verified subject again, because the vault could have
// been written by an earlier version or restored from a backup of another
// tenant's table.
func TestFinishLoginReturnsCognitoTokensForTheEnrolledUser(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Sub: "user-1"},
		PlainBox{},
		stubIDVerifier{sub: "user-1"},
	)
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "the-sealed-token",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	tokens, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  auth.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if tokens.AccessToken == "" || tokens.IDToken == "" {
		t.Fatalf("tokens = %+v, want the set the SPA needs", tokens)
	}
	if tokens.RefreshToken != "the-sealed-token" {
		t.Fatalf("refresh token = %q, want the one the vault held", tokens.RefreshToken)
	}
}

// The same subject check guards unlock. A vault whose contents refresh into
// somebody else's identity must not mint tokens: that is a session handed to
// the wrong person, which is the worst outcome this package can produce.
func TestFinishLoginRefusesWhenTheVaultRefreshesIntoAnotherIdentity(t *testing.T) {
	store, svc, auth := enrolFixture(t,
		&FakeRefresher{Sub: "user-1"},
		PlainBox{},
		stubIDVerifier{sub: "user-1"},
	)
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "the-sealed-token",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	// The enrolment is done; from here the verifier answers with a different
	// subject, as it would if the vault had been restored from another tenant's
	// backup.
	svc.verifier = stubIDVerifier{sub: "somebody-else"}

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	tokens, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  auth.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	})

	if err != ErrWebAuthnSubMismatch {
		t.Fatalf("err = %v, want ErrWebAuthnSubMismatch", err)
	}
	if tokens != nil {
		t.Fatalf("tokens = %+v were returned alongside the rejection", tokens)
	}
}

func TestFinishLoginRefusesAVaultItCannotOpen(t *testing.T) {
	store, svc, auth := enrolFixture(t, &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "the-sealed-token",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	svc.box = errBox{err: errStub("kms: AccessDeniedException on the token vault key")}

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  auth.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	}); err == nil {
		t.Fatal("FinishLogin returned tokens with a vault it could not decrypt")
	}
}

// An assertion signed by a different key is not this credential's assertion.
func TestFinishLoginRefusesAnAssertionFromAnotherAuthenticator(t *testing.T) {
	store, svc, auth := enrolFixture(t, &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "the-sealed-token",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	// Same credential id, different key: this is the shape of a stolen
	// credential id being replayed by an attacker who does not hold the secret.
	impostor := newVirtualAuthenticator(t, testOrigin, testRPID)
	impostor.credID = auth.credID

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, err := svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  impostor.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	}); err != ErrWebAuthnVerification {
		t.Fatalf("err = %v, want ErrWebAuthnVerification for an assertion signed by another key", err)
	}
}

// errStub is a plain error whose text reads like the one AWS returns.
type errStub string

func (e errStub) Error() string { return string(e) }
