package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// The router's answer to "where does this belong" is produced by a paid LLM
// call, stored on the capture, and — until these tests existed — dropped by the
// wire type before the response left the process. A client could therefore only
// ask the user the question the inference had already answered, by listing
// every note they own in no particular order.
//
// Both branches are covered because exactly one is ever set, and a client that
// only handles the one it happened to see first renders nothing for the other.

func TestACaptureCarriesTheNoteTheRouterSuggested(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Kitchen rebuild", nil)

	capture := h.putCapture(t, model.CaptureIndex{
		ID: "c_suggest_note", UserID: "user1", Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), SuggestedNoteID: note.ID, RouteConfidence: 0.62,
	})

	body := decodeCapture(t, h, capture.ID)

	got, ok := body["suggested_note_id"].(string)
	if !ok || got != note.ID {
		t.Fatalf("suggested_note_id = %v, want %q — without it the client cannot offer "+
			"the note the router already chose", body["suggested_note_id"], note.ID)
	}
	if body["suggested_title"] != nil {
		t.Errorf("suggested_title = %v, want null: exactly one of the two is ever set",
			body["suggested_title"])
	}
}

func TestACaptureCarriesTheTitleTheRouterWouldGiveANewNote(t *testing.T) {
	h := newHarness(t)

	capture := h.putCapture(t, model.CaptureIndex{
		ID: "c_suggest_title", UserID: "user1", Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), SuggestedTitle: "Kitchen rebuild", RouteConfidence: 0.41,
	})

	body := decodeCapture(t, h, capture.ID)

	got, ok := body["suggested_title"].(string)
	if !ok || got != "Kitchen rebuild" {
		t.Fatalf("suggested_title = %v, want %q", body["suggested_title"], "Kitchen rebuild")
	}
	if body["suggested_note_id"] != nil {
		t.Errorf("suggested_note_id = %v, want null", body["suggested_note_id"])
	}
}

// TestACaptureWithNoSuggestionSaysSoExplicitly keeps the two fields present and
// null rather than absent. The frontend's wire types declare them nullable, and
// a field that vanishes when empty is a different shape from one that is null.
func TestACaptureWithNoSuggestionSaysSoExplicitly(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof repair", nil)

	capture := h.putCapture(t, model.CaptureIndex{
		ID: "c_routed", UserID: "user1", NoteID: note.ID,
		Status: model.StatusAppended, CreatedAt: model.Now(),
	})

	body := decodeCapture(t, h, capture.ID)

	for _, field := range []string{"suggested_note_id", "suggested_title"} {
		v, present := body[field]
		if !present {
			t.Errorf("%s is absent; it must be present and null", field)
		}
		if v != nil {
			t.Errorf("%s = %v, want null on a capture that was routed", field, v)
		}
	}
}

// TestTheSuggestionSurvivesTheCaptureList covers the collection as well as the
// single read. The progress card renders from the list, so a field carried by
// only one of the two is a field the card cannot use.
func TestTheSuggestionSurvivesTheCaptureList(t *testing.T) {
	h := newHarness(t)
	h.putCapture(t, model.CaptureIndex{
		ID: "c_suggest_list", UserID: "user1", Status: model.StatusNeedsTarget,
		CreatedAt: model.Now(), SuggestedTitle: "Kitchen rebuild",
	})

	w := h.do(t, http.MethodGet, "/v1/captures", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/captures = %d", w.Code)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range page.Items {
		if item["id"] == "c_suggest_list" {
			if item["suggested_title"] != "Kitchen rebuild" {
				t.Fatalf("suggested_title in the list = %v, want %q",
					item["suggested_title"], "Kitchen rebuild")
			}
			return
		}
	}
	t.Fatal("the needs_target capture was not in the list at all")
}

func decodeCapture(t *testing.T, h *harness, captureID string) map[string]any {
	t.Helper()
	w := h.do(t, http.MethodGet, "/v1/captures/"+captureID, "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/captures/%s = %d: %s", captureID, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}
