package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// A checklist as the worker leaves it: two recorded items, one already ticked
// by the person, a blank line between them from the append's placement.
var checklistDictated = service.CaptureMarker("c_1") + "\n- [ ] call the roofer and buy sealant\n\n" + service.CaptureMarker("c_2") + "\n- [x] passport"

func seedChecklist(t *testing.T, h *harness, mutate func(*model.NoteIndex)) {
	t.Helper()
	seedNoteWithBody(t, h, "l1", checklistDictated, func(n *model.NoteIndex) {
		n.Kind = model.NoteKindChecklist
		if mutate != nil {
			mutate(n)
		}
	})
}

// The split is the model's; the worker sends the marker-stripped item lines,
// asks in tasks mode, and stores the list it gets back.
func TestTasksModeStoresTheSplitListTheModelReturns(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "- [ ] call the roofer\n- [ ] buy sealant\n- [x] passport"}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedChecklist(t, h, nil)

	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := llmFake.NoteCalls()
	if len(calls) != 1 || calls[0].Mode != model.NoteCleanTasks {
		t.Fatalf("model calls = %+v, want one in tasks mode", calls)
	}
	if calls[0].Body != "- [ ] call the roofer and buy sealant\n\n- [x] passport" {
		t.Errorf("body sent = %q", calls[0].Body)
	}
	n := getNote(t, h, "l1")
	if n.CleanedBody != "- [ ] call the roofer\n- [ ] buy sealant\n- [x] passport" || n.CleanedMode != model.NoteCleanTasks {
		t.Errorf("stored view = %q in %q", n.CleanedBody, n.CleanedMode)
	}
	if n.CleanedStale || n.CleanedError != "" {
		t.Errorf("stale=%v error=%q after a successful run", n.CleanedStale, n.CleanedError)
	}
}

// Done items come back verbatim and the blank line the append placed is not
// in the view: the stored text is item lines and nothing else.
func TestTasksModeKeepsDoneItemsVerbatimAndDropsBlankLines(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedChecklist(t, h, nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := getNote(t, h, "l1").CleanedBody; got != "- [ ] call the roofer and buy sealant\n- [x] passport" {
		t.Errorf("cleaned body = %q", got)
	}
}

// A model that answers in prose has not produced a checklist view. The run
// records the fixed verdict and keeps the previous list.
func TestTasksModeRefusesProseAndKeepsThePreviousList(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "Call the roofer, then buy sealant. The passport is packed."}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedChecklist(t, h, func(n *model.NoteIndex) {
		n.CleanedBody, n.CleanedMode, n.CleanedAt = "- [ ] the old list", model.NoteCleanTasks, "2026-01-01T00:00:00.000000000Z"
	})
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n := getNote(t, h, "l1")
	if n.CleanedError != cleanNoteUnusable {
		t.Errorf("cleaned error = %q, want %q", n.CleanedError, cleanNoteUnusable)
	}
	if n.CleanedBody != "- [ ] the old list" {
		t.Errorf("the previous list was not kept: %q", n.CleanedBody)
	}
}

func TestTasksModeRefusesMoreThanFiveHundredItems(t *testing.T) {
	// Every open item in the body's words and the body's done item among
	// them, so the count is the only thing the check can refuse.
	items := make([]string, 0, 501)
	for i := 0; i < 500; i++ {
		items = append(items, "- [ ] roofer")
	}
	items = append(items, "- [x] passport")
	llmFake := &fake.LLM{NoteResponse: strings.Join(items, "\n")}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedChecklist(t, h, nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := getNote(t, h, "l1"); n.CleanedError != cleanNoteUnusable || n.CleanedBody != "" {
		t.Errorf("501 items: error=%q body=%q, want the unusable verdict and no view", n.CleanedError, n.CleanedBody)
	}

	llmFake.NoteResponse = strings.Join(items[1:], "\n")
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := getNote(t, h, "l1"); n.CleanedError != "" || strings.Count(n.CleanedBody, "\n") != 499 {
		t.Errorf("500 items: error=%q lines=%d, want the list stored", n.CleanedError, strings.Count(n.CleanedBody, "\n")+1)
	}
}

// A task whose words are not in the note is the model's, not the person's:
// it is dropped and the split the model got right is stored (owner feedback
// 2026-09-26, "- [x] Make a list." beside two correct splits).
func TestTasksModeDropsATaskWhoseWordsAreNotInTheNote(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "- [ ] call the roofer\n- [ ] buy sealant\n- [ ] make a list\n- [x] passport"}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedChecklist(t, h, nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n := getNote(t, h, "l1")
	if n.CleanedBody != "- [ ] call the roofer\n- [ ] buy sealant\n- [x] passport" || n.CleanedError != "" {
		t.Errorf("stored view = %q (%q), want the invented task dropped and the rest kept", n.CleanedBody, n.CleanedError)
	}
}

// An answer that loses, invents or rewords a tick is refused whole and the
// previous view is kept: adoption writes the view over the body, and a lost
// tick there is worse than an older view.
func TestTasksModeRefusesAnAnswerThatChangesTheDoneItems(t *testing.T) {
	for name, reply := range map[string]string{
		"a tick lost":          "- [ ] call the roofer\n- [ ] buy sealant\n- [ ] passport",
		"a tick invented":      "- [x] call the roofer\n- [ ] buy sealant\n- [x] passport",
		"a done item reworded": "- [ ] call the roofer\n- [ ] buy sealant\n- [x] passport renewed",
	} {
		llmFake := &fake.LLM{NoteResponse: reply}
		h := newHarness(t, harnessOpts{llm: llmFake})
		seedChecklist(t, h, func(n *model.NoteIndex) {
			n.CleanedBody, n.CleanedMode, n.CleanedAt = "- [ ] the old list", model.NoteCleanTasks, "2026-01-01T00:00:00.000000000Z"
		})
		if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanTasks)); err != nil {
			t.Fatalf("%s: Handle: %v", name, err)
		}
		if n := getNote(t, h, "l1"); n.CleanedError != cleanNoteUnusable || n.CleanedBody != "- [ ] the old list" {
			t.Errorf("%s: error=%q body=%q, want the unusable verdict and the previous list", name, n.CleanedError, n.CleanedBody)
		}
	}
}

// auto_clean on a checklist: the append writes the item, and the clean that
// follows runs in tasks whatever preference the row holds — inline here, as
// a worker without an invoker runs it.
func TestAppendToAnAutoCleanChecklistCleansInTasksMode(t *testing.T) {
	ctx := context.Background()
	objects := memory.NewObjects()
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{objects: objects, llm: llmFake})
	seedNoteWithBody(t, h, "l1", "", func(n *model.NoteIndex) {
		n.Kind = model.NoteKindChecklist
		n.AutoClean = true
		n.CleanMode = model.NoteCleanPolished // a preference from before it was a checklist
	})
	const cleanKey = "tenants/user1/captures/c_1/clean.txt"
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "l1", Status: model.StatusCleaned, Mode: model.CleanupFaithful,
		CleanKey: cleanKey, CreatedAt: model.Now(),
		AudioKey: "tenants/user1/captures/c_1/audio.webm", RawKey: "tenants/user1/captures/c_1/raw.txt",
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if err := objects.Put(ctx, cleanKey, []byte("buy sealant"), "text/plain"); err != nil {
		t.Fatalf("seed clean text: %v", err)
	}

	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := llmFake.NoteCalls()
	if len(calls) != 1 || calls[0].Mode != model.NoteCleanTasks {
		t.Fatalf("clean-note calls = %+v, want one in tasks mode", calls)
	}
	n := getNote(t, h, "l1")
	if n.CleanedMode != model.NoteCleanTasks || n.CleanedBody != "- [ ] buy sealant" || n.CleanedStale {
		t.Errorf("view after the append = %q in %q (stale=%v)", n.CleanedBody, n.CleanedMode, n.CleanedStale)
	}
}
