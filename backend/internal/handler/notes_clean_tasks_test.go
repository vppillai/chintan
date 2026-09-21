package handler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

// A checklist cleans in tasks and nothing else; a plain note never in tasks.
// Both PATCH cleaned_mode and POST …/clean {mode} answer the mismatch with a
// fixed 400 sentence, and the wire's cleaned_mode reads tasks for a checklist.
func TestChecklistCleansInTasksModeOnly(t *testing.T) {
	h := newHarness(t)
	list := h.createNote(t, "user1", "Packing", map[string]any{"kind": "checklist", "body": "- [ ] passport"})
	if list.CleanedMode != "tasks" {
		t.Errorf("a checklist's cleaned_mode = %q, want tasks", list.CleanedMode)
	}

	// No mode: the checklist's own, which is tasks.
	w := h.do(t, http.MethodPost, "/v1/notes/"+list.ID+"/clean", "user1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST clean: status = %d body = %s", w.Code, w.Body.String())
	}
	var queued handler.NoteCleanQueued
	decodeInto(t, w, &queued)
	if queued.Mode != "tasks" || strings.Join(h.worker.calls, ",") != "clean-note/user1/"+list.ID+"/tasks" {
		t.Errorf("queued %+v, worker calls %v; want a tasks hand-off", queued, h.worker.calls)
	}

	const checklistSentence = "cleaned_mode must be tasks for a checklist"
	for _, req := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"clean in polished", http.MethodPost, "/v1/notes/" + list.ID + "/clean", map[string]any{"mode": "polished"}},
		{"clean in structured", http.MethodPost, "/v1/notes/" + list.ID + "/clean", map[string]any{"mode": "structured"}},
		{"patch cleaned_mode structured", http.MethodPatch, "/v1/notes/" + list.ID, map[string]any{"version": list.Version, "cleaned_mode": "structured"}},
	} {
		w := h.do(t, req.method, req.path, "user1", req.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", req.name, w.Code, w.Body.String())
			continue
		}
		if p := problemOf(t, w); p["detail"] != checklistSentence {
			t.Errorf("%s: detail = %v, want %q", req.name, p["detail"], checklistSentence)
		}
	}
	if len(h.worker.calls) != 1 {
		t.Errorf("a refused mode was handed to the worker: %v", h.worker.calls)
	}
	w = h.do(t, http.MethodPatch, "/v1/notes/"+list.ID, "user1", map[string]any{"version": list.Version, "cleaned_mode": "tasks"})
	if w.Code != http.StatusOK {
		t.Errorf("PATCH cleaned_mode=tasks on a checklist: status = %d body = %s", w.Code, w.Body.String())
	}

	// The other way round.
	plain := h.createNote(t, "user1", "Roof", map[string]any{"body": "the gutter leaks"})
	const plainSentence = "cleaned_mode must be polished or structured"
	for _, req := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"clean in tasks", http.MethodPost, "/v1/notes/" + plain.ID + "/clean", map[string]any{"mode": "tasks"}},
		{"patch cleaned_mode tasks", http.MethodPatch, "/v1/notes/" + plain.ID, map[string]any{"version": plain.Version, "cleaned_mode": "tasks"}},
	} {
		w := h.do(t, req.method, req.path, "user1", req.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", req.name, w.Code, w.Body.String())
			continue
		}
		if p := problemOf(t, w); p["detail"] != plainSentence {
			t.Errorf("%s: detail = %v, want %q", req.name, p["detail"], plainSentence)
		}
	}

	// Becoming a checklist and taking tasks in one PATCH is fine, and the
	// switch back reads the default again rather than a tasks preference the
	// note can no longer run.
	w = h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "checklist", "cleaned_mode": "tasks", "body": "- [ ] the gutter leaks"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH kind+cleaned_mode: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "checklist" || plain.CleanedMode != "tasks" {
		t.Errorf("after the switch: kind %q cleaned_mode %q", plain.Kind, plain.CleanedMode)
	}
	w = h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "note", "body": "the gutter leaks"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH back to note: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "note" || plain.CleanedMode != "structured" {
		t.Errorf("after switching back: kind %q cleaned_mode %q, want note in the default mode", plain.Kind, plain.CleanedMode)
	}
	decodeInto(t, h.do(t, http.MethodPost, "/v1/notes/"+plain.ID+"/clean", "user1", nil), &queued)
	if queued.Mode != "structured" {
		t.Errorf("an unspecified clean of the switched-back note queued %q, want structured", queued.Mode)
	}
}
