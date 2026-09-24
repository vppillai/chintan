package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

// listIDs pages GET /v1/notes at the given limit and returns the ids in the
// order served, with how many pages it took.
func listIDs(t *testing.T, h *harness, user string, limit int) ([]string, int) {
	t.Helper()
	var ids []string
	cursor, pages := "", 0
	for {
		path := fmt.Sprintf("/v1/notes?limit=%d", limit)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := h.do(t, http.MethodGet, path, user, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d body = %s", path, w.Code, w.Body.String())
		}
		var page handler.Page[handler.Note]
		decodeInto(t, w, &page)
		pages++
		for _, n := range page.Items {
			ids = append(ids, n.ID)
		}
		if page.Cursor == "" || pages > 20 {
			return ids, pages
		}
		cursor = page.Cursor
	}
}

func pin(t *testing.T, h *harness, user string, note handler.Note, pinned bool) handler.Note {
	t.Helper()
	current := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, user, nil)
	var detail handler.NoteDetail
	decodeInto(t, current, &detail)
	w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, user,
		map[string]any{"version": detail.Version, "pinned": pinned})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH pinned=%v: status = %d body = %s", pinned, w.Code, w.Body.String())
	}
	var out handler.Note
	decodeInto(t, w, &out)
	return out
}

// The pinned tier: a new pin lands last among the pinned notes, the pinned
// notes lead the list in rank order whatever was touched since, the cursor
// carries a page boundary across the tier, and archiving clears the pin.
func TestPinnedNotesLeadTheListAndPageAcrossTheTier(t *testing.T) {
	h := newHarness(t)
	a := h.createNote(t, "user1", "A", nil)
	b := h.createNote(t, "user1", "B", nil)
	c := h.createNote(t, "user1", "C", nil)

	first := pin(t, h, "user1", a, true)
	if !first.Pinned || first.PinRank == nil || *first.PinRank != 0 {
		t.Fatalf("first pin = %+v, want pinned at rank 0", first)
	}
	second := pin(t, h, "user1", b, true)
	if second.PinRank == nil || *second.PinRank != model.PinRankStep {
		t.Fatalf("second pin rank = %v, want %d", second.PinRank, model.PinRankStep)
	}
	// Touching c would put it first in a plain touch order; it stays below
	// the pinned tier.
	h.do(t, http.MethodPatch, "/v1/notes/"+c.ID, "user1", map[string]any{"version": c.Version, "title": "C touched"})

	if got, _ := listIDs(t, h, "user1", 50); fmt.Sprint(got) != fmt.Sprint([]string{a.ID, b.ID, c.ID}) {
		t.Fatalf("order = %v, want [a b c]", got)
	}
	// One note per page: the cursor must resume inside the pinned tier and
	// then step across into the touch-ordered tier without a skip or repeat.
	got, pages := listIDs(t, h, "user1", 1)
	if fmt.Sprint(got) != fmt.Sprint([]string{a.ID, b.ID, c.ID}) || pages != 3 {
		t.Fatalf("paged order = %v over %d pages, want [a b c] over 3", got, pages)
	}

	unpinned := pin(t, h, "user1", a, false)
	if unpinned.Pinned || unpinned.PinRank != nil {
		t.Fatalf("unpin = %+v, want pinned false with a null rank", unpinned)
	}
	if got, _ := listIDs(t, h, "user1", 50); got[0] != b.ID {
		t.Fatalf("after unpinning a, order = %v, want b first", got)
	}

	// Archiving b takes it out of the Pinned group; restoring it does not
	// put it back.
	if w := h.do(t, http.MethodDelete, "/v1/notes/"+b.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive: %d", w.Code)
	}
	stored, err := h.store.GetNote(context.Background(), "user1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Pinned() || stored.PinRank != 0 {
		t.Fatalf("archived note still pinned: %+v", stored)
	}
	w := h.do(t, http.MethodPost, "/v1/notes/"+b.ID+"/restore", "user1", nil)
	var restored handler.Note
	decodeInto(t, w, &restored)
	if restored.Pinned {
		t.Fatal("a restored note came back pinned")
	}
}

// A new pin lands last even once an unpin has left a gap in the ranks: pins
// at 0/1000/2000, the middle one unpinned, and the next pin must sit below
// the one at 2000 rather than tie it (a tie is broken by the newer pin,
// which would put it above).
func TestANewPinLandsLastAfterAnUnpin(t *testing.T) {
	h := newHarness(t)
	a := h.createNote(t, "user1", "A", nil)
	b := h.createNote(t, "user1", "B", nil)
	c := h.createNote(t, "user1", "C", nil)
	d := h.createNote(t, "user1", "D", nil)
	for _, n := range []handler.Note{a, b, c} {
		pin(t, h, "user1", n, true)
	}
	pin(t, h, "user1", b, false)
	last := pin(t, h, "user1", d, true)
	if last.PinRank == nil || *last.PinRank != 3*model.PinRankStep {
		t.Fatalf("rank after a gap = %v, want %d", last.PinRank, 3*model.PinRankStep)
	}
	if got, _ := listIDs(t, h, "user1", 50); fmt.Sprint(got) != fmt.Sprint([]string{a.ID, c.ID, d.ID, b.ID}) {
		t.Fatalf("order = %v, want [a c d b]", got)
	}
}

func TestPinningStopsAtFifty(t *testing.T) {
	h := newHarness(t)
	var notes []handler.Note
	for i := 0; i <= model.MaxPinnedNotes; i++ {
		notes = append(notes, h.createNote(t, "user1", fmt.Sprintf("Note %d", i), nil))
	}
	for _, n := range notes[:model.MaxPinnedNotes] {
		pin(t, h, "user1", n, true)
	}
	last := notes[model.MaxPinnedNotes]
	w := h.do(t, http.MethodPatch, "/v1/notes/"+last.ID, "user1", map[string]any{"version": last.Version, "pinned": true})
	if w.Code != http.StatusConflict {
		t.Fatalf("51st pin: status = %d body = %s", w.Code, w.Body.String())
	}
	if p := problemOf(t, w); p["detail"] != "you can pin up to fifty notes" {
		t.Fatalf("detail = %q", p["detail"])
	}
	// Pinning a note that is already pinned is not a 51st pin.
	if again := pin(t, h, "user1", notes[0], true); *again.PinRank != 0 {
		t.Fatalf("re-pinning moved the note to rank %d", *again.PinRank)
	}
}

func TestReorderPinsValidatesAndRewritesTheRanks(t *testing.T) {
	h := newHarness(t)
	a := h.createNote(t, "user1", "A", nil)
	b := h.createNote(t, "user1", "B", nil)
	c := h.createNote(t, "user1", "C", map[string]any{"body": "the tiler starts on the fourteenth"})
	loose := h.createNote(t, "user1", "Not pinned", nil)
	theirs := h.createNote(t, "user2", "Theirs", nil)
	for _, n := range []handler.Note{a, b, c} {
		pin(t, h, "user1", n, true)
	}
	pin(t, h, "user2", theirs, true)

	reorder := func(ids []string) int {
		return h.do(t, http.MethodPost, "/v1/notes/pins", "user1", map[string]any{"ids": ids}).Code
	}
	for name, ids := range map[string][]string{
		"an unpinned note":         {c.ID, loose.ID},
		"another tenant's pin":     {c.ID, theirs.ID},
		"a note that is not there": {c.ID, "missing"},
		"the same note twice":      {c.ID, c.ID},
	} {
		if got := reorder(ids); got != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, got)
		}
	}
	if got := reorder(nil); got != http.StatusBadRequest {
		t.Errorf("no ids: status = %d, want 400", got)
	}
	tooMany := make([]string, model.MaxPinnedNotes+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("n%d", i)
	}
	if got := reorder(tooMany); got != http.StatusBadRequest {
		t.Errorf("51 ids: status = %d, want 400", got)
	}
	// Nothing above moved anything.
	if got, _ := listIDs(t, h, "user1", 50); fmt.Sprint(got) != fmt.Sprint([]string{a.ID, b.ID, c.ID, loose.ID}) {
		t.Fatalf("a refused reorder changed the order: %v", got)
	}

	w := h.do(t, http.MethodPost, "/v1/notes/pins", "user1", map[string]any{"ids": []string{c.ID, a.ID, b.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("reorder: status = %d body = %s", w.Code, w.Body.String())
	}
	var page handler.Page[handler.Note]
	decodeInto(t, w, &page)
	if len(page.Items) != 3 || page.Cursor != "" {
		t.Fatalf("reorder answered %d items, cursor %q", len(page.Items), page.Cursor)
	}
	for i, want := range []string{c.ID, a.ID, b.ID} {
		if page.Items[i].ID != want || *page.Items[i].PinRank != int64(i)*model.PinRankStep {
			t.Fatalf("item %d = %s rank %d, want %s rank %d", i, page.Items[i].ID, *page.Items[i].PinRank, want, i*model.PinRankStep)
		}
	}
	if got, _ := listIDs(t, h, "user1", 50); fmt.Sprint(got) != fmt.Sprint([]string{c.ID, a.ID, b.ID, loose.ID}) {
		t.Fatalf("list after reorder = %v, want [c a b loose]", got)
	}
	// The note keeps its search text through the reorder: the service reads
	// each note whole before writing it, since a listed note carries none and
	// a row put from one would lose it.
	stored, err := h.store.GetNote(context.Background(), "user1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PinRank != 0 || stored.SearchText == "" {
		t.Fatalf("stored note after reorder: rank %d, search text %q", stored.PinRank, stored.SearchText)
	}
}
