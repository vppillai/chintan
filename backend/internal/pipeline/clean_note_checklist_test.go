package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// A checklist has no cleaned view since 2026-09-27 (round-5 prompts lens,
// PR-D4): the request path refuses a clean, and the worker, which may still
// receive a task queued before the deploy or by a client that has not
// reloaded, reaches its verdict by doing nothing — no model call, no row
// write.
func TestCleanNoteOnAChecklistMakesNoCallAndWritesNothing(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedNoteWithBody(t, h, "l1", service.CaptureMarker("c_1")+"\n- [ ] call the roofer and buy sealant", func(n *model.NoteIndex) {
		n.Kind = model.NoteKindChecklist
		// A view the deleted tasks mode stored: left as it is, never shown.
		n.CleanedBody, n.CleanedMode, n.CleanedAt = "- [ ] the old split", "tasks", "2026-01-01T00:00:00.000000000Z"
	})

	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "l1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls := llmFake.NoteCalls(); len(calls) != 0 {
		t.Fatalf("model calls = %+v, want none for a checklist", calls)
	}
	if n := getNote(t, h, "l1"); n.CleanedBody != "- [ ] the old split" || n.CleanedError != "" {
		t.Errorf("row after the task = body %q error %q, want it untouched", n.CleanedBody, n.CleanedError)
	}
}

// auto_clean on a checklist — a preference kept from before it was one — asks
// for nothing after an append: the item is written and no clean runs.
func TestAppendToAnAutoCleanChecklistRunsNoClean(t *testing.T) {
	ctx := context.Background()
	objects := memory.NewObjects()
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{objects: objects, llm: llmFake})
	seedNoteWithBody(t, h, "l1", "", func(n *model.NoteIndex) {
		n.Kind = model.NoteKindChecklist
		n.AutoClean = true
	})
	const cleanKey = "tenants/user1/captures/c_1/clean.txt"
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "l1", Status: model.StatusCleaned,
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
	if calls := llmFake.NoteCalls(); len(calls) != 0 {
		t.Fatalf("clean-note calls = %+v, want none for a checklist", calls)
	}
	n := getNote(t, h, "l1")
	if n.CleanedBody != "" || n.CleanedRequestedAt != "" {
		t.Errorf("a clean was recorded for a checklist: body %q requested_at %q", n.CleanedBody, n.CleanedRequestedAt)
	}
}
