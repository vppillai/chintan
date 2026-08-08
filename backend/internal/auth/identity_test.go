package auth

import (
	"context"
	"testing"
)

func TestIdentityRoundTripsThroughContext(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{UserID: "u1", TenantID: "u1"})
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("expected an identity in the context")
	}
	if got.UserID != "u1" || got.TenantID != "u1" {
		t.Fatalf("got %+v", got)
	}
}

func TestFromContextReportsAbsence(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("expected no identity in a bare context")
	}
}

// The context key is an unexported struct type, so no other package can collide
// with it or forge an identity by writing a string key.
func TestIdentityKeyIsNotForgeableByString(t *testing.T) {
	//lint:ignore SA1029 deliberately using a string key to prove it does not collide
	ctx := context.WithValue(context.Background(), "auth.contextKey", Identity{UserID: "attacker"}) //nolint:staticcheck
	if _, ok := FromContext(ctx); ok {
		t.Fatal("a string-keyed value must not satisfy FromContext")
	}
}
