package handler_test

import (
	"net/http"
	"testing"
)

// A checklist has no cleaned view (2026-09-27, PR-D4): POST …/clean in any
// mode that is one, or in none, and PATCH cleaned_mode both answer one fixed
// 400 sentence, nothing is handed to the worker, and the wire carries no
// cleaned_mode for it. The deleted "tasks" mode is an unknown mode for every
// note and is refused as one before the kind is looked at.
func TestChecklistHasNoCleanedView(t *testing.T) {
	h := newHarness(t)
	list := h.createNote(t, "user1", "Packing", map[string]any{"kind": "checklist", "body": "- [ ] passport"})
	if list.CleanedMode != "" {
		t.Errorf("a checklist's cleaned_mode = %q, want none", list.CleanedMode)
	}

	const checklistSentence = "a checklist has no cleaned view"
	for _, req := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"clean unspecified", http.MethodPost, "/v1/notes/" + list.ID + "/clean", nil},
		{"clean in polished", http.MethodPost, "/v1/notes/" + list.ID + "/clean", map[string]any{"mode": "polished"}},
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
	if len(h.worker.calls) != 0 {
		t.Errorf("a refused clean was handed to the worker: %v", h.worker.calls)
	}

	// The other way round: a plain note takes polished or structured only.
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

	// Converting a note that had chosen polished into a checklist keeps the
	// row's preference but stops reporting it; converting back reads it again.
	w := h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1", map[string]any{"version": plain.Version, "cleaned_mode": "polished"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH cleaned_mode: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	w = h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "checklist", "body": "- [ ] the gutter leaks"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH kind: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "checklist" || plain.CleanedMode != "polished" {
		t.Errorf("after the switch: kind %q cleaned_mode %q; the preference is kept for the switch back", plain.Kind, plain.CleanedMode)
	}
	w = h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "note", "body": "the gutter leaks"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH back to note: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "note" || plain.CleanedMode != "polished" {
		t.Errorf("after switching back: kind %q cleaned_mode %q, want the polished preference again", plain.Kind, plain.CleanedMode)
	}
}
