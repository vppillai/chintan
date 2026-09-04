package handler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

// Tenant isolation is asserted here as well as in the repository: a leak in the
// middleware or the handlers is reachable over HTTP and would never be seen by
// a store-level test. Two distinct authenticated identities, one router, no
// shared visibility.

func TestHTTPCrossTenantNoteIsNotReadable(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "alice", "Alice private", nil)

	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "bob", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob read alice's note: status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Alice private") {
		t.Fatalf("response leaked alice's title: %s", w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotListed(t *testing.T) {
	h := newHarness(t)
	h.createNote(t, "alice", "Alice private", nil)

	w := h.do(t, http.MethodGet, "/v1/notes", "bob", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Alice private") {
		t.Fatalf("bob's list contained alice's note: %s", w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotDeletable(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "alice", "Alice private", nil)

	_ = h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "bob", nil)

	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "alice", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("alice's note was destroyed by bob: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHTTPCrossTenantNoteIsNotEditable(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "alice", "Alice private", nil)

	// The version alice actually holds, so the write is refused for being
	// bob's rather than for being stale — the isolation property, not the
	// concurrency one.
	_ = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "bob", map[string]any{
		"version": note.Version,
		"title":   "Bob was here",
	})

	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "alice", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Bob was here") {
		t.Fatalf("bob edited alice's note: %s", w.Body.String())
	}
}

// A capture is addressed by id alone, so it is the other way a tenant boundary
// could be crossed.
func TestHTTPCrossTenantCaptureIsNotReachable(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "alice", "Alice private", nil)

	w := h.do(t, http.MethodPost, "/v1/captures", "alice", map[string]any{
		"content_type": "audio/webm",
		"note_id":      note.ID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("alice could not begin a capture: status=%d body=%s", w.Code, w.Body.String())
	}
	var created handler.CaptureCreated
	decodeInto(t, w, &created)

	for _, path := range []string{
		"/v1/captures/" + created.Capture.ID,
		"/v1/captures/" + created.Capture.ID + "/download?kind=audio",
	} {
		got := h.do(t, http.MethodGet, path, "bob", nil)
		if got.Code != http.StatusNotFound {
			t.Errorf("bob reached %s: status=%d body=%s", path, got.Code, got.Body.String())
		}
	}
	if got := h.do(t, http.MethodPost, "/v1/captures/"+created.Capture.ID+"/retry", "bob", nil); got.Code != http.StatusNotFound {
		t.Errorf("bob retried alice's capture: status=%d", got.Code)
	}
}

// Unauthenticated requests must not reach data handlers at all.
func TestHTTPDataRoutesRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/v1/notes", "/v1/settings", "/v1/captures", "/v1/tags", "/v1/search?q=x",
	} {
		w := h.do(t, http.MethodGet, path, "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without credentials: status=%d", path, w.Code)
		}
	}
}
