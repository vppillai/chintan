package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// The composition root over fakes: the same build main runs, with the fake
// table and the in-memory bucket in place of AWS, serves the liveness probe
// and a note's round trip. Nothing here reads the environment; a missing
// wiring in build fails here rather than on the first request after a deploy.
func TestBuildServesTheAPIOverFakes(t *testing.T) {
	router, readiness := build(deps{
		store:         dynamofake.NewStore(),
		objects:       memory.NewObjects(),
		usage:         memory.NewUsage(),
		allowedOrigin: "http://localhost:3000",
	})
	if readiness == nil {
		t.Fatal("build returned no readiness service")
	}

	do := func(method, path, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		// The verifier is nil and fails closed; the test stands in for it the
		// way the handler tests do, with the identity already on the context.
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if w := do(http.MethodGet, "/v1/health", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/health = %d: %s", w.Code, w.Body)
	}
	if w := do(http.MethodPost, "/v1/notes", `{"title":"Wired"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/notes = %d: %s", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/v1/notes", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Wired"`) {
		t.Fatalf("GET /v1/notes = %d: %s", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/v1/usage", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/usage = %d: %s; the usage store is not wired", w.Code, w.Body)
	}
	// The services whose Deps field answers 503 when nil: each must be wired.
	if w := do(http.MethodGet, "/v1/search?q=x", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/search = %d: %s; search is not wired", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/v1/tags", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /v1/tags = %d: %s; tags are not wired", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/v1/export/e_1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/export/e_1 = %d: %s; export is not wired", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/v1/ask/x", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/ask/x = %d: %s; ask is not wired", w.Code, w.Body)
	}
}
