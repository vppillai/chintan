package middleware

import (
	"net/http"
)

// allowedRequestHeaders is the CORS header allowlist.
//
// X-User-ID was removed deliberately. Advertising it made a header the server
// trusted reachable from any browser; the server no longer reads it, and it
// must not be re-added here.
const allowedRequestHeaders = "Content-Type, Authorization, Idempotency-Key"

// CORS restricts cross-origin access to exactly one origin.
//
// There is no wildcard branch. Reflecting an arbitrary origin alongside
// Access-Control-Allow-Credentials: true defeats the same-origin policy
// entirely, so a "*" configuration is rejected at startup rather than handled
// here.
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" && origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Add("Vary", "Origin")
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", allowedRequestHeaders)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
