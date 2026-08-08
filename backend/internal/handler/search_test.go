package handler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

func seedCorpus(t *testing.T, h *harness, userID string) {
	t.Helper()
	roof := h.createNote(t, userID, "Roof repair", map[string]any{
		"aliases": []string{"guttering"},
		"tags":    []string{"house", "urgent"},
	})
	h.do(t, http.MethodPatch, "/v1/notes/"+roof.ID, userID, map[string]any{
		"version": roof.Version,
		"body":    "The slate above the porch is cracked and lets water through.",
	})

	car := h.createNote(t, userID, "Car service", map[string]any{"tags": []string{"house"}})
	h.do(t, http.MethodPatch, "/v1/notes/"+car.ID, userID, map[string]any{
		"version": car.Version,
		"body":    "Book the annual service before the MOT runs out.",
	})
}

func TestSearchFindsAcrossTitleAliasTagAndBody(t *testing.T) {
	h := newHarness(t)
	seedCorpus(t, h, "user1")

	for query, want := range map[string]string{
		"roof":        "title",
		"guttering":   "alias",
		"urgent":      "tag",
		"porch":       "body",
		"slate water": "body", // every term must match, not any
	} {
		w := h.do(t, http.MethodGet, "/v1/search?q="+strings.ReplaceAll(query, " ", "+"), "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("q=%q: status = %d body = %s", query, w.Code, w.Body.String())
		}
		var got handler.Page[struct {
			NoteID    string   `json:"note_id"`
			Title     string   `json:"title"`
			Excerpt   string   `json:"excerpt"`
			MatchedIn []string `json:"matched_in"`
		}]
		decodeInto(t, w, &got)
		if len(got.Items) != 1 {
			t.Fatalf("q=%q returned %d results, want 1", query, len(got.Items))
		}
		found := false
		for _, field := range got.Items[0].MatchedIn {
			if field == want {
				found = true
			}
		}
		if !found {
			t.Errorf("q=%q: matched_in = %v, want it to include %q", query, got.Items[0].MatchedIn, want)
		}
		if want == "body" && got.Items[0].Excerpt == "" {
			t.Errorf("q=%q: a body match has no excerpt to show the hit in situ", query)
		}
	}
}

func TestSearchIsScopedToTheCaller(t *testing.T) {
	h := newHarness(t)
	seedCorpus(t, h, "alice")

	w := h.do(t, http.MethodGet, "/v1/search?q=roof", "bob", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got handler.Page[map[string]any]
	decodeInto(t, w, &got)
	if len(got.Items) != 0 {
		t.Fatalf("bob's search returned alice's notes: %v", got.Items)
	}
}

func TestSearchRequiresAQuery(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/v1/search", "/v1/search?q="} {
		w := h.do(t, http.MethodGet, path, "user1", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}

	long := "/v1/search?q=" + strings.Repeat("x", 600)
	if w := h.do(t, http.MethodGet, long, "user1", nil); w.Code != http.StatusBadRequest {
		t.Errorf("an over-long query: status = %d, want 400", w.Code)
	}
}

func TestTagsAreCountedAndFolded(t *testing.T) {
	h := newHarness(t)
	seedCorpus(t, h, "user1")

	w := h.do(t, http.MethodGet, "/v1/tags", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got handler.Page[struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}]
	decodeInto(t, w, &got)

	counts := map[string]int{}
	for _, tag := range got.Items {
		counts[tag.Name] = tag.Count
	}
	if counts["house"] != 2 {
		t.Errorf("house = %d, want 2", counts["house"])
	}
	if counts["urgent"] != 1 {
		t.Errorf("urgent = %d, want 1", counts["urgent"])
	}
	// Most used first, so the picker leads with what the person actually uses.
	if len(got.Items) > 0 && got.Items[0].Name != "house" {
		t.Errorf("first tag = %q, want the most used", got.Items[0].Name)
	}
}

func TestNotesCanBeFilteredByTag(t *testing.T) {
	h := newHarness(t)
	seedCorpus(t, h, "user1")

	notes := listNotes(t, h, "user1", "/v1/notes?tag=urgent")
	if len(notes) != 1 || notes[0].Title != "Roof repair" {
		t.Fatalf("tag filter returned %d notes: %+v", len(notes), notes)
	}
}

func TestExportProducesADownloadableCopy(t *testing.T) {
	h := newHarness(t)
	seedCorpus(t, h, "user1")

	w := h.do(t, http.MethodPost, "/v1/export", "user1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: status = %d body = %s", w.Code, w.Body.String())
	}
	var job struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
		Bytes     int64  `json:"bytes"`
	}
	decodeInto(t, w, &job)
	if job.ID == "" || job.Status == "" {
		t.Fatalf("job = %+v", job)
	}
	if job.Bytes == 0 {
		t.Error("the export is empty")
	}

	poll := h.do(t, http.MethodGet, "/v1/export/"+job.ID, "user1", nil)
	if poll.Code != http.StatusOK {
		t.Fatalf("poll: status = %d body = %s", poll.Code, poll.Body.String())
	}
	var polled struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	decodeInto(t, poll, &polled)
	if polled.Status != "ready" || polled.URL == "" {
		t.Fatalf("polled job = %+v", polled)
	}

	t.Run("another tenant cannot poll it", func(t *testing.T) {
		other := h.do(t, http.MethodGet, "/v1/export/"+job.ID, "bob", nil)
		if other.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", other.Code)
		}
	})

	t.Run("an id this API never issued is 404, not 500", func(t *testing.T) {
		// The second is outside the pattern the contract declares, and the id
		// becomes part of an object key, so it is refused rather than trusted.
		for _, id := range []string{"e_neverissued", "not%20an%20id"} {
			got := h.do(t, http.MethodGet, "/v1/export/"+id, "user1", nil)
			if got.Code != http.StatusNotFound {
				t.Errorf("id=%q: status = %d, want 404", id, got.Code)
			}
		}
	})
}
