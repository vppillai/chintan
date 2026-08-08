package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The v1 defect was reachable over HTTP, so the isolation property is asserted
// here as well as in the repository. Two distinct authenticated identities, one
// router, no shared visibility.

func isolationRouter(t *testing.T) http.Handler {
	t.Helper()
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)
	captureService := service.NewCaptureService(store, objects, nil, nil)
	return handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000", nil)
}

func createNoteAs(t *testing.T, router http.Handler, userID, title string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"title": title})
	req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("create note as %s: status=%d body=%s", userID, w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created note: %v (body=%s)", err, w.Body.String())
	}
	if created.ID == "" {
		t.Fatalf("created note has no id: %s", w.Body.String())
	}
	return created.ID
}

func requestAs(t *testing.T, router http.Handler, method, path, userID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(middleware.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestHTTPCrossTenantNoteIsNotReadable(t *testing.T) {
	router := isolationRouter(t)
	noteID := createNoteAs(t, router, "alice", "Alice private")

	w := requestAs(t, router, "GET", "/v1/notes/"+noteID, "bob", nil)
	if w.Code != 404 {
		t.Fatalf("bob read alice's note: status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Alice private") {
		t.Fatalf("response leaked alice's title: %s", w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotListed(t *testing.T) {
	router := isolationRouter(t)
	createNoteAs(t, router, "alice", "Alice private")

	w := requestAs(t, router, "GET", "/v1/notes", "bob", nil)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Alice private") {
		t.Fatalf("bob's list contained alice's note: %s", w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotDeletable(t *testing.T) {
	router := isolationRouter(t)
	noteID := createNoteAs(t, router, "alice", "Alice private")

	_ = requestAs(t, router, "DELETE", "/v1/notes/"+noteID, "bob", nil)

	w := requestAs(t, router, "GET", "/v1/notes/"+noteID, "alice", nil)
	if w.Code != 200 {
		t.Fatalf("alice's note was destroyed by bob: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotEditable(t *testing.T) {
	router := isolationRouter(t)
	noteID := createNoteAs(t, router, "alice", "Alice private")

	patch, _ := json.Marshal(map[string]any{"title": "Bob was here"})
	_ = requestAs(t, router, "PATCH", "/v1/notes/"+noteID, "bob", patch)

	w := requestAs(t, router, "GET", "/v1/notes/"+noteID, "alice", nil)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Bob was here") {
		t.Fatalf("bob edited alice's note: %s", w.Body.String())
	}
}

// Unauthenticated requests must not reach data handlers at all.
func TestHTTPDataRoutesRequireAuthentication(t *testing.T) {
	router := isolationRouter(t)
	for _, path := range []string{"/v1/notes", "/v1/settings", "/v1/captures"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s without credentials: status=%d", path, w.Code)
		}
	}
}
