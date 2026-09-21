package handler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

// kind is on every Note, "note" unless the client said otherwise, and is set
// on POST and PATCH. The storage default is "" so rows from before the field
// need no backfill; the wire never shows that.
func TestNoteKindOnTheWire(t *testing.T) {
	h := newHarness(t)

	plain := h.createNote(t, "user1", "Reading list", nil)
	if plain.Kind != "note" {
		t.Errorf("a new note's kind = %q, want note", plain.Kind)
	}
	list := h.createNote(t, "user1", "Packing", map[string]any{"kind": "checklist", "body": "- [ ] passport"})
	if list.Kind != "checklist" {
		t.Errorf("created with kind=checklist, got %q", list.Kind)
	}
	var detail handler.NoteDetail
	decodeInto(t, h.do(t, http.MethodGet, "/v1/notes/"+list.ID, "user1", nil), &detail)
	if detail.Kind != "checklist" || detail.Body != "- [ ] passport" {
		t.Errorf("detail = kind %q body %q", detail.Kind, detail.Body)
	}

	// PATCH switches it either way; the body is the client's to convert.
	w := h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "checklist", "body": "- [ ] read the paper"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH kind=checklist: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "checklist" {
		t.Errorf("after PATCH kind = %q, want checklist", plain.Kind)
	}
	w = h.do(t, http.MethodPatch, "/v1/notes/"+plain.ID, "user1",
		map[string]any{"version": plain.Version, "kind": "note", "body": "read the paper"})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH kind=note: status = %d body = %s", w.Code, w.Body.String())
	}
	decodeInto(t, w, &plain)
	if plain.Kind != "note" {
		t.Errorf("after PATCH back, kind = %q, want note", plain.Kind)
	}

	// The one refusal, with the same sentence on both writes and the filter.
	for _, req := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/v1/notes", map[string]any{"title": "x", "kind": "todo"}},
		{http.MethodPatch, "/v1/notes/" + plain.ID, map[string]any{"version": plain.Version, "kind": ""}},
		{http.MethodGet, "/v1/notes?kind=todo", nil},
	} {
		w := h.do(t, req.method, req.path, "user1", req.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400 (%s)", req.method, req.path, w.Code, w.Body.String())
			continue
		}
		if p := problemOf(t, w); p["detail"] != "kind must be note or checklist" {
			t.Errorf("%s %s: detail = %v", req.method, req.path, p["detail"])
		}
	}
}

// ?kind= filters the page the way ?tag= does, and the offline corpus rows
// (include=search_text) carry kind like every other Note.
func TestNotesListFiltersByKind(t *testing.T) {
	h := newHarness(t)
	h.createNote(t, "user1", "Reading list", map[string]any{"body": "papers to read"})
	packing := h.createNote(t, "user1", "Packing", map[string]any{"kind": "checklist", "body": "- [ ] passport"})
	groceries := h.createNote(t, "user1", "Groceries", map[string]any{"kind": "checklist", "body": "- [ ] milk", "tags": []string{"home"}})

	ids := func(notes []handler.Note) string {
		out := make([]string, 0, len(notes))
		for _, n := range notes {
			out = append(out, n.Title)
		}
		return strings.Join(out, ",")
	}
	if got := ids(listNotes(t, h, "user1", "/v1/notes?kind=checklist")); got != "Groceries,Packing" {
		t.Errorf("kind=checklist → %s, want the two checklists newest first", got)
	}
	if got := ids(listNotes(t, h, "user1", "/v1/notes?kind=note")); got != "Reading list" {
		t.Errorf("kind=note → %s", got)
	}
	// Both filters together, as the Home screen's chips compose them.
	if got := ids(listNotes(t, h, "user1", "/v1/notes?kind=checklist&tag=home")); got != "Groceries" {
		t.Errorf("kind=checklist&tag=home → %s", got)
	}
	if got := len(listNotes(t, h, "user1", "/v1/notes")); got != 3 {
		t.Errorf("unfiltered list has %d notes, want 3", got)
	}

	corpus := listNotes(t, h, "user1", "/v1/notes?include=search_text")
	kinds := map[string]string{}
	for _, n := range corpus {
		kinds[n.ID] = n.Kind
	}
	if kinds[packing.ID] != "checklist" || kinds[groceries.ID] != "checklist" {
		t.Errorf("corpus rows do not carry kind: %v", kinds)
	}
}
