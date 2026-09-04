package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/httperr"
)

// WithUserID puts a single-tenant identity in the context.
//
// This is in-process wiring only. Unlike a request header, a context value
// cannot be supplied by a remote caller.
func WithUserID(ctx context.Context, userID string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{UserID: userID, TenantID: userID})
}

// GetUserID returns the authenticated user id.
func GetUserID(ctx context.Context) (string, bool) {
	id, ok := auth.FromContext(ctx)
	if !ok || id.UserID == "" {
		return "", false
	}
	return id.UserID, true
}

// Auth verifies the bearer token and stores the resulting identity.
//
// There is deliberately no header-based identity path. An unverified
// `X-User-ID` header preferred over the token would let any caller holding one
// valid token act as any other user.
//
// A nil verifier fails closed: every request without an in-process identity is
// rejected. cmd/api refuses to start without one, so this can only be reached
// by a test that has not wired an identity.
func Auth(v auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.FromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}

			raw, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok || v == nil {
				httperr.Unauthorized(w, r, "authentication required")
				return
			}

			id, err := v.Verify(r.Context(), raw)
			if err != nil {
				// The specific claim that failed goes to logs, never to the
				// caller: a precise error is a probing oracle.
				httperr.Unauthorized(w, r, "authentication required")
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
