package service

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// A second device whose enrolment Cognito rejects must not take the first
// device with it.
//
// FinishRegistration used to store the new credential first and, when the
// refresh token was rejected, unwind with DeleteAllWebAuthnCredentials — which
// deleted the phone's working credential along with the laptop's failed one.
// The phone's next unlock then got a bare 401 with no hint why.
func TestAFailedSecondEnrolmentLeavesTheFirstDeviceEnrolled(t *testing.T) {
	refresher := &FakeRefresher{Sub: "user-1"}
	store, svc, phone := enrolFixture(t, refresher, PlainBox{}, stubIDVerifier{sub: "user-1"})
	ctx := context.Background()

	// The phone enrols and works.
	options, err := svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration (phone): %v", err)
	}
	if err := svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   phone.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "phone-refresh-token",
	}); err != nil {
		t.Fatalf("FinishRegistration (phone): %v", err)
	}
	vaultBefore, err := store.GetRefreshVault(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetRefreshVault after the phone enrolled: %v", err)
	}

	// The laptop tries with a refresh token Cognito has since revoked.
	laptop := newVirtualAuthenticator(t, testOrigin, testRPID)
	refresher.Err = errStub("NotAuthorizedException: Refresh Token has been revoked")
	options, err = svc.BeginRegistration(ctx, "user-1")
	if err != nil {
		t.Fatalf("BeginRegistration (laptop): %v", err)
	}
	err = svc.FinishRegistration(ctx, "user-1", &model.WebAuthnVerifyRequest{
		ChallengeID:  options.ChallengeID,
		Credential:   laptop.register(t, challengeOf(t, store, options.ChallengeID)),
		RefreshToken: "laptop-revoked-token",
	})
	if err == nil {
		t.Fatal("FinishRegistration (laptop) succeeded with a refresh token Cognito rejected")
	}

	// The phone is untouched: still enrolled, its vault unchanged.
	enrolled, err := svc.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !enrolled {
		t.Fatal("Status = false: the laptop's failed enrolment un-enrolled the phone")
	}
	page, err := store.ListWebAuthnCredentialsByUser(ctx, "user-1", repository.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListWebAuthnCredentialsByUser: %v", err)
	}
	if got := len(page.Items); got != 1 {
		t.Fatalf("%d credentials enrolled after the failed second enrolment, want exactly the phone's 1", got)
	}
	vaultAfter, err := store.GetRefreshVault(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetRefreshVault after the failed enrolment: %v", err)
	}
	if string(vaultAfter.Ciphertext) != string(vaultBefore.Ciphertext) {
		t.Fatal("the refresh vault was rewritten by an enrolment that failed")
	}
}
