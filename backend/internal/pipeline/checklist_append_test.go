package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// A recording filed into a checklist becomes one open item on one line, with
// the capture marker on the line before it as for any append, so the body's
// paragraph rule still finds exactly this recording's text.
func TestAppendIntoAChecklistWritesOneItemPerRecording(t *testing.T) {
	ctx := context.Background()
	objects := memory.NewObjects()
	h := newHarness(t, harnessOpts{objects: objects})
	const noteKey = "tenants/user1/notes/list1/note.md"
	if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "list1", Title: "Packing", Kind: model.NoteKindChecklist,
		UpdatedAt: model.Now(), S3MarkdownKey: noteKey,
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := objects.Put(ctx, noteKey, []byte(""), "text/markdown"); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	seedCleaned := func(id, cleaned string) {
		key := "tenants/user1/captures/" + id + "/clean.txt"
		if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
			ID: id, UserID: "user1", NoteID: "list1", Status: model.StatusCleaned,
			Mode: model.CleanupFaithful, CleanKey: key, CreatedAt: model.Now(),
			AudioKey: "tenants/user1/captures/" + id + "/audio.webm",
			RawKey:   "tenants/user1/captures/" + id + "/raw.txt",
		}); err != nil {
			t.Fatalf("seed capture %s: %v", id, err)
		}
		if err := objects.Put(ctx, key, []byte(cleaned), "text/plain"); err != nil {
			t.Fatalf("seed clean text %s: %v", id, err)
		}
	}
	body := func() string {
		raw, err := objects.Get(ctx, noteKey)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return string(raw)
	}

	// The cleaned transcript arrives as prose with its own line breaks; the
	// item is one line.
	seedCleaned("c_1", "Call the roofer.\n\nAnd   ask about\tthe gutter.  ")
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run c_1: %v", err)
	}
	want := service.CaptureMarker("c_1") + "\n- [ ] Call the roofer. And ask about the gutter."
	if got := body(); got != want {
		t.Fatalf("body after the first append = %q, want %q", got, want)
	}

	// The second recording is the next item, placed as every append is placed.
	seedCleaned("c_2", "buy sealant")
	if _, err := h.pipeline.Run(ctx, "user1", "c_2"); err != nil {
		t.Fatalf("Run c_2: %v", err)
	}
	want += "\n\n" + service.CaptureMarker("c_2") + "\n- [ ] buy sealant"
	if got := body(); got != want {
		t.Fatalf("body after the second append = %q, want %q", got, want)
	}

	// What the person and the index see: item lines and nothing else.
	if got := service.StripCaptureMarkers(body()); got != "- [ ] Call the roofer. And ask about the gutter.\n\n- [ ] buy sealant" {
		t.Errorf("stripped body = %q", got)
	}
	n := mustGetNote(t, h.store, "user1", "list1")
	if !strings.HasPrefix(n.Snippet, "- [ ] Call the roofer.") || !strings.Contains(n.SearchText, "- [ ] buy sealant") {
		t.Errorf("index sees something other than the raw lines: snippet=%q search=%q", n.Snippet, n.SearchText)
	}

	// Deleting one recording removes exactly its item: the same primitive the
	// API's delete uses, over the body the append just wrote.
	rest, text, found := service.CutCaptureParagraph(body(), "c_1")
	if !found || text != "- [ ] Call the roofer. And ask about the gutter." {
		t.Errorf("CutCaptureParagraph(c_1) = %q, %v", text, found)
	}
	if rest != service.CaptureMarker("c_2")+"\n- [ ] buy sealant" {
		t.Errorf("body without c_1 = %q", rest)
	}
}

func TestChecklistItemIsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("word ", 600) // 3,000 runes
	for _, tc := range []struct{ name, in, want string }{
		{"one line", "buy milk", "- [ ] buy milk"},
		{"collapses breaks and runs", "  buy\n\nmilk\t and   eggs \r\n", "- [ ] buy milk and eggs"},
		{"nothing said", " \n\t ", ""},
		{"keeps task syntax spoken as text", "- [ ] already an item", "- [ ] - [ ] already an item"},
	} {
		if got := checklistItem(tc.in); got != tc.want {
			t.Errorf("%s: checklistItem(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	got := checklistItem(long)
	if n := len([]rune(got)); n != len("- [ ] ")+maxChecklistItemRunes-1 { // the cut lands on a trailing space, which is trimmed
		t.Errorf("a long transcript is one item of %d runes, want the cap", n)
	}
	if strings.Count(got, "\n") != 0 {
		t.Error("a long item was split across lines; the split is the cleaned view's job")
	}
}
