package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/routing"
	"github.com/vppillai/chintan/backend/internal/service"
)

// seedChecklistCapture puts the checklist "list1" (titled Shopping list) and
// an uploaded recording c_1 aimed at noteID — "" for one the router decides.
func seedChecklistCapture(t *testing.T, h *harness, noteID string, mutate func(*model.NoteIndex)) {
	t.Helper()
	ctx := context.Background()
	n := model.NoteIndex{
		ID: "list1", Title: "Shopping list", Kind: model.NoteKindChecklist,
		UpdatedAt: model.Now(), S3MarkdownKey: "tenants/user1/notes/list1/note.md",
	}
	if mutate != nil {
		mutate(&n)
	}
	if _, err := h.store.PutNote(ctx, "user1", n); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := h.objects.Put(ctx, n.S3MarkdownKey, []byte(""), "text/markdown"); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: noteID, Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm", CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
}

func runChecklistCapture(t *testing.T, h *harness) (model.CaptureIndex, string) {
	t.Helper()
	ctx := context.Background()
	capture, err := h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, _ := h.objects.Get(ctx, "tenants/user1/notes/list1/note.md")
	return capture, string(body)
}

// The owner's first capture: routed into the existing Shopping list, the
// recording becomes exactly the items named, one line each under the one
// marker, from the RAW transcript — the router's spans (which here mangle the
// routed text to "and add chickpeas …") decide the destination and nothing
// else, and the transcript cleanup is not called. Deleting the recording
// removes exactly its items.
func TestARecordingRoutedIntoAChecklistBecomesItsItemsFromTheRawTranscript(t *testing.T) {
	const raw = "create a shopping list and add chickpeas and green gram into it"
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: raw},
		llm: &fake.LLM{ItemsResponse: []string{"Chickpeas", "Green gram"}, Response: "Add chickpeas and green gram into it."},
		router: &fake.Router{
			Decision: provider.RouteDecision{Action: provider.RouteAppend, NoteID: "list1", Confidence: 1},
			Spans:    []routing.Span{{StartWord: 0, EndWord: 4}},
		},
	})
	seedChecklistCapture(t, h, "", nil)

	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended || capture.NoteID != "list1" {
		t.Fatalf("capture = %s (%s) in %q, want appended into list1", capture.Status, capture.Error, capture.NoteID)
	}
	want := service.CaptureMarker("c_1") + "\n- [ ] Chickpeas\n- [ ] Green gram"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	calls := h.llm.ItemsCalls()
	if len(calls) != 1 || calls[0].Transcript != raw || calls[0].Title != "Shopping list" {
		t.Errorf("extraction calls = %+v, want one over the raw transcript for Shopping list", calls)
	}
	if n := h.llm.Calls(); n != 0 {
		t.Errorf("transcript cleanup was called %d time(s) for a checklist; the extraction replaces it", n)
	}
	if h.router.CallCount() != 1 {
		t.Errorf("router calls = %d, want the one routing call", h.router.CallCount())
	}

	rest, text, found := service.CutCaptureParagraph(body, "c_1")
	if !found || text != "- [ ] Chickpeas\n- [ ] Green gram" || rest != "" {
		t.Errorf("CutCaptureParagraph = %q, %q, %v; want both items and an empty body", rest, text, found)
	}
	n := mustGetNote(t, h.store, "user1", "list1")
	if !strings.HasPrefix(n.Snippet, "- [ ] Chickpeas") || !strings.Contains(n.SearchText, "- [ ] green gram") {
		t.Errorf("index sees something other than the item lines: snippet=%q search=%q", n.Snippet, n.SearchText)
	}
}

// A second recording is the next paragraph, placed as every append is; the
// rendering collapses whitespace per item and drops blank lines.
func TestASecondRecordingIntoAChecklistIsTheNextParagraph(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "milk and  eggs"},
		llm: &fake.LLM{ItemsResponse: []string{" Milk ", "", "Two\tloaves  of bread"}},
	})
	seedChecklistCapture(t, h, "list1", nil)
	ctx := context.Background()
	if err := h.objects.Put(ctx, "tenants/user1/notes/list1/note.md", []byte(service.CaptureMarker("c_0")+"\n- [ ] Umbrella"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	_, body := runChecklistCapture(t, h)
	want := service.CaptureMarker("c_0") + "\n- [ ] Umbrella\n\n" + service.CaptureMarker("c_1") + "\n- [ ] Milk\n- [ ] Two loaves of bread"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if got := service.StripCaptureMarkers(body); got != "- [ ] Umbrella\n\n- [ ] Milk\n- [ ] Two loaves of bread" {
		t.Errorf("stripped body = %q", got)
	}
}

// A recording that only tells the app what to do — "create a shopping list"
// — adds no item: the note exists and the capture is no_content, as an
// instruction-only recording is for a plain note.
func TestAnInstructionOnlyRecordingIntoAChecklistAddsNothing(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "create a shopping list"},
		llm: &fake.LLM{ItemsResponse: []string{}},
	})
	seedChecklistCapture(t, h, "list1", nil)
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusNoContent || capture.Error != "" {
		t.Errorf("capture = %s (%q), want no_content", capture.Status, capture.Error)
	}
	if body != "" {
		t.Errorf("an item was appended for an instruction-only recording: %q", body)
	}
}

// A reply that is not a list falls back to the recording as one item, the
// pre-2026-09-26 behaviour: dictation is never lost to a bad reply.
func TestAnUnusableItemsReplyAppendsTheRecordingAsOneItem(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "add umbrella\nto shopping list"},
		llm: &fake.LLM{ItemsErr: cleanup.ErrNotAnItemList},
	})
	seedChecklistCapture(t, h, "list1", nil)
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("capture = %s (%s), want appended", capture.Status, capture.Error)
	}
	if want := service.CaptureMarker("c_1") + "\n- [ ] add umbrella to shopping list"; body != want {
		t.Errorf("body = %q, want the recording as one item %q", body, want)
	}
}

// A verbatim checklist keeps the recording as spoken, in one item, and calls
// no model at all.
func TestAVerbatimChecklistTakesTheRecordingAsOneItem(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "um add milk\nand eggs"},
		llm: &fake.LLM{ItemsResponse: []string{"Milk", "Eggs"}},
	})
	seedChecklistCapture(t, h, "list1", func(n *model.NoteIndex) { n.Verbatim = true })
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("capture = %s (%s), want appended", capture.Status, capture.Error)
	}
	if want := service.CaptureMarker("c_1") + "\n- [ ] um add milk and eggs"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if len(h.llm.ItemsCalls()) != 0 || h.llm.Calls() != 0 {
		t.Errorf("a verbatim checklist called the model: items=%d cleanup=%d", len(h.llm.ItemsCalls()), h.llm.Calls())
	}
}

// The owner's second capture, into a verbatim list: the router decides the
// destination and its spans would cut the words down to "list"; the item is
// the recording as spoken, because that is what verbatim promises, and no
// model is called for it.
func TestAVerbatimChecklistReachedByTheRouterTakesTheRawWords(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "Add umbrella\nto shopping list"},
		llm: &fake.LLM{ItemsResponse: []string{"Umbrella"}},
		router: &fake.Router{
			Decision: provider.RouteDecision{Action: provider.RouteAppend, NoteID: "list1", Confidence: 1},
			Spans:    []routing.Span{{StartWord: 0, EndWord: 4}},
		},
	})
	seedChecklistCapture(t, h, "", func(n *model.NoteIndex) { n.Verbatim = true })
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended || capture.NoteID != "list1" {
		t.Fatalf("capture = %s (%s) in %q, want appended into list1", capture.Status, capture.Error, capture.NoteID)
	}
	if want := service.CaptureMarker("c_1") + "\n- [ ] Add umbrella to shopping list"; body != want {
		t.Errorf("body = %q, want the words as spoken %q", body, want)
	}
	if len(h.llm.ItemsCalls()) != 0 || h.llm.Calls() != 0 || h.router.CallCount() != 1 {
		t.Errorf("calls: items=%d cleanup=%d router=%d; want the one routing call only", len(h.llm.ItemsCalls()), h.llm.Calls(), h.router.CallCount())
	}
}

func TestChecklistItemsIsOneBoundedLinePerItem(t *testing.T) {
	long := strings.Repeat("word ", 600) // 3,000 runes
	for _, tc := range []struct{ name, in, want string }{
		{"one item", "buy milk", "- [ ] buy milk"},
		{"one line per item, blanks dropped", "Milk\n\n  Eggs \nTwo   loaves\tof bread\n", "- [ ] Milk\n- [ ] Eggs\n- [ ] Two loaves of bread"},
		{"nothing said", " \n\t ", ""},
		{"keeps task syntax spoken as text", "- [ ] already an item", "- [ ] - [ ] already an item"},
	} {
		if got := checklistItems(tc.in); got != tc.want {
			t.Errorf("%s: checklistItems(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	got := checklistItems(long + "\n" + long)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("two long items became %d lines", len(lines))
	}
	for _, line := range lines {
		if n := len([]rune(line)); n != len("- [ ] ")+cleanup.MaxChecklistItemRunes-1 { // the cut lands on a trailing space, which is trimmed
			t.Errorf("a long item is %d runes, want the cap", n)
		}
	}
}

// Ticks are carried line for line when a recording's items are written again;
// a different number of items carries nothing, because no line can be said to
// be the one that was ticked.
func TestKeepTickCarriesTicksLineForLine(t *testing.T) {
	for _, tc := range []struct{ name, old, text, want string }{
		{"one item", "- [x] milk", "- [ ] Milk", "- [x] Milk"},
		{"the second of three", "- [ ] a\n- [x] b\n- [ ] c", "- [ ] A\n- [ ] B\n- [ ] C", "- [ ] A\n- [x] B\n- [ ] C"},
		{"all ticked", "- [x] a\n- [x] b", "- [ ] A\n- [ ] B", "- [x] A\n- [x] B"},
		{"count differs", "- [x] a\n- [x] b", "- [ ] A\n- [ ] B\n- [ ] C", "- [ ] A\n- [ ] B\n- [ ] C"},
		{"plain paragraph", "First take.", "Second take.", "Second take."},
		{"nothing before", "", "- [ ] A", "- [ ] A"},
	} {
		if got := keepTick(tc.old, tc.text); got != tc.want {
			t.Errorf("%s: keepTick(%q, %q) = %q, want %q", tc.name, tc.old, tc.text, got, tc.want)
		}
	}
}
