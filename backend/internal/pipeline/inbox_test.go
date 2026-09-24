package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// A recording the inbox wrote as MP3 or WAV reaches the speech provider with
// the type the object was stored under: the bucket notifies on both
// suffixes and contentTypeForAudioKey round-trips them.
func TestInboxAudioContainersReachTheProviderWithTheirOwnType(t *testing.T) {
	for _, tc := range []struct{ ext, contentType string }{
		{"mp3", "audio/mpeg"},
		{"wav", "audio/wav"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Cleaned."}})
			ctx := context.Background()
			key := "tenants/user1/captures/c_1/audio." + tc.ext
			if err := h.objects.PutTagged(ctx, key, []byte("bytes"), tc.contentType, map[string]string{"chintan-artifact": "capture-audio"}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
				ID: "c_1", UserID: "user1", NoteID: "note1", Status: model.StatusUploaded,
				AudioKey: key, CreatedAt: model.Now(), Source: model.DeviceSource("dev_1"),
			}); err != nil {
				t.Fatal(err)
			}
			seedNote(t, h.store, h.objects, "note1")

			if err := NewWorker(h.pipeline).Handle(ctx, s3Event(key)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := h.stt.Sources; len(got) != 1 || got[0].ContentType != tc.contentType {
				t.Fatalf("stt sources = %+v, want one call as %s", got, tc.contentType)
			}
			capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
			if capture.Status != model.StatusAppended || capture.Source != "device:dev_1" {
				t.Fatalf("capture = %+v", capture)
			}
		})
	}
}

// seedNote puts an empty note in place for an append to land in.
func seedNote(t *testing.T, store *memory.Store, objects interface {
	Put(context.Context, string, []byte, string) error
}, noteID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.PutNote(ctx, "user1", model.NoteIndex{
		ID: noteID, Title: "Destination", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/" + noteID + "/note.md",
	}); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, "tenants/user1/notes/"+noteID+"/note.md", []byte(""), "text/markdown"); err != nil {
		t.Fatal(err)
	}
}

// A capture that arrived as text has its transcript before the pipeline
// starts: transcription is skipped, routing reads the text, and the note's
// own language never sends it to the provider either.
func TestTextCaptureSkipsTranscriptionAndRoutes(t *testing.T) {
	ctx := context.Background()

	t.Run("routed", func(t *testing.T) {
		h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Buy milk."}})
		seedNote(t, h.store, h.objects, "note1")
		h.router.Decision = provider.RouteDecision{Action: provider.RouteAppend, NoteID: "note1", Content: "buy milk", Confidence: 0.95}
		if err := h.objects.Put(ctx, "tenants/user1/captures/c_t/raw.txt", []byte("buy milk"), "text/plain"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_t", UserID: "user1", Status: model.StatusTranscribed, CreatedAt: model.Now(),
			RawKey: "tenants/user1/captures/c_t/raw.txt", Source: model.DeviceSource("dev_1"),
		}); err != nil {
			t.Fatal(err)
		}

		final, err := h.pipeline.Run(ctx, "user1", "c_t")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if h.stt.Calls() != 0 {
			t.Fatalf("the speech provider was called %d times for text", h.stt.Calls())
		}
		if h.router.CallCount() != 1 || final.NoteID != "note1" || final.Status != model.StatusAppended {
			t.Fatalf("final = %+v (router calls %d)", final, h.router.CallCount())
		}
		body, _ := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
		if !strings.Contains(string(body), "Buy milk.") {
			t.Fatalf("note body = %q", body)
		}
	})

	t.Run("into a note with its own language", func(t *testing.T) {
		h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "പാൽ വാങ്ങുക."}})
		seedNote(t, h.store, h.objects, "note_ml")
		note, _ := h.store.GetNote(ctx, "user1", "note_ml")
		note.Language = "ml"
		if _, err := h.store.PutNote(ctx, "user1", note); err != nil {
			t.Fatal(err)
		}
		if err := h.objects.Put(ctx, "tenants/user1/captures/c_t/raw.txt", []byte("പാൽ വാങ്ങുക"), "text/plain"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_t", UserID: "user1", NoteID: "note_ml", Status: model.StatusTranscribed, CreatedAt: model.Now(),
			RawKey: "tenants/user1/captures/c_t/raw.txt", TargetSource: model.TargetSourceClient,
		}); err != nil {
			t.Fatal(err)
		}
		final, err := h.pipeline.Run(ctx, "user1", "c_t")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if h.stt.Calls() != 0 {
			t.Fatalf("a text capture was sent for transcription in the note's language: %d calls", h.stt.Calls())
		}
		if final.Status != model.StatusAppended {
			t.Fatalf("final = %+v", final)
		}
	})
}
