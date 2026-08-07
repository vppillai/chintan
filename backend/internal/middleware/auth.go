package middleware

import (
	"context"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
)

type contextKey string

const userIDKey contextKey = "userID"

// WithUserID adds a userID to the context (for testing)
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// GetUserID extracts userID from context
func GetUserID(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey).(string)
	return userID, ok
}

// Auth middleware extracts userID from JWT claims or context and adds to request context
func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For now, check context first (for tests)
		if _, ok := GetUserID(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}

		// TODO: Production JWT parsing would go here
		// For now, check for a test header
		userID := r.Header.Get("X-User-ID")
		if userID != "" {
			ctx := WithUserID(r.Context(), userID)
			r = r.WithContext(ctx)
			next.ServeHTTP(w, r)
			return
		}

		httperr.Unauthorized(w, "authentication required")
	})
}
