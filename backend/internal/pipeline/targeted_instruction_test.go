package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// runTargeted records a transcript straight into note1 and returns the note
// body the pipeline left.
func runTargeted(t *testing.T, h *harness) (model.CaptureIndex, string) {
	t.Helper()
	ctx := context.Background()
	seedUploadedCapture(t, h, "note1")
	if err := NewWorker(h.pipeline).Handle(ctx, s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	capture, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	body, err := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
	if err != nil {
		t.Fatalf("read note body: %v", err)
	}
	return capture, string(body)
}

// A recording made into a note skips routing, so until 2026-09 the spoken
// instruction went into the body verbatim while the same words in a routed
// recording were removed (QA 2026-09-05 §5a). The targeted path now makes the
// same span call — the target note as its only candidate — and ignores the
// destination it answers.
func TestARecordingIntoANoteHasItsSpokenInstructionRemoved(t *testing.T) {
	h := newHarness(t, harnessOpts{stt: &fake.STT{Response: "Create a note with the title Staging Smoke. The gutter on the north side is leaking again."}})
	// The router's answer as the real adapter derives it: the eight words of
	// instruction deleted, the destination it picks irrelevant here.
	h.router.Spans = []routing.Span{{StartWord: 0, EndWord: 8}}

	capture, body := runTargeted(t, h)
	if capture.Status != model.StatusAppended || capture.NoteID != "note1" {
		t.Fatalf("capture = %s in %q, want appended into note1", capture.Status, capture.NoteID)
	}
	if strings.Contains(body, "Create a note") || strings.Contains(body, "Staging Smoke") {
		t.Errorf("the spoken instruction reached the note body:\n%s", body)
	}
	if !strings.Contains(body, "gutter on the north side is leaking again") {
		t.Errorf("the dictation is missing from the note body:\n%s", body)
	}
	if got := h.router.CallCount(); got != 1 {
		t.Errorf("router calls = %d, want exactly one span call", got)
	}
	if c := h.router.LastCandidates; len(c) != 1 || c[0].NoteID != "note1" {
		t.Errorf("router candidates = %+v, want the target note alone", c)
	}
	if capture.RoutedKey == "" {
		t.Error("the stripped text was not stored as the routed text; a retry would call the router again")
	}
}

// No instruction cue, no call: plain dictation into a note costs nothing
// extra and is appended as spoken.
func TestARecordingIntoANoteWithoutAnInstructionCueSkipsTheRouter(t *testing.T) {
	h := newHarness(t, harnessOpts{stt: &fake.STT{Response: "the gutter on the north side is leaking again"}})
	capture, body := runTargeted(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", capture.Status)
	}
	if got := h.router.CallCount(); got != 0 {
		t.Errorf("router calls = %d, want none for a transcript with no instruction cue", got)
	}
	if !strings.Contains(body, "gutter on the north side") {
		t.Errorf("body = %q", body)
	}
}

// The span call is a convenience: when the router fails, the recording is
// appended as spoken, instruction and all, rather than lost or retried.
func TestARouterFailureOnARecordingIntoANoteKeepsTheWordsAsSpoken(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt:    &fake.STT{Response: "Add this to my roof note. The flashing needs checking."},
		router: &fake.Router{ShouldFail: true},
	})
	capture, body := runTargeted(t, h)
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended despite the router failing", capture.Status)
	}
	// The fake cleanup lowercases what it is given; the words are what matter.
	if got := strings.ToLower(body); !strings.Contains(got, "add this to my roof note") || !strings.Contains(got, "flashing needs checking") {
		t.Errorf("a failed span call changed the words: %q", body)
	}
}
