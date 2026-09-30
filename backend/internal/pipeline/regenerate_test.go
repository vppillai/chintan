package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// seedTranscribedInto writes an appended recording the way the pipeline
// leaves one: its transcript at raw.txt (and at routed.txt when routed is
// set), its clean artefact, and its paragraph under its marker in the note.
func seedTranscribedInto(t *testing.T, h *harness, note model.NoteIndex, captureID, raw, routed, clean string) {
	t.Helper()
	ctx := context.Background()
	seedAppendedInto(t, h, note, captureID, clean)
	c, err := h.store.GetCapture(ctx, "user1", captureID)
	if err != nil {
		t.Fatal(err)
	}
	c.RawKey = "tenants/user1/captures/" + captureID + "/raw.txt"
	c.CleanKey = "tenants/user1/captures/" + captureID + "/clean.txt"
	if err := h.objects.Put(ctx, c.RawKey, []byte(raw), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if routed != "" {
		c.RoutedKey = "tenants/user1/captures/" + captureID + "/routed.txt"
		if err := h.objects.Put(ctx, c.RoutedKey, []byte(routed), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.objects.Put(ctx, c.CleanKey, []byte(clean), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutCapture(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func regenerate(t *testing.T, h *harness, noteID string) int {
	t.Helper()
	svc := service.NewNotesService(h.store, h.objects).WithInvoker(directInvoker{h.pipeline})
	n, err := svc.RequestRegenerate(context.Background(), "user1", noteID)
	if err != nil {
		t.Fatalf("RequestRegenerate: %v", err)
	}
	return n
}

// A plain note: every recording's paragraph is cleaned again from its stored
// transcript — the routed one where the router left one — and replaced by its
// marker, in place; nothing is transcribed; a paragraph the person rewrote
// into their own words (its marker carried to the end) is left alone and not
// counted; the title is untouched; the cleaned view, since the note has one,
// is regenerated once at the end.
func TestRegenerateRecleansEachParagraphInPlaceWithoutTranscribing(t *testing.T) {
	llmFake := &fake.LLM{Response: "Cleaned with the new prompt.", NoteResponse: "# Roof\n\nThe new view."}
	h := newHarness(t, harnessOpts{llm: llmFake})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "note1", Title: "Roof", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
		S3MetaKey:     "tenants/user1/notes/note1/meta.json",
		CleanedBody:   "# Roof\n\nThe old view.", CleanedMode: model.NoteCleanStructured, CleanedAt: model.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTranscribedInto(t, h, note, "c_1", "um the gutter leaks and on past the twelve words that a tidy would take", "", "Um, the gutter leaks.")
	seedTranscribedInto(t, h, note, "c_2", "add this to roof call the roofer and on past the twelve words that a tidy would take", "call the roofer and on past the twelve words that a tidy would take", "Call the roofer.")
	seedTranscribedInto(t, h, note, "c_3", "the ridge tiles and on past the twelve words that a tidy would take", "", "The ridge tiles.")
	// The person rewrote c_3's paragraph into their own words at the top of
	// the note: the editor carried its marker to the end with nothing under
	// it. (A recording's paragraph runs to the next marker, so words typed
	// between two recordings belong to the earlier one and are replaced with
	// it, as Transcribe again replaces them; the confirm says so.)
	body := "My own words about the ridge.\n\n" + service.CaptureMarker("c_1") + "\nUm, the gutter leaks.\n\n" + service.CaptureMarker("c_2") + "\nCall the roofer.\n" + service.CaptureMarker("c_3")
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	if n := regenerate(t, h, "note1"); n != 2 {
		t.Fatalf("regenerated %d recordings, want 2 (c_3's words are the person's now)", n)
	}

	got, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	want := "My own words about the ridge.\n\n" + service.CaptureMarker("c_1") + "\nCleaned with the new prompt.\n\n" + service.CaptureMarker("c_2") + "\nCleaned with the new prompt.\n" + service.CaptureMarker("c_3")
	if string(got) != want {
		t.Fatalf("body after regeneration:\n%s\nwant:\n%s", got, want)
	}
	if n := len(h.stt.Sources); n != 0 {
		t.Errorf("stt calls = %d, want none: the transcript is kept", n)
	}
	if n := llmFake.Calls(); n != 2 {
		t.Errorf("cleanup calls = %d, want one per recording", n)
	}
	for _, id := range []string{"c_1", "c_2", "c_3"} {
		c, err := h.store.GetCapture(ctx, "user1", id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != model.StatusAppended || c.AppendedAt == 0 {
			t.Errorf("%s = %s (%s), want appended", id, c.Status, c.Error)
		}
	}
	after := mustGetNote(t, h.store, "user1", "note1")
	if after.Title != "Roof" {
		t.Errorf("title = %q, want unchanged", after.Title)
	}
	if after.CleanedBody != "# Roof\n\nThe new view." || after.CleanedStale {
		t.Errorf("cleaned view = %q stale=%v, want regenerated from the new body", after.CleanedBody, after.CleanedStale)
	}
	if calls := llmFake.NoteCalls(); len(calls) != 1 {
		t.Errorf("whole-note calls = %d, want exactly one, after the last recording", len(calls))
	}
}

// A checklist that has been ticked and reordered holds every recording's
// items under nobody's marker. Regeneration finds each recording's previous
// items by their words, swaps the new ones in where the first of them stood
// — the tick following its words — and leaves typed items and the other
// recordings' items where they are. A recording whose items the person had
// deleted gets nothing put back.
func TestRegenerateAChecklistReplacesEachRecordingsItemsByTheirWordsAndKeepsTicks(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{}})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "list1", Title: "Shopping list", Kind: model.NoteKindChecklist, UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/list1/note.md",
		S3MetaKey:     "tenants/user1/notes/list1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	// c_1 said "eggs and milk" and produced Milk, Eggs (the old prompt's
	// order); the person ticked Milk. c_2's Bread was deleted by hand.
	seedTranscribedInto(t, h, note, "c_1", "eggs and milk and butter", "", "Milk\nEggs")
	seedTranscribedInto(t, h, note, "c_2", "bread", "", "Bread")
	body := "- [x] Milk\n- [ ] Typed by hand\n- [ ] Eggs\n" + service.CaptureMarker("c_1") + "\n" + service.CaptureMarker("c_2")
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	if n := regenerate(t, h, "list1"); n != 2 {
		t.Fatalf("regenerated %d recordings, want 2", n)
	}

	got, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	want := "- [ ] eggs\n- [x] milk\n- [ ] butter\n- [ ] Typed by hand\n" + service.CaptureMarker("c_1") + "\n" + service.CaptureMarker("c_2")
	if string(got) != want {
		t.Fatalf("checklist after regeneration:\n%s\nwant:\n%s", got, want)
	}
	if n := h.llm.Calls(); n != 0 {
		t.Errorf("transcript cleanup was called %d time(s); a checklist extracts items", n)
	}
	if calls := h.llm.ItemsCalls(); len(calls) != 2 || calls[0].Transcript != "eggs and milk and butter" {
		t.Errorf("items calls = %+v, want one per recording over the raw transcript", calls)
	}
	n := mustGetNote(t, h.store, "user1", "list1")
	if !strings.Contains(n.SearchText, "butter") {
		t.Errorf("index did not follow the body: search=%q", n.SearchText)
	}
}

// A recording whose transcript, extracted again, names nothing to add is
// no_content, and its earlier items come out of the list.
func TestRegenerateWithdrawsTheItemsOfARecordingThatNowAddsNothing(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{ItemsResponse: []cleanup.Item{}}})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "list1", Title: "Shopping list", Kind: model.NoteKindChecklist, UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/list1/note.md",
		S3MetaKey:     "tenants/user1/notes/list1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTranscribedInto(t, h, note, "c_1", "create a shopping list", "", "create a shopping list")
	body := "- [ ] create a shopping list\n- [ ] Typed by hand\n" + service.CaptureMarker("c_1")
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	regenerate(t, h, "list1")

	got, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	if want := "- [ ] Typed by hand\n" + service.CaptureMarker("c_1"); string(got) != want {
		t.Fatalf("checklist after regeneration:\n%s\nwant:\n%s", got, want)
	}
	c, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusNoContent {
		t.Errorf("status = %s, want no_content", c.Status)
	}
}

// The task from the worker's side: the payload names the note and the ids
// the API reset; a recording that is not at transcribed — landed by an
// earlier attempt of the same task — is skipped, so a Lambda retry bills
// nothing twice; and a task with no ids chooses and resets the recordings
// itself, which is how chintanctl sends it.
func TestRegenerateTaskSkipsWhatIsDoneAndChoosesForItselfWhenGivenNoIds(t *testing.T) {
	llmFake := &fake.LLM{Response: "Again."}
	h := newHarness(t, harnessOpts{llm: llmFake})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "note1", Title: "Roof", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
		S3MetaKey:     "tenants/user1/notes/note1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTranscribedInto(t, h, note, "c_1", "one and on past the twelve words that a tidy would take", "", "One.")
	seedTranscribedInto(t, h, note, "c_2", "two and on past the twelve words that a tidy would take", "", "Two.")

	// The API reset only c_2; c_1 is appended and must be left alone even
	// though the payload names it.
	c2, _ := h.store.GetCapture(ctx, "user1", "c_2")
	service.ResetForRegenerate(&c2, h.clock.Now())
	if _, err := h.store.PutCapture(ctx, c2); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Invocation{Task: TaskRegenerateNote, TenantID: "user1", NoteID: "note1", CaptureIDs: []string{"c_1", "c_2"}})
	if err := NewWorker(h.pipeline).Handle(ctx, raw); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	if want := service.CaptureMarker("c_1") + "\nOne.\n\n" + service.CaptureMarker("c_2") + "\nAgain."; string(got) != want {
		t.Fatalf("body after the task:\n%s\nwant:\n%s", got, want)
	}
	if n := llmFake.Calls(); n != 1 {
		t.Errorf("cleanup calls = %d, want one: c_1 was not reset", n)
	}

	// No ids: the operator's road.
	raw, _ = json.Marshal(Invocation{Task: TaskRegenerateNote, TenantID: "user1", NoteID: "note1"})
	if err := NewWorker(h.pipeline).Handle(ctx, raw); err != nil {
		t.Fatalf("Handle (no ids): %v", err)
	}
	got, _ = h.objects.Get(ctx, note.S3MarkdownKey)
	if want := service.CaptureMarker("c_1") + "\nAgain.\n\n" + service.CaptureMarker("c_2") + "\nAgain."; string(got) != want {
		t.Fatalf("body after the operator's task:\n%s\nwant:\n%s", got, want)
	}
	if n := llmFake.Calls(); n != 3 {
		t.Errorf("cleanup calls = %d, want three in all", n)
	}
}

// keepTick's two carries, pinned on their own.
func TestKeepTickFollowsTheWordsThenTheLine(t *testing.T) {
	cases := []struct{ name, old, text, want string }{
		{"by words, reordered", "- [x] Milk\n- [ ] Eggs", "- [ ] Eggs\n- [ ] Milk\n- [ ] Butter", "- [ ] Eggs\n- [x] Milk\n- [ ] Butter"},
		{"by line when no words match and the count holds", "- [ ] one\n- [x] two", "- [ ] uno\n- [ ] dos", "- [ ] uno\n- [x] dos"},
		{"nothing when neither holds", "- [ ] one\n- [x] two", "- [ ] uno", "- [ ] uno"},
		{"a tick is carried once", "- [x] Milk", "- [ ] Milk\n- [ ] Milk", "- [x] Milk\n- [ ] Milk"},
		{"whitespace does not break the match", "  - [x] Two  loaves", "- [ ] Two loaves", "- [x] Two loaves"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepTick(tc.old, tc.text); got != tc.want {
				t.Errorf("keepTick(%q, %q) = %q, want %q", tc.old, tc.text, got, tc.want)
			}
		})
	}
}

// failOnceOnPutIfMatch fails the first conditional write of one key — the
// append's body write (service.RewriteNoteBody) — and counts the ones after
// it, so a test can induce the fault an S3 5xx is and then read how many
// times the body was written once the fault cleared.
type failOnceOnPutIfMatch struct {
	repository.Objects
	key    string
	mu     sync.Mutex
	failed bool
	puts   int
}

func (o *failOnceOnPutIfMatch) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if key == o.key {
		o.mu.Lock()
		first := !o.failed
		o.failed = true
		if !first {
			o.puts++
		}
		o.mu.Unlock()
		if first {
			return errInducedObjectFault
		}
	}
	return o.Objects.PutIfMatch(ctx, key, body, contentType, etag)
}

func (o *failOnceOnPutIfMatch) writes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.puts
}

func seedChecklistForRegenerate(t *testing.T, h *harness, noteKey string) model.NoteIndex {
	t.Helper()
	note, err := h.store.PutNote(context.Background(), "user1", model.NoteIndex{
		ID: "list1", Title: "Shopping list", Kind: model.NoteKindChecklist, UpdatedAt: model.Now(),
		S3MarkdownKey: noteKey,
		S3MetaKey:     "tenants/user1/notes/list1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	return note
}

// The append can fail after the extraction has overwritten the recording's
// clean artefact — an object store fault here — and the claim is handed back
// with the row at `appending`. Until 2026-09-27 the attempt that resumed
// there had no record of which lines were the recording's and put the new
// items in beside the old ones. The extraction keeps a copy of the old items
// (clean.prev.txt) and the resume reads it: the strip's Retry replaces the
// old lines exactly once, without a second extraction; and an attempt that
// finds the list already rewritten — its own earlier try that wrote the
// block and died before it could say so — finishes without writing.
func TestAChecklistAppendResumedAfterAFailureReplacesTheOldItemsExactlyOnce(t *testing.T) {
	const noteKey = "tenants/user1/notes/list1/note.md"
	objects := &failOnceOnPutIfMatch{Objects: memory.NewObjects(), key: noteKey}
	h := newHarness(t, harnessOpts{objects: objects, llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "eggs"}, {Text: "milk"}, {Text: "butter"}}}})
	ctx := context.Background()
	note := seedChecklistForRegenerate(t, h, noteKey)
	seedTranscribedInto(t, h, note, "c_1", "eggs and milk and butter", "", "Milk\nEggs")
	body := "- [x] Milk\n- [ ] Typed by hand\n- [ ] Eggs\n" + service.CaptureMarker("c_1")
	if err := h.objects.Put(ctx, noteKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	// The request path's reset, then the worker's task: the extraction
	// lands and the body write fails.
	c1, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	service.ResetForRegenerate(&c1, h.clock.Now())
	if _, err := h.store.PutCapture(ctx, c1); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Invocation{Task: TaskRegenerateNote, TenantID: "user1", NoteID: "list1", CaptureIDs: []string{"c_1"}})
	if err := NewWorker(h.pipeline).Handle(ctx, raw); err == nil {
		t.Fatal("the task succeeded through an induced body-write fault")
	}
	c1, _ = h.store.GetCapture(ctx, "user1", "c_1")
	if c1.Status != model.StatusAppending || c1.AppendToken != "" || c1.CleanKey == "" {
		t.Fatalf("after the fault: status=%s token=%q clean=%q; want appending with the claim handed back and the new items stored", c1.Status, c1.AppendToken, c1.CleanKey)
	}
	if got, _ := h.objects.Get(ctx, noteKey); string(got) != body {
		t.Fatalf("the failed write changed the body:\n%s", got)
	}

	// The strip's Retry: run resumes at the append with the copy.
	final, err := h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil || final.Status != model.StatusAppended {
		t.Fatalf("Retry = %s (%s), %v; want appended", final.Status, final.Error, err)
	}
	want := "- [ ] eggs\n- [x] milk\n- [ ] butter\n- [ ] Typed by hand\n" + service.CaptureMarker("c_1")
	if got, _ := h.objects.Get(ctx, noteKey); string(got) != want {
		t.Fatalf("checklist after the resumed append:\n%s\nwant:\n%s", got, want)
	}
	if calls := h.llm.ItemsCalls(); len(calls) != 1 {
		t.Errorf("items calls = %d, want the one extraction: the resume does not bill again", len(calls))
	}

	// The attempt that wrote the block and died before it could say so: the
	// row at appending under its own fresh claim, the body already rewritten.
	// The retry inside the lease sees its words are in and finishes the
	// bookkeeping; the body is not written again.
	c1, _ = h.store.GetCapture(ctx, "user1", "c_1")
	c1.Status, c1.AppendedAt = model.StatusAppending, 0
	c1.AppendToken, c1.AppendClaimedAt = appendToken("c_1", c1.CleanKey), h.clock.Now().Unix()
	if _, err := h.store.PutCapture(ctx, c1); err != nil {
		t.Fatal(err)
	}
	writes := objects.writes()
	final, err = h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil || final.Status != model.StatusAppended {
		t.Fatalf("retry inside the lease = %s (%s), %v; want appended", final.Status, final.Error, err)
	}
	if got, _ := h.objects.Get(ctx, noteKey); string(got) != want {
		t.Fatalf("checklist after the retry inside the lease:\n%s\nwant:\n%s", got, want)
	}
	if n := objects.writes() - writes; n != 0 {
		t.Errorf("the body was written %d time(s) for items already in the list", n)
	}
}

// The task's own retry: Lambda re-delivers a regenerate-note whose run hit a
// fault, and a recording left mid-way — at appending with its claim handed
// back, or cleaned and never appended — is finished by it rather than skipped
// for the strip's Retry to find a quarter of an hour on; the ones never
// started are run; the cleanup is not billed again for a row whose artefact
// is stored.
func TestRegenerateTaskRetryFinishesTheRecordingsLeftMidWay(t *testing.T) {
	const noteKey = "tenants/user1/notes/note1/note.md"
	objects := &failOnceOnPutIfMatch{Objects: memory.NewObjects(), key: noteKey}
	llmFake := &fake.LLM{Response: "Again."}
	h := newHarness(t, harnessOpts{objects: objects, llm: llmFake})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "note1", Title: "Roof", UpdatedAt: model.Now(),
		S3MarkdownKey: noteKey, S3MetaKey: "tenants/user1/notes/note1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTranscribedInto(t, h, note, "c_1", "one and on past the twelve words that a tidy would take", "", "One.")
	seedTranscribedInto(t, h, note, "c_2", "two and on past the twelve words that a tidy would take", "", "Two.")
	seedTranscribedInto(t, h, note, "c_3", "three and on past the twelve words that a tidy would take", "", "Three.")
	for _, id := range []string{"c_1", "c_2", "c_3"} {
		c, _ := h.store.GetCapture(ctx, "user1", id)
		service.ResetForRegenerate(&c, h.clock.Now())
		if id == "c_3" {
			// An earlier attempt cleaned it and died before the append.
			c.Status, c.CleanKey = model.StatusCleaned, "tenants/user1/captures/c_3/clean.txt"
			if err := h.objects.Put(ctx, c.CleanKey, []byte("Three, again."), "text/plain"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.store.PutCapture(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(Invocation{Task: TaskRegenerateNote, TenantID: "user1", NoteID: "note1", CaptureIDs: []string{"c_1", "c_2", "c_3"}})
	if err := NewWorker(h.pipeline).Handle(ctx, raw); err == nil {
		t.Fatal("the task succeeded through an induced body-write fault")
	}
	if c, _ := h.store.GetCapture(ctx, "user1", "c_1"); c.Status != model.StatusAppending {
		t.Fatalf("c_1 after the fault = %s, want appending", c.Status)
	}

	// Lambda's retry, same payload.
	if err := NewWorker(h.pipeline).Handle(ctx, raw); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, _ := h.objects.Get(ctx, noteKey)
	want := service.CaptureMarker("c_1") + "\nAgain.\n\n" + service.CaptureMarker("c_2") + "\nAgain.\n\n" + service.CaptureMarker("c_3") + "\nThree, again."
	if string(got) != want {
		t.Fatalf("body after the retry:\n%s\nwant:\n%s", got, want)
	}
	for _, id := range []string{"c_1", "c_2", "c_3"} {
		if c, _ := h.store.GetCapture(ctx, "user1", id); c.Status != model.StatusAppended {
			t.Errorf("%s = %s, want appended", id, c.Status)
		}
	}
	if n := llmFake.Calls(); n != 2 {
		t.Errorf("cleanup calls = %d, want two: c_1 once before the fault, c_2 once, c_3 never (its artefact was stored)", n)
	}
}

// replaceChecklistItems on its own: a second pass over a list the first
// rewrote returns it unchanged, whatever words the old and new items share;
// the person's deletion of a recording's items stands even when they typed
// one of the new words; a sub-item is found by its words and the block goes
// back at the top level; a parent the recording shares is left standing; a
// sub-item of a block that stays is left in it.
// Every case is run twice, the second pass over the first's result, since
// paragraphInNote asks exactly that of an interrupted attempt.
func TestReplaceChecklistItemsIsIdempotentAndRespectsADeletion(t *testing.T) {
	m, m2 := service.CaptureMarker("c_1"), service.CaptureMarker("c_2")
	prev, shared := []string{"Milk", "Eggs"}, []string{"Costco", "  Chicken"}
	text := "- [ ] eggs\n- [ ] milk\n- [ ] butter"
	first := replaceChecklistItems("- [x] Milk\n- [ ] Typed\n- [ ] Eggs\n"+m, "c_1", prev, text)
	if want := "- [ ] eggs\n- [x] milk\n- [ ] butter\n- [ ] Typed\n" + m; first != want {
		t.Fatalf("first pass:\n%s\nwant:\n%s", first, want)
	}
	if again := replaceChecklistItems(first, "c_1", prev, text); again != first {
		t.Errorf("a second pass changed the list:\n%s", again)
	}
	cases := []struct {
		name, body string
		prev       []string
		text, want string
	}{
		{"landed with no words in common reads as the person's deletion", "- [ ] butter\n- [ ] Typed\n" + m, []string{"Bread"}, "- [ ] butter", "- [ ] butter\n- [ ] Typed\n" + m},
		{"a deletion is not overruled by a typed line with a new item's words", "- [ ] butter\n" + m, prev, text, "- [ ] butter\n" + m},
		{"a sub-item is matched by its words and the block is written flat", "- [ ] Typed\n  - [x] Milk\n  - [ ] Eggs\n" + m, prev, text, "- [ ] Typed\n- [ ] eggs\n- [x] milk\n- [ ] butter\n" + m},
		{"a typed duplicate of a new item folds into the block", "- [ ] Milk\n- [ ] butter\n- [ ] Typed\n" + m, prev, text, "- [ ] eggs\n- [ ] milk\n- [ ] butter\n- [ ] Typed\n" + m},
		// A parent this recording joined (its Costco, another's Meat under
		// it) is not its to take: the parent and the other child stay,
		// whatever the recording says now — another grouping, or nothing.
		{"a shared parent stays when the recording's child leaves it", m + "\n- [ ] Costco\n  - [ ] Meat\n  - [ ] Chicken\n" + m2, shared, "- [ ] Chicken", m + "\n- [ ] Costco\n  - [ ] Meat\n- [ ] Chicken\n" + m2},
		{"a shared parent stays when the recording now names nothing", m + "\n- [ ] Costco\n  - [ ] Meat\n  - [ ] Chicken\n" + m2, shared, "", m + "\n- [ ] Costco\n  - [ ] Meat\n" + m2},
		{"the same words again keep the block's order and the tick", m + "\n- [ ] Costco\n  - [ ] Meat\n  - [x] Chicken\n  - [ ] Beef\n" + m2, shared, "- [ ] Costco\n  - [ ] Chicken", m + "\n- [ ] Costco\n  - [ ] Meat\n  - [x] Chicken\n  - [ ] Beef\n" + m2},
		{"a done shared parent stays done when its carried child is done", m + "\n- [x] Costco\n  - [x] Meat\n  - [x] Chicken\n" + m2, shared, "- [ ] Costco\n  - [ ] Chicken", m + "\n- [x] Costco\n  - [x] Meat\n  - [x] Chicken\n" + m2},
		{"a new child beside the carried one reopens the done shared parent", m + "\n- [x] Costco\n  - [x] Meat\n  - [x] Chicken\n" + m2, shared, "- [ ] Costco\n  - [ ] Chicken\n  - [ ] Beef", m + "\n- [ ] Costco\n  - [x] Meat\n  - [x] Chicken\n  - [ ] Beef\n" + m2},
		// A sub-item with a new item's words under a parent that stays —
		// typed there, or another recording's — is that block's line: the
		// merge finds it in place, and it is not pulled out and written flat
		// in the recording's block above its own marker (review 2026-09-29,
		// DB6-8). Under the recording's own parent it comes out with the
		// parent and goes back in the block, not doubled.
		{"a fresh word under a parent the recording does not own stays in its block", "- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice\n\n" + m + "\n- [ ] Milk", []string{"Milk"}, "- [ ] Rice\n- [ ] Milk", "- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice\n\n" + m + "\n- [ ] Milk"},
		{"a fresh word under the recording's own parent comes out with it", m + "\n- [ ] Costco\n  - [ ] Chicken\n  - [ ] Rice", shared, "- [ ] Costco\n  - [ ] Chicken\n  - [ ] Rice", m + "\n- [ ] Costco\n  - [ ] Chicken\n  - [ ] Rice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replaceChecklistItems(tc.body, "c_1", tc.prev, tc.text)
			if got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if again := replaceChecklistItems(got, "c_1", tc.prev, tc.text); again != got {
				t.Errorf("a second pass changed the list:\n%s", again)
			}
		})
	}
}

// A recording whose first append merged its child under a parent the list
// already had (the child under c_0's Costco, c_1's marker bare) is
// regenerated: its items are found by their words wherever they stand and
// the shared parent is left standing, so the child is not added a second
// time, keeps its tick and its place in the block, and the parent's other
// child is untouched; the marker stays where it was. The body is the same
// body.
func TestRegenerateARecordingWhoseChildrenSitUnderAnExistingParentDoesNotDoubleThem(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "Costco", Children: []cleanup.Item{{Text: "Chicken"}}}}}})
	ctx := context.Background()
	note, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "list1", Title: "Shopping list", Kind: model.NoteKindChecklist, UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/list1/note.md",
		S3MetaKey:     "tenants/user1/notes/list1/meta.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTranscribedInto(t, h, note, "c_1", "chicken from costco", "", "Costco\n  Chicken")
	body := service.CaptureMarker("c_0") + "\n- [ ] Costco\n  - [ ] Meat\n  - [x] Chicken\n- [ ] Milk\n" + service.CaptureMarker("c_1")
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	if n := regenerate(t, h, "list1"); n != 1 {
		t.Fatalf("regenerated %d recordings, want 1", n)
	}
	got, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	if string(got) != body {
		t.Fatalf("checklist after regeneration:\n%s\nwant it unchanged:\n%s", got, body)
	}
}
