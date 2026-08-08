package handler_test

import (
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

const testKey = "idem-key-0000001"

// The defect this closes: a double-tap on a flaky mobile link creating two
// notes.
func TestIdempotentReplayReturnsTheOriginalResponse(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"title": "Only once"}

	first := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if first.Code != http.StatusCreated {
		t.Fatalf("first: status = %d body = %s", first.Code, first.Body.String())
	}
	second := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if second.Code != first.Code {
		t.Fatalf("replay status = %d, want %d", second.Code, first.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs:\n first=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("a replay is not marked as one")
	}

	// One note, not two.
	notes := listNotes(t, h, "user1", "/v1/notes")
	count := 0
	for _, n := range notes {
		if n.Title == "Only once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the note was created %d times", count)
	}
}

// Replaying a key with a different body would return the response of a request
// the caller never made.
func TestIdempotencyKeyIsBoundToItsRequest(t *testing.T) {
	h := newHarness(t)

	first := h.do(t, http.MethodPost, "/v1/notes", "user1",
		map[string]any{"title": "First"}, [2]string{"Idempotency-Key", testKey})
	if first.Code != http.StatusCreated {
		t.Fatalf("first: status = %d", first.Code)
	}

	second := h.do(t, http.MethodPost, "/v1/notes", "user1",
		map[string]any{"title": "Different"}, [2]string{"Idempotency-Key", testKey})
	if second.Code != http.StatusConflict {
		t.Fatalf("reused key with a new body: status = %d, want 409", second.Code)
	}
	problemOf(t, second)
}

// A key belongs to the tenant that claimed it.
func TestIdempotencyKeysAreTenantScoped(t *testing.T) {
	h := newHarness(t)

	alice := h.do(t, http.MethodPost, "/v1/notes", "alice",
		map[string]any{"title": "Alice note"}, [2]string{"Idempotency-Key", testKey})
	if alice.Code != http.StatusCreated {
		t.Fatalf("alice: status = %d", alice.Code)
	}

	bob := h.do(t, http.MethodPost, "/v1/notes", "bob",
		map[string]any{"title": "Bob note"}, [2]string{"Idempotency-Key", testKey})
	if bob.Code != http.StatusCreated {
		t.Fatalf("bob reusing alice's key: status = %d body = %s", bob.Code, bob.Body.String())
	}
	var note handler.Note
	decodeInto(t, bob, &note)
	if note.Title != "Bob note" {
		t.Fatalf("bob received alice's response: %+v", note)
	}
}

func TestIdempotencyKeyIsBounded(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"short", string(make([]byte, 200))} {
		w := h.do(t, http.MethodPost, "/v1/notes", "user1",
			map[string]any{"title": "x"}, [2]string{"Idempotency-Key", key})
		if w.Code != http.StatusBadRequest {
			t.Errorf("key of length %d: status = %d, want 400", len(key), w.Code)
		}
	}
}

// A request without the header still works. The header is optional in the
// contract and refusing an unkeyed POST would break every caller.
func TestUnkeyedPostsStillWork(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/v1/notes", "user1", map[string]any{"title": "No key"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
