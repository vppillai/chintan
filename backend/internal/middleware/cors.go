package middleware

import (
	"net/http"
)

// allowedRequestHeaders is the CORS header allowlist this middleware
// advertises — which, behind API Gateway, is not what a browser is told.
//
// The gateway's CorsConfiguration (infrastructure/template.yaml, HttpApi)
// overwrites Access-Control-Allow-Headers and -Methods on every response that
// leaves it, this middleware's 204 to a preflight included; the template's
// list is the one the browser sees and the one that has to name every header
// the client sends. This list is what a run without the gateway advertises —
// local tests, a bare binary — and it is kept in step with the template's by
// hand; no test compares the two. The template's comment says why the OPTIONS
// route reaches the Lambda at all.
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
