package repository

import (
	"context"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

func TestWebAuthnStoreRoundTrip(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	chal := model.WebAuthnChallenge{
		ChallengeID: "chal1",
		SessionData: `{"challenge":"abc"}`,
		UserID:      "user-1",
		CreatedAt:   time.Now().Unix(),
		ExpiresAt:   time.Now().Add(5 * time.Minute).Unix(),
	}
	if err := store.PutWebAuthnChallenge(ctx, chal); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetWebAuthnChallenge(ctx, "chal1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionData != chal.SessionData {
		t.Fatalf("session=%q", got.SessionData)
	}
	_ = store.DeleteWebAuthnChallenge(ctx, "chal1")
	if _, err := store.GetWebAuthnChallenge(ctx, "chal1"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}

	cred := model.WebAuthnCredential{
		UserID: "user-1", CredentialID: "cred1", Credential: `{"id":"cred1"}`, SignCount: 1, CreatedAt: time.Now().Unix(),
	}
	if err := store.PutWebAuthnCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListWebAuthnCredentials(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list=%v err=%v", all, err)
	}
	byUser, err := store.ListWebAuthnCredentialsByUser(ctx, "user-1")
	if err != nil || len(byUser) != 1 {
		t.Fatalf("byUser=%v err=%v", byUser, err)
	}
	if err := store.DeleteAllWebAuthnCredentials(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	all, _ = store.ListWebAuthnCredentials(ctx)
	if len(all) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(all))
	}

	vault := model.RefreshVault{UserID: "user-1", Ciphertext: []byte("cipher"), UpdatedAt: time.Now().Unix()}
	if err := store.PutRefreshVault(ctx, vault); err != nil {
		t.Fatal(err)
	}
	gv, err := store.GetRefreshVault(ctx, "user-1")
	if err != nil || string(gv.Ciphertext) != "cipher" {
		t.Fatalf("vault=%v err=%v", gv, err)
	}
	_ = store.DeleteRefreshVault(ctx, "user-1")
	if _, err := store.GetRefreshVault(ctx, "user-1"); err != ErrNotFound {
		t.Fatalf("expected vault gone, got %v", err)
	}
}

func TestWebAuthnChallengeExpiry(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_ = store.PutWebAuthnChallenge(ctx, model.WebAuthnChallenge{
		ChallengeID: "old",
		SessionData: "{}",
		ExpiresAt:   time.Now().Add(-time.Minute).Unix(),
	})
	if _, err := store.GetWebAuthnChallenge(ctx, "old"); err != ErrNotFound {
		t.Fatalf("expired challenge should be not found, got %v", err)
	}
}
