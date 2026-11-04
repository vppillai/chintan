package handler_test

import (
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

// PATCH color: set, returned on the list and the detail, "none" clears it,
// anything else is a 400 — and none of it is an edit, so updated_at stays.
func TestPatchColorPersistsAndIsNotAnEdit(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Groceries", nil)
	if note.Color != "none" {
		t.Fatalf("new note colour = %q, want none", note.Color)
	}

	w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
		map[string]any{"version": note.Version, "color": "lavender"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH color: status = %d body = %s", w.Code, w.Body.String())
	}
	var saved handler.Note
	decodeInto(t, w, &saved)
	if saved.Color != "lavender" || saved.UpdatedAt != note.UpdatedAt || saved.Version == note.Version {
		t.Fatalf("saved = colour %q updated_at %q version %d; want lavender, %q unchanged, version moved",
			saved.Color, saved.UpdatedAt, saved.Version, note.UpdatedAt)
	}

	var page handler.Page[handler.Note]
	decodeInto(t, h.do(t, http.MethodGet, "/v1/notes", "user1", nil), &page)
	if len(page.Items) != 1 || page.Items[0].Color != "lavender" {
		t.Fatalf("listed = %+v, want the note in lavender", page.Items)
	}
	var detail handler.NoteDetail
	decodeInto(t, h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil), &detail)
	if detail.Color != "lavender" {
		t.Fatalf("detail colour = %q, want lavender", detail.Color)
	}

	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
		map[string]any{"version": saved.Version, "color": "teal"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH color=teal: status = %d, want 400", w.Code)
	}

	w = h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1",
		map[string]any{"version": saved.Version, "color": "none"})
	decodeInto(t, w, &saved)
	if w.Code != http.StatusOK || saved.Color != "none" {
		t.Fatalf("PATCH color=none: status = %d colour = %q, want 200 none", w.Code, saved.Color)
	}
}
