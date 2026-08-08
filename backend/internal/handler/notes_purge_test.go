package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type purgeResultWire struct {
	NoteID string `json:"note_id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func purge(t *testing.T, h *harness, ids []string, headers ...[2]string) []purgeResultWire {
	t.Helper()
	w := h.do(t, http.MethodPost, "/v1/notes/purge", "user1", map[string]any{"note_ids": ids}, headers...)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/notes/purge = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Results []purgeResultWire `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Results
}

// archiveViaAPI creates a note and archives it the way a client does, so the
// test exercises the same states the app produces.
func archiveViaAPI(t *testing.T, h *harness, title string) string {
	t.Helper()
	note := h.createNote(t, "user1", title, nil)
	if w := h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive = %d", w.Code)
	}
	return note.ID
}

// TestPurgeBatchAnswersOncePerNote is the contract the client renders from: a
// mixed batch reports each outcome separately rather than collapsing to one
// verdict the server cannot honestly give.
func TestPurgeBatchAnswersOncePerNote(t *testing.T) {
	h := newHarness(t)
	archived := archiveViaAPI(t, h, "Done with this")
	active := h.createNote(t, "user1", "Still in use", nil)

	results := purge(t, h, []string{archived, "note_missing", active.ID})

	if len(results) != 3 {
		t.Fatalf("got %d results, want one per requested note", len(results))
	}
	want := map[string]string{
		archived:       "purged",
		"note_missing": "not_found",
		active.ID:      "failed",
	}
	for _, r := range results {
		if want[r.NoteID] != r.Status {
			t.Errorf("%s = %q, want %q (detail %q)", r.NoteID, r.Status, want[r.NoteID], r.Detail)
		}
	}
}

// TestPurgeBatchLeavesAnActiveNoteAlone is the one that costs a user their
// notes if it regresses.
func TestPurgeBatchLeavesAnActiveNoteAlone(t *testing.T) {
	h := newHarness(t)
	active := h.createNote(t, "user1", "Still in use", nil)

	results := purge(t, h, []string{active.ID})
	if results[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", results[0].Status)
	}
	if !strings.Contains(results[0].Detail, "not archived") {
		t.Errorf("detail = %q, want it to explain that the note must be archived first", results[0].Detail)
	}

	if w := h.do(t, http.MethodGet, "/v1/notes/"+active.ID, "user1", nil); w.Code != http.StatusOK {
		t.Fatalf("the active note is gone: GET returned %d", w.Code)
	}
}

// TestPurgeBatchIsIdempotentUnderTheSameKey covers the replay every other POST
// on this API supports. A dropped response must not turn into a second cascade.
func TestPurgeBatchIsIdempotentUnderTheSameKey(t *testing.T) {
	h := newHarness(t)
	archived := archiveViaAPI(t, h, "Done with this")
	key := [2]string{"Idempotency-Key", "purge-batch-1"}

	first := purge(t, h, []string{archived}, key)
	if first[0].Status != "purged" {
		t.Fatalf("first = %q, want purged", first[0].Status)
	}

	// The recorded response, not a fresh run: a second cascade would report
	// not_found, so replaying the original answer is what proves the record was
	// used.
	second := purge(t, h, []string{archived}, key)
	if second[0].Status != "purged" {
		t.Errorf("replay = %q, want the original answer %q", second[0].Status, "purged")
	}
}

// TestPurgeBatchRefusesAnEmptyOrOversizedBatch keeps the work per request
// bounded and rejects a body that names nothing.
func TestPurgeBatchRefusesAnEmptyOrOversizedBatch(t *testing.T) {
	h := newHarness(t)

	if w := h.do(t, http.MethodPost, "/v1/notes/purge", "user1",
		map[string]any{"note_ids": []string{}}); w.Code != http.StatusBadRequest {
		t.Errorf("empty batch = %d, want 400", w.Code)
	}

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "note_x"
	}
	w := h.do(t, http.MethodPost, "/v1/notes/purge", "user1", map[string]any{"note_ids": ids})
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized batch = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "100") {
		t.Errorf("the refusal does not say what the limit is: %s", w.Body.String())
	}
}

// TestPurgeBatchIsScopedToTheCaller keeps one tenant from deleting another's
// notes by naming their ids, and from learning that those ids exist.
func TestPurgeBatchIsScopedToTheCaller(t *testing.T) {
	h := newHarness(t)
	theirs := archiveViaAPI(t, h, "Theirs")

	w := h.do(t, http.MethodPost, "/v1/notes/purge", "user2",
		map[string]any{"note_ids": []string{theirs}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		Results []purgeResultWire `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Results[0].Status != "not_found" {
		t.Errorf("status = %q, want not_found — anything else confirms another tenant's id",
			body.Results[0].Status)
	}

	// Still there for its owner.
	if _, err := h.store.GetNote(t.Context(), "user1", theirs); err != nil {
		t.Fatalf("another tenant's note was deleted: %v", err)
	}
}
