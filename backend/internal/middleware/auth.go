package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

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

// Auth middleware extracts userID from JWT claims or context and adds to request context.
// Signature verification is performed by API Gateway's JWT authorizer; this only reads `sub`.
func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := GetUserID(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}

		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			userID = userIDFromBearer(r.Header.Get("Authorization"))
		}
		if userID == "" {
			httperr.Unauthorized(w, "authentication required")
			return
		}

		next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), userID)))
	})
}

func userIDFromBearer(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	token := strings.TrimSpace(header[len(prefix):])
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some issuers pad; try standard encoding as a fallback.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}
