package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The owner's capture of 2026-09-27: "add milk to the shopping list" with no
// such note. The router names a new note and its kind; a checklist is created
// as one, and this same run extracts the item, so the body is "- [ ] Milk"
// and never the cleaned sentence. The kind, not the words, decides: the same
// recording the router files as a plain note is still the cleaned sentence,
// and into the listed Shopping list it is the same item.
func TestARoutedRecordingThatNamesAnUnlistedListStartsAChecklistWithTheItem(t *testing.T) {
	const raw = "add milk to the shopping list"
	for _, tc := range []struct {
		name       string
		listed     bool
		decision   provider.RouteDecision
		wantNoteID string
		wantKind   string
		wantBody   string
		wantItems  int
	}{
		{
			name:      "a new checklist",
			decision:  provider.RouteDecision{Action: provider.RouteNew, Title: "Shopping list", Checklist: true, Confidence: 1},
			wantKind:  model.NoteKindChecklist,
			wantBody:  "- [ ] Milk",
			wantItems: 1,
		},
		{
			name:     "a new plain note",
			decision: provider.RouteDecision{Action: provider.RouteNew, Title: "Shopping list", Confidence: 1},
			wantBody: "Add milk to the shopping list.",
		},
		{
			name:       "the listed checklist",
			listed:     true,
			decision:   provider.RouteDecision{Action: provider.RouteAppend, NoteID: "list1", Confidence: 1},
			wantNoteID: "list1",
			wantKind:   model.NoteKindChecklist,
			wantBody:   "- [ ] Milk",
			wantItems:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{
				stt:    &fake.STT{Response: raw},
				llm:    &fake.LLM{ItemsResponse: []string{"Milk"}, Response: "Add milk to the shopping list."},
				router: &fake.Router{Decision: tc.decision},
			})
			ctx := context.Background()
			if tc.listed {
				seedChecklistCapture(t, h, "", nil)
			} else {
				if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
					ID: "c_1", UserID: "user1", Status: model.StatusUploaded,
					AudioKey: "tenants/user1/captures/c_1/audio.webm", CreatedAt: model.Now(),
				}); err != nil {
					t.Fatal(err)
				}
			}

			capture, err := h.pipeline.Run(ctx, "user1", "c_1")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if capture.Status != model.StatusAppended || capture.NoteID == "" {
				t.Fatalf("capture = %s (%s) in %q, want appended", capture.Status, capture.Error, capture.NoteID)
			}
			if tc.wantNoteID != "" && capture.NoteID != tc.wantNoteID {
				t.Errorf("note = %q, want %q", capture.NoteID, tc.wantNoteID)
			}
			n := mustGetNote(t, h.store, "user1", capture.NoteID)
			if n.Kind != tc.wantKind || n.Title != "Shopping list" {
				t.Errorf("note kind = %q title = %q, want kind %q titled Shopping list", n.Kind, n.Title, tc.wantKind)
			}
			body, err := h.objects.Get(ctx, n.S3MarkdownKey)
			if err != nil {
				t.Fatal(err)
			}
			if want := service.CaptureMarker("c_1") + "\n" + tc.wantBody; string(body) != want {
				t.Errorf("body = %q, want %q", body, want)
			}
			if items, cleans := len(h.llm.ItemsCalls()), h.llm.Calls(); items != tc.wantItems || cleans != 1-tc.wantItems {
				t.Errorf("calls: items=%d cleanup=%d; want items=%d cleanup=%d", items, cleans, tc.wantItems, 1-tc.wantItems)
			}
		})
	}
}
