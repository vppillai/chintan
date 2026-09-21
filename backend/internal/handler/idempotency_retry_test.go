package handler_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// failNextPutNote makes the first PutNote fail, which surfaces from POST
// /v1/notes as a 500 — the transient infrastructure fault a client retries.
type failNextPutNote struct {
	repository.Store
	mu     sync.Mutex
	failed bool
}

func (s *failNextPutNote) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	s.mu.Unlock()
	if first {
		return model.NoteIndex{}, errors.New("induced store failure")
	}
	return s.Store.PutNote(ctx, tenantID, n)
}

// A 5xx must not pin the key. Before this, the claim taken by the failed request
// stayed unfinished for the record's 24-hour TTL, so every retry with the same
// key — which is what the frontend does, deliberately — was answered with 409
// "an identical request is still in flight" for a day.
func TestARetryAfterA5xxWithTheSameIdempotencyKeySucceeds(t *testing.T) {
	h := newHarness(t, func(deps *handler.Deps, h *harness) {
		flaky := &failNextPutNote{Store: h.store}
		deps.Notes = service.NewNotesService(flaky, h.objects)
	})
	body := map[string]any{"title": "Eventually"}

	first := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first: status = %d, want 500 from the induced failure; body = %s", first.Code, first.Body.String())
	}

	second := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if second.Code != http.StatusCreated {
		t.Fatalf("retry after a 5xx: status = %d, want 201; body = %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("the retry was served as a replay, but there was no recorded response to replay")
	}

	// And the successful response is now the one a further replay returns.
	third := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if third.Code != http.StatusCreated || third.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay after the successful retry: status = %d replayed = %q, want 201 and true",
			third.Code, third.Header().Get("Idempotency-Replayed"))
	}
	if third.Body.String() != second.Body.String() {
		t.Fatalf("replay body differs:\n retry=%s\nreplay=%s", second.Body.String(), third.Body.String())
	}

	count := 0
	for _, n := range listNotes(t, h, "user1", "/v1/notes") {
		if n.Title == "Eventually" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the note was created %d times, want 1", count)
	}
}

// A 429 is not settled. The uploader keys every attempt on the recording's
// localId, so recording a spend-capped 429 answered the "Resend tomorrow" the
// UI promises with the recorded 429, Idempotency-Replayed: true, for a day
// (review 2026-09-21, T13).
func TestATransient429IsNotPinnedToTheIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"content_type": "audio/webm", "duration_ms": 1000, "size_bytes": 1000}

	h.spend.capped = true
	first := h.do(t, http.MethodPost, "/v1/captures", "user1", body, [2]string{"Idempotency-Key", "local-id-recording-1"})
	if first.Code != http.StatusTooManyRequests {
		t.Fatalf("capped: status = %d, want 429: %s", first.Code, first.Body.String())
	}

	// Midnight UTC: the cap has reset. The user taps Resend.
	h.spend.capped = false
	second := h.do(t, http.MethodPost, "/v1/captures", "user1", body, [2]string{"Idempotency-Key", "local-id-recording-1"})
	if second.Code != http.StatusCreated {
		t.Fatalf("after the cap reset: status = %d (replayed=%q), want 201: %s", second.Code, second.Header().Get("Idempotency-Replayed"), second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("the resend was served as a replay, but a 429 is nothing to replay")
	}
}

// A 409 is the same: the state it names — archived, in flight, appending —
// is exactly what a later attempt expects to have moved on.
func TestATransient409IsNotPinnedToTheIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", map[string]any{"body": "the gutter leaks"})
	if w := h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive: status = %d", w.Code)
	}

	first := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil, [2]string{"Idempotency-Key", testKey})
	if first.Code != http.StatusConflict {
		t.Fatalf("clean an archived note: status = %d, want 409: %s", first.Code, first.Body.String())
	}
	if w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/restore", "user1", nil); w.Code != http.StatusOK {
		t.Fatalf("restore: status = %d body = %s", w.Code, w.Body.String())
	}

	second := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil, [2]string{"Idempotency-Key", testKey})
	if second.Code != http.StatusAccepted || second.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatalf("clean after the restore: status = %d replayed = %q, want 202 and no replay: %s",
			second.Code, second.Header().Get("Idempotency-Replayed"), second.Body.String())
	}
}

// A settled 4xx replays as what it was. The middleware used to force
// Content-Type: application/json onto every replay, so a recorded problem body
// went out under the wrong type.
func TestAReplayedProblemKeepsItsContentType(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"title": ""}

	first := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if first.Code != http.StatusBadRequest {
		t.Fatalf("first: status = %d, want 400: %s", first.Code, first.Body.String())
	}
	second := h.do(t, http.MethodPost, "/v1/notes", "user1", body, [2]string{"Idempotency-Key", testKey})
	if second.Code != http.StatusBadRequest || second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: status = %d replayed = %q, want the recorded 400", second.Code, second.Header().Get("Idempotency-Replayed"))
	}
	problemOf(t, second)
	if second.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs:\n first=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
}
