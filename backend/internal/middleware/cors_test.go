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
		w.Header().Set("X-Reached-Handler", r.Method)
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

// The allowed headers and methods live in one place — the gateway's
// CorsConfiguration in infrastructure/template.yaml — so the middleware
// declares neither. A second list here is how the two once disagreed (three
// headers against seven), and how X-User-ID, a header the server trusted, was
// once advertised to every browser.
func TestCORSDeclaresNoHeaderOrMethodList(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		w := corsResponse(t, "https://app.example", "https://app.example", method)
		for _, h := range []string{"Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Access-Control-Max-Age"} {
			if got := w.Header().Get(h); got != "" {
				t.Errorf("%s: %s = %q; the list belongs to the gateway's CorsConfiguration alone", method, h, got)
			}
		}
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

// A preflight that reaches the Lambda is not answered by the middleware: in
// front of the gateway none arrives (CorsConfiguration answers them), and
// without the gateway the request goes on to the handler like any other, so
// a local run can see what the router makes of it rather than a 204 that hides
// it.
func TestCORSPassesAPreflightThroughToTheHandler(t *testing.T) {
	w := corsResponse(t, "https://app.example", "https://app.example", http.MethodOptions)
	if got := w.Header().Get("X-Reached-Handler"); got != http.MethodOptions {
		t.Fatalf("the handler did not see the OPTIONS request (reached=%q, status=%d)", got, w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("allow-origin on the passed-through preflight = %q", got)
	}
}
