package handler_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
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

// A filter is applied before the page is cut, not to the page the store had
// already cut: with sixty plain notes and one checklist, `?kind=checklist`
// used to answer the one match WITH a cursor — the raw page of fifty rows was
// full — and the page after it was empty, which Home rendered as
// "Checklists · 0+" (smoke 2026-09-21, finding 1). Now a page holds up to
// `limit` matches and the cursor is set only when more matches exist.
func TestFilteredListPagesOverMatchesNotRows(t *testing.T) {
	cases := []struct {
		name     string
		plain    int
		matching int
		match    map[string]any // what makes a note match the query
		query    string
		// Items per page, following the cursor until there is none.
		wantPages []int
	}{
		{"one checklist among sixty plain notes", 60, 1,
			map[string]any{"kind": "checklist"}, "kind=checklist&limit=50", []int{1}},
		{"a hundred and twenty checklists in pages of a hundred", 0, 120,
			map[string]any{"kind": "checklist"}, "kind=checklist&limit=100", []int{100, 20}},
		{"thirty tagged notes spread over three raw pages of fifty", 90, 30,
			map[string]any{"tags": []string{"house"}}, "tag=house&limit=50", []int{30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// Interleaved, so the matches sit among the rest in list order
			// rather than all on the first raw page.
			total := tc.plain + tc.matching
			for i, made := 0, 0; i < total; i++ {
				if made < tc.matching && i*tc.matching/total >= made {
					h.createNote(t, "user1", fmt.Sprintf("Match %d", made), tc.match)
					made++
					continue
				}
				h.createNote(t, "user1", fmt.Sprintf("Plain %d", i), nil)
			}

			var got []int
			path := "/v1/notes?" + tc.query
			for {
				w := h.do(t, http.MethodGet, path, "user1", nil)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: status = %d body = %s", path, w.Code, w.Body.String())
				}
				var page handler.Page[handler.Note]
				decodeInto(t, w, &page)
				for _, n := range page.Items {
					if !strings.HasPrefix(n.Title, "Match ") {
						t.Errorf("%q came through the filter", n.Title)
					}
				}
				got = append(got, len(page.Items))
				if page.Cursor == "" {
					break
				}
				if len(got) > len(tc.wantPages) {
					t.Fatalf("still a cursor after %d pages: %v", len(got), got)
				}
				path = "/v1/notes?" + tc.query + "&cursor=" + url.QueryEscape(page.Cursor)
			}
			if !slices.Equal(got, tc.wantPages) {
				t.Errorf("pages = %v, want %v", got, tc.wantPages)
			}
		})
	}
}
