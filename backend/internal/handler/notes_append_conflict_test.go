package handler_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
)

// The 409 a body save gets while a recording is being appended is the
// version-conflict shape plus two things the client keys on: `reason`, so it
// repeats the same save instead of rebasing on current_version, and a
// Retry-After saying when. Both are part of the contract with the frontend.
func TestPatchDuringAnAppendAnswers409WithReasonAndRetryAfter(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Being dictated into", nil)
	stamped, err := h.store.StampNoteAppend(context.Background(), "user1", note.ID, "cap1", note.Version, time.Now())
	if err != nil {
		t.Fatalf("StampNoteAppend: %v", err)
	}

	w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
		"version": note.Version,
		"body":    "typed while dictating",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	p := problemOf(t, w)
	if p["reason"] != httperr.ReasonAppendInProgress {
		t.Errorf("reason = %v, want %q", p["reason"], httperr.ReasonAppendInProgress)
	}
	if current, _ := p["current_version"].(float64); int64(current) != stamped.Version {
		t.Errorf("current_version = %v, want the stamped version %d", p["current_version"], stamped.Version)
	}

	// A title-only save is not a body write and is answered normally.
	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
		"version": stamped.Version,
		"title":   "Retitled during the append",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("title save during an append: status = %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "" {
		t.Error("Retry-After on a 200")
	}
}
