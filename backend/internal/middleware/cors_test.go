package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func corsResponse(t *testing.T, allowed, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	h := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, "/v1/notes", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// Advertising X-User-ID is what made the impersonation header reachable from a
// browser. It must never reappear in the allowlist.
func TestCORSDoesNotAdvertiseUserIDHeader(t *testing.T) {
	w := corsResponse(t, "https://app.example", "https://app.example", http.MethodGet)
	got := w.Header().Get("Access-Control-Allow-Headers")
	if strings.Contains(strings.ToLower(got), "x-user-id") {
		t.Fatalf("Access-Control-Allow-Headers still advertises X-User-ID: %q", got)
	}
	if !strings.Contains(got, "Authorization") {
		t.Fatalf("Authorization must remain allowed: %q", got)
	}
}

func TestCORSAllowsOnlyTheConfiguredOrigin(t *testing.T) {
	w := corsResponse(t, "https://app.example", "https://app.example", http.MethodGet)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow-origin=%q", got)
	}
	if got := w.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary must include Origin, got %q", got)
	}
}

func TestCORSRejectsForeignOrigin(t *testing.T) {
	w := corsResponse(t, "https://app.example", "https://evil.example", http.MethodGet)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was allowed: %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("credentials advertised to a foreign origin: %q", got)
	}
}

// The wildcard branch is gone: "*" is not an origin, so nothing matches it.
func TestCORSWildcardConfigurationReflectsNothing(t *testing.T) {
	w := corsResponse(t, "*", "https://evil.example", http.MethodGet)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("wildcard config reflected an arbitrary origin: %q", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	w := corsResponse(t, "https://app.example", "https://app.example", http.MethodOptions)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
}
