package handler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

// POST /v1/notes/{id}/clean is a hand-off: 202, one worker invocation naming
// the note and the mode, and nothing run inline.
func TestCleanNoteQueuesTheWorkerAndRunsNothingInline(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", map[string]any{"body": "the gutter leaks"})

	w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var queued handler.NoteCleanQueued
	decodeInto(t, w, &queued)
	if queued.Status != "queued" || queued.Mode != "structured" {
		t.Errorf("body = %+v, want queued in the default mode", queued)
	}
	if got := strings.Join(h.worker.calls, ","); got != "clean-note/user1/"+note.ID+"/structured" {
		t.Errorf("worker calls = %v", h.worker.calls)
	}

	// An explicit mode is honoured; a second request while one is queued is
	// fine.
	w = h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", map[string]any{"mode": "polished"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("second request: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &queued)
	if queued.Mode != "polished" {
		t.Errorf("mode = %q, want polished", queued.Mode)
	}
	if len(h.worker.calls) != 2 {
		t.Errorf("worker calls = %v, want two hand-offs", h.worker.calls)
	}

	// The note's own preference is the default once set. Each hand-off stamped
	// the row under its version, so the PATCH carries the version as it now is.
	var current handler.NoteDetail
	decodeInto(t, h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil), &current)
	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": current.Version, "cleaned_mode": "polished"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH cleaned_mode: status = %d body = %s", w.Code, w.Body.String())
	}
	w = h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil)
	decodeInto(t, w, &queued)
	if queued.Mode != "polished" {
		t.Errorf("mode after a stored preference = %q, want polished", queued.Mode)
	}
}

func TestCleanNoteRefusals(t *testing.T) {
	t.Run("archived note is 409", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		problemOf(t, w)
		if len(h.worker.calls) != 0 {
			t.Errorf("an archived note was handed to the worker: %v", h.worker.calls)
		}
	})
	t.Run("spend cap is 429 before the worker is invoked", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.spend.capped = true
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if p := problemOf(t, w); !strings.HasSuffix(p["type"].(string), "#spend-capped") {
			t.Errorf("type = %v, want the spend-capped URI so the client does not retry", p["type"])
		}
		if len(h.worker.calls) != 0 {
			t.Errorf("a capped request was handed to the worker: %v", h.worker.calls)
		}
	})
	t.Run("unknown mode is 400", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		for _, body := range []any{map[string]any{"mode": "faithful"}, map[string]any{"mode": "structured", "body": "x"}, []byte("not json")} {
			w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("body %v: status = %d, want 400 (%s)", body, w.Code, w.Body.String())
			}
		}
		if len(h.worker.calls) != 0 {
			t.Errorf("a refused request was handed to the worker: %v", h.worker.calls)
		}
	})
	t.Run("missing note is 404", func(t *testing.T) {
		h := newHarness(t)
		if w := h.do(t, http.MethodPost, "/v1/notes/missing/clean", "user1", nil); w.Code != http.StatusNotFound {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("a hand-off that fails is a 500, not a silent 202", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.worker.cleanErr = errors.New("lambda: AccessDeniedException")
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "AccessDenied") {
			t.Error("the infrastructure error text reached the client")
		}
	})
	t.Run("idempotency key replays the 202", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		key := [2]string{"Idempotency-Key", "clean-once-12345"}
		first := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil, key)
		second := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/clean", "user1", nil, key)
		if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
			t.Fatalf("statuses = %d, %d", first.Code, second.Code)
		}
		if second.Header().Get("Idempotency-Replayed") != "true" {
			t.Error("the second request was not a replay")
		}
		if len(h.worker.calls) != 1 {
			t.Errorf("worker calls = %v, want exactly one for a replayed key", h.worker.calls)
		}
	})
}

// The view on the wire: null until it exists, then body/mode/generated_at/
// stale, with error beside a kept body after a failed run. PATCH carries the
// preferences and nothing that writes the view.
func TestNoteDetailCarriesTheCleanedViewAndThePreferences(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", map[string]any{"body": "the gutter leaks"})

	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	var detail handler.NoteDetail
	decodeInto(t, w, &detail)
	if detail.Cleaned != nil {
		t.Errorf("cleaned = %+v on a note never cleaned, want null", detail.Cleaned)
	}
	if !strings.Contains(w.Body.String(), `"cleaned":null`) || !strings.Contains(w.Body.String(), `"auto_clean":false`) {
		t.Errorf("the detail must carry cleaned: null and auto_clean: false explicitly: %s", w.Body.String())
	}

	// Preferences round-trip through PATCH and come back on the Note.
	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": note.Version, "auto_clean": true, "cleaned_mode": "polished"})
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	var updated handler.Note
	decodeInto(t, w, &updated)
	if !updated.AutoClean || updated.CleanedMode != "polished" {
		t.Errorf("preferences not echoed: %+v", updated)
	}
	if w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": updated.Version, "cleaned_mode": "faithful"}); w.Code != http.StatusBadRequest {
		t.Errorf("cleaned_mode=faithful: status = %d, want 400", w.Code)
	}
	// There is no field that writes the view; an attempt is an unknown field.
	if w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": updated.Version, "cleaned": map[string]any{"body": "x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with cleaned: status = %d, want 400 (the view is read-only)", w.Code)
	}

	// As the worker leaves it.
	stored, err := h.store.GetNote(context.Background(), "user1", note.ID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	stored.CleanedBody, stored.CleanedMode, stored.CleanedAt = "# Roof\n\nThe gutter leaks.", model.NoteCleanStructured, "2026-01-01T00:00:00.000000000Z"
	if _, err := h.store.PutNote(context.Background(), "user1", stored); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	decodeInto(t, w, &detail)
	if detail.Cleaned == nil {
		t.Fatalf("cleaned is null with a stored view: %s", w.Body.String())
	}
	if detail.Cleaned.Body != "# Roof\n\nThe gutter leaks." || detail.Cleaned.Mode != "structured" || detail.Cleaned.GeneratedAt != "2026-01-01T00:00:00.000000000Z" || detail.Cleaned.Stale || detail.Cleaned.Error != nil {
		t.Errorf("cleaned = %+v", detail.Cleaned)
	}

	// An editor save marks it stale; the view itself is unchanged.
	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": detail.Version, "body": "the gutter leaks badly"})
	if w.Code != http.StatusOK {
		t.Fatalf("patch body: %d %s", w.Code, w.Body.String())
	}
	w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	decodeInto(t, w, &detail)
	if detail.Cleaned == nil || !detail.Cleaned.Stale || detail.Cleaned.Body != "# Roof\n\nThe gutter leaks." {
		t.Errorf("after a body edit cleaned = %+v, want the same view marked stale", detail.Cleaned)
	}
	if len(h.worker.calls) != 0 {
		t.Errorf("an editor save invoked the worker %v; auto_clean follows recordings, not saves", h.worker.calls)
	}

	// A failed run beside a kept view.
	stored, _ = h.store.GetNote(context.Background(), "user1", note.ID)
	stored.CleanedError = "the cleanup provider failed; try again"
	if _, err := h.store.PutNote(context.Background(), "user1", stored); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	decodeInto(t, w, &detail)
	if detail.Cleaned == nil || detail.Cleaned.Error == nil || *detail.Cleaned.Error != "the cleanup provider failed; try again" || detail.Cleaned.Body == "" {
		t.Errorf("after a failed run cleaned = %+v, want the kept view with error beside it", detail.Cleaned)
	}
}

// The list carries auto_clean and cleaned_mode truthfully (they are projected)
// and never the view itself.
func TestNotesListCarriesThePreferencesButNeverTheView(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", nil)
	h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"version": note.Version, "auto_clean": true})
	stored, _ := h.store.GetNote(context.Background(), "user1", note.ID)
	stored.CleanedBody, stored.CleanedMode, stored.CleanedAt = "# The view", model.NoteCleanStructured, "2026-01-01T00:00:00.000000000Z"
	if _, err := h.store.PutNote(context.Background(), "user1", stored); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	w := h.do(t, http.MethodGet, "/v1/notes", "user1", nil)
	if !strings.Contains(w.Body.String(), `"auto_clean":true`) {
		t.Errorf("the list does not carry auto_clean: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "The view") {
		t.Errorf("the list carries the cleaned view: %s", w.Body.String())
	}
}
