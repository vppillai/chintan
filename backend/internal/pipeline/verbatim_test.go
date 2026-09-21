package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// README, the OpenAPI document and the About screen all say a note marked
// verbatim bypasses cleanup; until 2026-09-21 run() never read note.Verbatim
// and the verifier counted one paid cleanup call for such a note (review
// T11). The transcript is appended as spoken and no model is called; the
// spoken instruction is still stripped, because that is routing's job, not
// cleanup's.
func TestAVerbatimNoteIsAppendedAsSpokenWithoutACleanupCall(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "um so the pod was crash looping, exit code 137"},
		llm: &fake.LLM{Response: "The pod was crash-looping with exit code 137."},
	})
	ctx := context.Background()
	seedUploadedCapture(t, h, "note1")
	note := mustGetNote(t, h.store, "user1", "note1")
	note.Verbatim = true
	if _, err := h.store.PutNote(ctx, "user1", note); err != nil {
		t.Fatal(err)
	}

	if err := NewWorker(h.pipeline).Handle(ctx, s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	capture, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (%s), want appended", capture.Status, capture.Error)
	}
	if n := h.llm.Calls(); n != 0 {
		t.Fatalf("cleanup model was called %d time(s) for a verbatim note; want 0", n)
	}
	if capture.CleanKey == "" || capture.CleanKey != capture.RawKey {
		t.Errorf("clean_key = %q, want the raw transcript's key %q so every reader of the cleaned text finds the words as spoken", capture.CleanKey, capture.RawKey)
	}
	body, _ := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
	if !strings.Contains(string(body), "um so the pod was crash looping, exit code 137") {
		t.Errorf("the note body does not carry the transcript as spoken:\n%s", body)
	}
	if strings.Contains(string(body), "crash-looping") {
		t.Errorf("the cleaned text reached a verbatim note:\n%s", body)
	}
}
