package service

import (
	"context"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

func TestWebAuthnStatusAndDisable(t *testing.T) {
	store := repository.NewMemoryStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{})
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
	store := repository.NewMemoryStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{}, PlainBox{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.BeginLogin(context.Background())
	if err != ErrWebAuthnNotEnrolled {
		t.Fatalf("got %v", err)
	}
}

func TestFinishRegistrationRequiresRefresh(t *testing.T) {
	store := repository.NewMemoryStore()
	svc, err := NewWebAuthnService(store, "https://vppillai.github.io", "Chintan", &FakeRefresher{Sub: "user-1"}, PlainBox{})
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

func TestSubFromIDToken(t *testing.T) {
	sub, err := subFromIDToken("a.eyJzdWIiOiJ1c2VyLTEifQ.b")
	if err != nil || sub != "user-1" {
		t.Fatalf("sub=%q err=%v", sub, err)
	}
}
