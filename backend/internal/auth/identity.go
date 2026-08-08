// Package auth verifies Cognito-issued JWTs and carries the resulting identity.
//
// Verification happens in-process even though API Gateway also runs a JWT
// authorizer. The gateway is not guaranteed to be the only ingress, and it does
// not strip request headers — the v1 build trusted an `X-User-ID` header over
// the token precisely because it assumed otherwise.
package auth

import "context"

// Identity is the authenticated caller.
//
// TenantID is the data-ownership boundary and is what storage keys are derived
// from. In v2 it always equals UserID; making Chintan multi-user means
// populating it from a claim instead of the subject, with no storage or API
// change. Nothing below this package may key data on UserID directly.
type Identity struct {
	UserID   string
	TenantID string
}

type contextKey struct{}

// WithIdentity returns a context carrying id.
//
// Callers outside this package use it only in tests and in the middleware.
// It is safe to export: a context value cannot be set by a remote caller,
// which is the property the deleted header path lacked.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the identity stored by WithIdentity.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}
