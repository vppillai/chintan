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
