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
		AudioKey: "tenants/user1/captures/c_1/audio.webm", CreatedAt: model.Now(),
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
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "Chickpeas"}, {Text: "Green gram"}}, Response: "Add chickpeas and green gram into it."},
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
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: " Milk "}, {Text: ""}, {Text: "Two\tloaves  of bread"}}},
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
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{}},
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
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "Milk"}, {Text: "Eggs"}}},
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
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "Umbrella"}}},
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
		{"one line per item, blanks dropped", "Milk\n\n Eggs \nTwo   loaves\tof bread\n", "- [ ] Milk\n- [ ] Eggs\n- [ ] Two loaves of bread"},
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

// Ticks are carried when a recording's items are written again: to the line
// with the same words first (case aside), and line for line only when no
// words match and the count holds; a different number of other words carries
// nothing, because no line can be said to be the one that was ticked
// (regenerate_test.go pins the words-first cases).
func TestKeepTickCarriesTicksLineForLine(t *testing.T) {
	for _, tc := range []struct{ name, old, text, want string }{
		{"one item", "- [x] milk", "- [ ] Milk", "- [x] Milk"},
		{"the second of three", "- [ ] a\n- [x] b\n- [ ] c", "- [ ] A\n- [ ] B\n- [ ] C", "- [ ] A\n- [x] B\n- [ ] C"},
		{"all ticked", "- [x] a\n- [x] b", "- [ ] A\n- [ ] B", "- [x] A\n- [x] B"},
		{"count differs, same words", "- [x] a\n- [x] b", "- [ ] A\n- [ ] B\n- [ ] C", "- [x] A\n- [x] B\n- [ ] C"},
		{"count differs, other words", "- [x] a\n- [x] b", "- [ ] x\n- [ ] y\n- [ ] z", "- [ ] x\n- [ ] y\n- [ ] z"},
		{"plain paragraph", "First take.", "Second take.", "Second take."},
		{"nothing before", "", "- [ ] A", "- [ ] A"},
	} {
		if got := keepTick(tc.old, tc.text); got != tc.want {
			t.Errorf("%s: keepTick(%q, %q) = %q, want %q", tc.name, tc.old, tc.text, got, tc.want)
		}
	}
}

// A line the extraction indented two spaces is a sub-item and keeps that
// indent ahead of its box; one space is not an indent.
func TestChecklistItemsKeepsTheIndent(t *testing.T) {
	if got := checklistItems("Walmart\n  Eggs\n   Two   dozen \n Milk"); got != "- [ ] Walmart\n  - [ ] Eggs\n  - [ ] Two dozen\n- [ ] Milk" {
		t.Errorf("checklistItems = %q", got)
	}
}

// A tick follows its words to a sub-item, and the indent stays on both
// sides: a ticked sub-item written again is a ticked sub-item.
func TestKeepTickCarriesATickToASubItem(t *testing.T) {
	for _, tc := range []struct{ name, old, text, want string }{
		{"words, into a sub-item", "- [x] Eggs\n- [ ] Milk", "- [ ] Walmart\n  - [ ] Eggs\n- [ ] Milk", "- [ ] Walmart\n  - [x] Eggs\n- [ ] Milk"},
		{"a ticked sub-item stays one", "- [ ] Walmart\n  - [x] Eggs", "- [ ] Walmart\n  - [ ] Eggs", "- [ ] Walmart\n  - [x] Eggs"},
		{"line for line, indented", "  - [x] a\n  - [x] b", "  - [ ] x\n  - [ ] y", "  - [x] x\n  - [x] y"},
	} {
		if got := keepTick(tc.old, tc.text); got != tc.want {
			t.Errorf("%s: keepTick(%q, %q) = %q, want %q", tc.name, tc.old, tc.text, got, tc.want)
		}
	}
}

// The recording that names a group the list does not have: the tree goes
// under its marker, a child two spaces in, and the paragraph is cut whole.
func TestARecordingWithGroupsIsNestedUnderItsMarker(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "add buying eggs from Walmart and meat from Costco in the shopping list"},
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{
			{Text: "Walmart", Children: []cleanup.Item{{Text: "Eggs"}}},
			{Text: "Costco", Children: []cleanup.Item{{Text: "Meat"}}},
		}},
	})
	seedChecklistCapture(t, h, "list1", nil)
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("capture = %s (%s), want appended", capture.Status, capture.Error)
	}
	want := service.CaptureMarker("c_1") + "\n- [ ] Walmart\n  - [ ] Eggs\n- [ ] Costco\n  - [ ] Meat"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if artefact, _ := h.objects.Get(context.Background(), capture.CleanKey); string(artefact) != "Walmart\n  Eggs\nCostco\n  Meat" {
		t.Errorf("clean artefact = %q", artefact)
	}
	rest, text, found := service.CutCaptureParagraph(body, "c_1")
	if !found || rest != "" || text != "- [ ] Walmart\n  - [ ] Eggs\n- [ ] Costco\n  - [ ] Meat" {
		t.Errorf("CutCaptureParagraph = %q, %q, %v", rest, text, found)
	}
}

func item(text string, children ...string) cleanup.Item {
	it := cleanup.Item{Text: text}
	for _, c := range children {
		it.Children = append(it.Children, cleanup.Item{Text: c})
	}
	return it
}

// The merge oracle's twelve cases (round-6 checklist lens, merge_test.go).
func TestMergeChecklistItems(t *testing.T) {
	marker := service.CaptureMarker("c_1")
	cases := []struct {
		name  string
		body  string
		items []cleanup.Item
		want  string
		rest  string // checklistItems(cleanup.RenderItems(rest))
	}{
		{
			name:  "a child joins its existing parent after the block's last line",
			body:  "- [ ] Costco\n  - [ ] Meat\n- [ ] Milk",
			items: []cleanup.Item{item("Costco", "Chicken")},
			want:  "- [ ] Costco\n  - [ ] Meat\n  - [ ] Chicken\n- [ ] Milk",
		},
		{
			name:  "a child already under the parent is not added twice",
			body:  "- [ ] Costco\n  - [ ] Meat",
			items: []cleanup.Item{item("Costco", "Meat")},
			want:  "- [ ] Costco\n  - [ ] Meat",
		},
		{
			name:  "a parent the list does not have goes under the marker",
			body:  "- [ ] Costco\n  - [ ] Meat",
			items: []cleanup.Item{item("Walmart", "Eggs")},
			want:  "- [ ] Costco\n  - [ ] Meat",
			rest:  "- [ ] Walmart\n  - [ ] Eggs",
		},
		{
			name:  "a childless duplicate is dropped; a ticked one is wanted again",
			body:  "- [ ] Milk\n- [x] Eggs",
			items: []cleanup.Item{item("Milk"), item("Eggs"), item("Bread")},
			want:  "- [ ] Milk\n- [ ] Eggs",
			rest:  "- [ ] Bread",
		},
		{
			name:  "a done parent that gains a child is reopened; its done children stay done",
			body:  "- [x] Costco\n  - [x] Meat",
			items: []cleanup.Item{item("Costco", "Chicken")},
			want:  "- [ ] Costco\n  - [x] Meat\n  - [ ] Chicken",
		},
		{
			name:  "a done child named again is reopened, and its parent with it",
			body:  "- [x] Costco\n  - [x] Meat",
			items: []cleanup.Item{item("Costco", "Meat")},
			want:  "- [ ] Costco\n  - [ ] Meat",
		},
		{
			name:  "a childless item matching a done sub-item reopens it and its parent",
			body:  "- [x] Costco\n  - [x] Meat\n- [ ] Milk",
			items: []cleanup.Item{item("meat")},
			want:  "- [ ] Costco\n  - [ ] Meat\n- [ ] Milk",
		},
		{
			name:  "an insertion never crosses a marker",
			body:  marker + "\n- [ ] Costco\n  - [ ] Meat\n\n" + service.CaptureMarker("c_2") + "\n- [ ] Milk",
			items: []cleanup.Item{item("Costco", "Rice")},
			want:  marker + "\n- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice\n\n" + service.CaptureMarker("c_2") + "\n- [ ] Milk",
		},
		{
			name:  "matching folds case and spacing, never indent",
			body:  "- [ ]  costco \n  - [ ] MEAT",
			items: []cleanup.Item{item("Costco", "meat", "Olive oil")},
			want:  "- [ ]  costco \n  - [ ] MEAT\n  - [ ] Olive oil",
		},
		{
			name:  "a parent's words as a sub-item elsewhere do not make it a parent",
			body:  "- [ ] Errands\n  - [ ] Costco",
			items: []cleanup.Item{item("Costco", "Meat")},
			want:  "- [ ] Errands\n  - [ ] Costco",
			rest:  "- [ ] Costco\n  - [ ] Meat",
		},
		{
			name:  "order of rest is the recording's order",
			body:  "- [ ] Milk",
			items: []cleanup.Item{item("Bread"), item("Milk"), item("Walmart", "Eggs")},
			want:  "- [ ] Milk",
			rest:  "- [ ] Bread\n- [ ] Walmart\n  - [ ] Eggs",
		},
		{
			name:  "an empty body merges nothing",
			body:  "",
			items: []cleanup.Item{item("Milk")},
			want:  "",
			rest:  "- [ ] Milk",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, _ := mergeChecklistItems(tc.body, tc.items)
			if got != tc.want {
				t.Errorf("body:\n%s\nwant:\n%s", got, tc.want)
			}
			if r := checklistItems(cleanup.RenderItems(rest)); r != tc.rest {
				t.Errorf("rest:\n%s\nwant:\n%s", r, tc.rest)
			}
			// Ticks are never added by a merge: the count of [x] never rises.
			if strings.Count(got, "[x]") > strings.Count(tc.body, "[x]") {
				t.Errorf("merge added a tick")
			}
		})
	}
}

// The merge in the pipeline: a second recording about Costco joins the
// Costco the list has instead of starting a second one, a milk already
// listed is not listed twice, a ticked one is wanted again, and what is
// left goes under the recording's marker. A recording whose every item
// joined leaves a bare marker. Regenerating the first recording keeps the
// child the second one merged under its parent.
func TestASecondRecordingAboutAnExistingParentJoinsIt(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "chicken from costco and milk and eggs"},
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{item("Costco", "Chicken"), item("Milk"), item("Eggs")}},
	})
	seedChecklistCapture(t, h, "list1", nil)
	ctx := context.Background()
	before := service.CaptureMarker("c_0") + "\n- [ ] Costco\n  - [ ] Meat\n- [ ] Milk\n- [x] Eggs"
	if err := h.objects.Put(ctx, "tenants/user1/notes/list1/note.md", []byte(before), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	capture, body := runChecklistCapture(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("capture = %s (%s), want appended", capture.Status, capture.Error)
	}
	want := service.CaptureMarker("c_0") + "\n- [ ] Costco\n  - [ ] Meat\n  - [ ] Chicken\n- [ ] Milk\n- [ ] Eggs\n" + service.CaptureMarker("c_1")
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	// The interrupted-attempt check sees the merged body as written.
	if written, err := h.pipeline.paragraphInNote(ctx, "tenants/user1/notes/list1/note.md", "c_1", "- [ ] Costco\n  - [ ] Chicken\n- [ ] Milk\n- [ ] Eggs", nil, cleanup.ItemsFromLines("Costco\n  Chicken\nMilk\nEggs")); err != nil || !written {
		t.Errorf("paragraphInNote after the merge = %v, %v; want written", written, err)
	}
	// Regenerating c_0 (Costco › Meat again) keeps the merged Chicken.
	next := replaceChecklistItems(body, "c_0", []string{"Costco", "  Meat"}, "- [ ] Costco\n  - [ ] Meat")
	if next != body {
		t.Errorf("regenerating c_0 changed the body:\n%s", next)
	}
}

// One recording, part of it merged and the rest under its marker, appended
// as every append is: the marker after a blank line, the rest under it.
func TestARecordingPartlyMergedPutsTheRestUnderItsMarker(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "milk and bread"},
		llm: &fake.LLM{ItemsResponse: []cleanup.Item{item("Milk"), item("Bread")}},
	})
	seedChecklistCapture(t, h, "list1", nil)
	ctx := context.Background()
	if err := h.objects.Put(ctx, "tenants/user1/notes/list1/note.md", []byte("- [x] Milk\n- [ ] Typed"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	_, body := runChecklistCapture(t, h)
	if want := "- [ ] Milk\n- [ ] Typed\n\n" + service.CaptureMarker("c_1") + "\n- [ ] Bread"; body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}
