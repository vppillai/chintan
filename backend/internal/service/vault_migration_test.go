package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Moving the vault off the customer-managed KMS key is a one-way break: an
// entry sealed by the CMK cannot be opened by the AES box that replaced it.
// With the entry left in place, biometric unlock fails on every attempt with an
// opaque "open vault" error and there is no way out of it from the app.
//
// So an entry that can never be opened is discarded and the user is told to
// enrol again — once.
func TestAVaultSealedByTheRetiredKeyIsDiscardedSoTheUserCanReEnrol(t *testing.T) {
	store, svc, auth := enrolFixture(t, &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "sealed-by-the-old-kms-key",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if _, err := store.GetRefreshVault(ctx, "user-1"); err != nil {
		t.Fatalf("the fixture did not leave a vault to migrate: %v", err)
	}

	// What the AES box reports for a blob the retired CMK sealed.
	svc.box = errBox{err: fmt.Errorf("%w: not a CVK1 blob", ErrVaultUnreadable)}

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  auth.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	})
	if !errors.Is(err, ErrWebAuthnReEnrolRequired) {
		t.Fatalf("FinishLogin err = %v, want ErrWebAuthnReEnrolRequired so the client can prompt for re-enrolment", err)
	}

	if _, err := store.GetRefreshVault(ctx, "user-1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("the unopenable vault entry survived; every later unlock fails the same way and the user is stuck")
	}
}

// The other half of that behaviour, and the more important one: a decrypt that
// failed for a reason that might not recur must NOT destroy the vault. An
// AccessDenied or a throttle is transient; deleting on it would turn a blip
// into a forced re-enrolment.
func TestATransientVaultFailureLeavesTheVaultAlone(t *testing.T) {
	store, svc, auth := enrolFixture(t, &FakeRefresher{Sub: "user-1"}, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	regOptions, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  regOptions.ChallengeID,
		Credential:   auth.register(t, challengeOf(t, store, regOptions.ChallengeID)),
		RefreshToken: "still-good",
	}); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	svc.box = errBox{err: errStub("ssm: ThrottlingException reading the vault key")}

	loginOptions, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = svc.FinishLogin(ctx, &model.WebAuthnVerifyRequest{
		ChallengeID: loginOptions.ChallengeID,
		Credential:  auth.assert(t, challengeOf(t, store, loginOptions.ChallengeID), []byte("user-1")),
	})
	if err == nil {
		t.Fatal("FinishLogin returned tokens with a vault it could not open")
	}
	if errors.Is(err, ErrWebAuthnReEnrolRequired) {
		t.Fatal("a transient failure was reported as needing re-enrolment")
	}

	if _, err := store.GetRefreshVault(ctx, "user-1"); err != nil {
		t.Fatalf("a transient decrypt failure destroyed the vault: %v", err)
	}
}
