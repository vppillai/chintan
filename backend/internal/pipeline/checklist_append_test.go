package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
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
		{"CRLF keeps its line ends", "- [x] milk\r\n- [ ] eggs\r", "- [ ]Milk\r\n- [ ] eggs\r", "- [x] Milk\r\n- [ ] eggs\r"},
		{"Malayalam: a changed vowel is other words", "- [x] പാൽ\n- [x] ചായ", "- [ ] പുൽ\n- [ ] ചായ\n- [ ] x", "- [ ] പുൽ\n- [x] ചായ\n- [ ] x"},
		{"Hindi: a changed vowel is other words", "- [x] दाल\n- [x] चाय", "- [ ] दिल\n- [ ] चाय\n- [ ] x", "- [ ] दिल\n- [x] चाय\n- [ ] x"},
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
			// Vowel signs are marks; folded as breaks, പാൽ (milk) and പുൽ
			// (grass), and दाल and दिल, were one item and the new one was
			// dropped as a duplicate (R7-2).
			name:  "Malayalam: another vowel is another item; the same word is a duplicate",
			body:  "- [ ] പാൽ",
			items: []cleanup.Item{item("പുൽ"), item("പാൽ")},
			want:  "- [ ] പാൽ",
			rest:  "- [ ] പുൽ",
		},
		{
			name:  "Hindi: another vowel is another item; the same word is a duplicate",
			body:  "- [ ] दाल",
			items: []cleanup.Item{item("दिल"), item("दाल")},
			want:  "- [ ] दाल",
			rest:  "- [ ] दिल",
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

// A checklist recording's first append was written and the worker died
// before it could mark the capture appended, and a later recording has since
// merged a child under this recording's parent (Meat under c_1's Costco, by
// c_2 — the ring's same-second batches are exactly this shape). Lambda's
// retry inside the lease finds the words in the note and finishes the
// attempt without a write; the owner's Retry after the lease takes the claim
// over and re-appends by words, not by cutting the paragraph, which runs to
// the next marker and took Meat with it (review 2026-09-29, DB6-1). Either
// way the body is the same body.
func TestARetryOfAnInterruptedFirstChecklistAppendKeepsASiblingsMergedChild(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{ItemsResponse: []cleanup.Item{item("Costco", "Chicken")}}})
	seedChecklistCapture(t, h, "list1", nil)
	ctx := context.Background()
	const noteKey = "tenants/user1/notes/list1/note.md"
	body := service.CaptureMarker("c_1") + "\n- [ ] Costco\n  - [ ] Chicken\n  - [ ] Meat\n" + service.CaptureMarker("c_2")
	if err := h.objects.Put(ctx, noteKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	// The dead attempt's state: transcript and items stored, the claim taken
	// d ago, the capture not marked appended.
	cleanKey, err := keys.CaptureClean("user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	const rawKey = "tenants/user1/captures/c_1/raw.txt"
	for key, text := range map[string]string{rawKey: "chicken from costco", cleanKey: "Costco\n  Chicken"} {
		if err := h.objects.Put(ctx, key, []byte(text), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	claimedAgo := func(d time.Duration) {
		c, err := h.store.GetCapture(ctx, "user1", "c_1")
		if err != nil {
			t.Fatal(err)
		}
		c.Status, c.Language, c.RawKey, c.CleanKey = model.StatusAppending, "en", rawKey, cleanKey
		c.AppendToken, c.AppendClaimedAt, c.AppendedAt = appendToken("c_1", cleanKey), time.Now().Add(-d).Unix(), 0
		if _, err := h.store.PutCapture(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		ago  time.Duration
	}{
		{"the retry inside the lease finishes the attempt", 0},
		{"the retry after the lease takes the claim over", repository.AppendClaimLease + time.Minute},
	} {
		claimedAgo(tc.ago)
		capture, err := h.pipeline.Run(ctx, "user1", "c_1")
		if err != nil || capture.Status != model.StatusAppended {
			t.Fatalf("%s: Run = %v, status %s (%s); want appended", tc.name, err, capture.Status, capture.Error)
		}
		if got, _ := h.objects.Get(ctx, noteKey); string(got) != body {
			t.Fatalf("%s: body\n%s\nwant it unchanged, Meat kept:\n%s", tc.name, got, body)
		}
	}
	if n := len(h.llm.ItemsCalls()); n != 0 {
		t.Errorf("extraction calls = %d, want none: the items were stored", n)
	}
}

// Three levels (round 8, F1): a ticked third-level line named again is
// wanted again, and so is every group it stands under — a done item has no
// open descendant. Before, only a sub-item's parent was reopened, and only a
// top-level one.
func TestMergeLeafReopensEveryAncestor(t *testing.T) {
	const body = "- [x] Party\n  - [x] Costco\n    - [x] Milk\n- [ ] Bread"
	got, rest, counts := mergeChecklistItems(body, []cleanup.Item{item("milk")})
	if want := "- [ ] Party\n  - [ ] Costco\n    - [ ] Milk\n- [ ] Bread"; got != want || len(rest) != 0 || counts.reopened != 1 {
		t.Errorf("merge = %q, rest %+v, %+v; want %q", got, rest, counts, want)
	}
}

// A recording's parent joins a top-level line and its children are that
// line's direct sub-items: a third-level line with a child's words is a
// different thing in a different group, so the child is added rather than
// taken for it. New children go after the block's last line, whatever its
// depth.
func TestMergeParentMatchesDirectChildrenOnly(t *testing.T) {
	const body = "- [ ] Costco\n  - [ ] Party\n    - [x] Meat\n- [ ] Milk"
	got, _, _ := mergeChecklistItems(body, []cleanup.Item{item("Costco", "Meat", "Party")})
	if want := "- [ ] Costco\n  - [ ] Party\n    - [x] Meat\n  - [ ] Meat\n- [ ] Milk"; got != want {
		t.Errorf("merge = %q, want %q", got, want)
	}
}

// parentOf is the nearest shallower item line above, across a marker or a
// blank, as the editor reads the body.
func TestParentOfAtDepthTwo(t *testing.T) {
	lines := strings.Split("- [ ] Party\n  - [ ] Costco\n"+service.CaptureMarker("c_1")+"\n    - [ ] Plates\n  - [ ] Candles\n- [ ] Milk", "\n")
	for i, want := range []int{-1, 0, -1, 1, 0, -1} {
		if got := parentOf(lines, i); got != want {
			t.Errorf("parentOf(%d) = %d, want %d", i, got, want)
		}
	}
}

// Two spaces per level, up to the third: a deeper line is clamped, not
// dropped.
func TestChecklistItemsKeepsTwoLevelsOfIndent(t *testing.T) {
	if got, want := checklistItems("Party\n  Costco\n    Plates\n      Paper ones"), "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n    - [ ] Paper ones"; got != want {
		t.Errorf("checklistItems = %q, want %q", got, want)
	}
}

// Regenerating a recording whose sub-item holds a third-level line typed by
// hand: the sub-item has a line under it that is not the recording's, so it
// stays with that line, and the list comes back as it was. Before, the
// sub-item came out and the typed line was left under the next item.
func TestReplaceChecklistItemsKeepsAThreeLevelBlock(t *testing.T) {
	body := service.CaptureMarker("c_1") + "\n- [ ] Costco\n  - [ ] Meat\n    - [ ] Chicken thighs\n- [ ] Milk"
	got := replaceChecklistItems(body, "c_1", []string{"Costco", "  Meat", "Milk"}, "- [ ] Costco\n  - [ ] Meat\n- [ ] Milk")
	if got != body {
		t.Errorf("replaceChecklistItems = %q, want the body unchanged %q", got, body)
	}
}

// Review of #191: when every old line stays as a shared parent (here both
// Costco and Meat, because of the line typed under Meat), the recording's
// new words still go in, after the shared parent's block (DB6-7). Before,
// no line was taken, the body came back unchanged and Rice was lost.
func TestReplaceChecklistItemsWritesANewChildWhenEveryOldLineIsShared(t *testing.T) {
	body := service.CaptureMarker("c_1") + "\n- [ ] Costco\n  - [ ] Meat\n    - [ ] Chicken thighs\n- [ ] Milk"
	got := replaceChecklistItems(body, "c_1", []string{"Costco", "  Meat"}, "- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice")
	if want := service.CaptureMarker("c_1") + "\n- [ ] Costco\n  - [ ] Meat\n    - [ ] Chicken thighs\n  - [ ] Rice\n- [ ] Milk"; got != want {
		t.Errorf("replaceChecklistItems = %q, want %q", got, want)
	}
	// A new top-level item goes right after the shared block.
	got = replaceChecklistItems(body, "c_1", []string{"Costco", "  Meat"}, "- [ ] Costco\n  - [ ] Meat\n- [ ] Bread")
	if want := service.CaptureMarker("c_1") + "\n- [ ] Costco\n  - [ ] Meat\n    - [ ] Chicken thighs\n- [ ] Bread\n- [ ] Milk"; got != want {
		t.Errorf("replaceChecklistItems = %q, want %q", got, want)
	}
}

// DB6-9 is the owner's and stays open: a blank line or a marker ends a
// parent's block for the merge, as before three levels. This pins the known
// cost, so a change to it is a decision and not an accident: the Rice under
// the later recording's marker is not found, and a second one is added.
func TestMergeParentBlockStillEndsAtABlankLine(t *testing.T) {
	body := "- [ ] Costco\n  - [ ] Meat\n\n" + service.CaptureMarker("c_2") + "\n  - [ ] Rice"
	got, _, _ := mergeChecklistItems(body, []cleanup.Item{item("Costco", "Rice")})
	if want := "- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice\n\n" + service.CaptureMarker("c_2") + "\n  - [ ] Rice"; got != want {
		t.Errorf("merge = %q, want %q", got, want)
	}
}
