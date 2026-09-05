package middleware

import (
	"net/http"
)

// CORS marks a response as readable by exactly one origin.
//
// Preflights are not answered here. API Gateway's CorsConfiguration
// (infrastructure/template.yaml, HttpApi) answers every OPTIONS request itself,
// before any route or authorizer, and that block is the single list of headers
// and methods a browser is told. Until 2026-09 this middleware carried a second
// list — three headers to the gateway's seven — and answered preflights with a
// 204, which only ever ran because an explicit "OPTIONS /{proxy+}" route sent
// them to the Lambda; the route is gone with the list. What remains is the part
// the actual response needs: which origin may read it, with credentials. The
// gateway sets these on its own responses too and overrides what the
// integration returns, so this is the answer for a local run and for any path
// that does not have the gateway in front.
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
			next.ServeHTTP(w, r)
		})
	}
}
