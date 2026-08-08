package service

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// Cognito only issues a new refresh token when rotation is on. When it does
// not, the old one is still the live credential and must be carried forward —
// dropping it leaves the vault holding an empty string and the user locked out
// of biometric unlock at the next attempt.
func TestFakeRefresherCarriesTheOldRefreshTokenForwardWhenNoneIsIssued(t *testing.T) {
	refresher := &FakeRefresher{Sub: "user-1"}

	tokens, err := refresher.Refresh(context.Background(), "the-existing-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken != "the-existing-token" {
		t.Fatalf("refresh token = %q, want the one that was presented carried forward", tokens.RefreshToken)
	}
	if tokens.IDToken == "" || tokens.AccessToken == "" {
		t.Fatalf("tokens = %+v, want a complete set", tokens)
	}
	if tokens.TokenType != "Bearer" {
		t.Errorf("token type = %q, want Bearer", tokens.TokenType)
	}
}

func TestFakeRefresherPrefersARotatedRefreshToken(t *testing.T) {
	refresher := &FakeRefresher{Sub: "user-1", RefreshToken: "rotated"}

	tokens, err := refresher.Refresh(context.Background(), "the-existing-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken != "rotated" {
		t.Fatalf("refresh token = %q, want the rotated one", tokens.RefreshToken)
	}
}

func TestFakeRefresherPassesItsFailureThrough(t *testing.T) {
	boom := errors.New("NotAuthorizedException: Refresh Token has been revoked")
	refresher := &FakeRefresher{Err: boom}

	tokens, err := refresher.Refresh(context.Background(), "whatever")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the configured failure", err)
	}
	if tokens.IDToken != "" || tokens.RefreshToken != "" {
		t.Fatalf("tokens = %+v were returned alongside the failure", tokens)
	}
}

func TestPlainBoxRoundTripsAPayload(t *testing.T) {
	ctx := context.Background()
	plaintext := []byte("a-cognito-refresh-token")

	sealed, err := PlainBox{}.Seal(ctx, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := PlainBox{}.Open(ctx, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened %q, want %q", opened, plaintext)
	}
}

// The box copies rather than aliasing. A vault whose ciphertext shares backing
// storage with the caller's buffer changes underneath the store when the caller
// reuses it.
func TestPlainBoxDoesNotAliasTheCallersBuffer(t *testing.T) {
	ctx := context.Background()
	plaintext := []byte("token")

	sealed, err := PlainBox{}.Seal(ctx, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	plaintext[0] = 'X'

	if string(sealed) != "token" {
		t.Fatalf("sealed value became %q when the caller reused its buffer; the box aliased it", sealed)
	}
}

// SealBox and TokenRefresher are the seams the API binary depends on instead of
// depending on the crypto and on Cognito directly. These assertions fail at
// compile time if a production implementation stops satisfying them.
//
// AESVaultBox is the only production SealBox. PlainBox is asserted here too,
// but it is defined in plainbox_test.go and exists only in the test binary —
// see the comment there for why that placement is load-bearing.
func TestProductionSealBoxAndRefresherSatisfyTheirSeams(t *testing.T) {
	var (
		_ SealBox        = PlainBox{}
		_ SealBox        = (*AESVaultBox)(nil)
		_ TokenRefresher = (*FakeRefresher)(nil)
		_ TokenRefresher = (*CognitoRefresher)(nil)
	)
}
