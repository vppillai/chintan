package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/service"
)

// seedAppendedInto writes captureID's paragraph into note1's body under its
// marker and the matching appended row, with the audio still in the bucket.
func seedAppendedInto(t *testing.T, h *harness, note model.NoteIndex, captureID, text string) {
	t.Helper()
	ctx := context.Background()
	existing, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	body := service.CaptureMarker(captureID) + "\n" + text
	if len(existing) > 0 {
		body = string(existing) + "\n\n" + body
	}
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	audioKey := "tenants/user1/captures/" + captureID + "/audio.webm"
	if err := h.objects.Put(ctx, audioKey, []byte("opus"), "audio/webm"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: captureID, UserID: "user1", NoteID: note.ID, Status: model.StatusAppended,
		Mode: model.CleanupFaithful, AudioKey: audioKey, CreatedAt: model.Now(), AppendedAt: 1,
		AppendToken: "earlier", Language: "en",
	}); err != nil {
		t.Fatal(err)
	}
}

func retranscribeNote(t *testing.T, kind string) (*harness, model.NoteIndex) {
	t.Helper()
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Response: "the words as they were said"},
		llm: &fake.LLM{Response: "The words as they were said."},
	})
	note, err := h.store.PutNote(context.Background(), "user1", model.NoteIndex{
		ID: "note1", Title: "Destination", Kind: kind, Language: "ml", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, note
}

// Transcribing a recording again replaces the paragraph it dictated where it
// stands, by its marker: the recordings before and after it keep their place,
// and there is one marker for it afterwards, not two paragraphs (review
// 2026-09-21, T7). The request path asks for the language; the worker sends
// it and records it.
func TestRetranscribingAnAppendedRecordingReplacesItsParagraphInPlace(t *testing.T) {
	h, note := retranscribeNote(t, "")
	ctx := context.Background()
	seedAppendedInto(t, h, note, "c_1", "First take, wrong script.")
	seedAppendedInto(t, h, note, "c_2", "Second take.")
	seedAppendedInto(t, h, note, "c_3", "Third take.")

	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(directInvoker{h.pipeline})
	if _, err := svc.RetranscribeCapture(ctx, "user1", "c_2", "ta"); err != nil {
		t.Fatalf("RetranscribeCapture: %v", err)
	}

	capture, err := h.store.GetCapture(ctx, "user1", "c_2")
	if err != nil {
		t.Fatal(err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (%s), want appended", capture.Status, capture.Error)
	}
	body, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	want := service.CaptureMarker("c_1") + "\nFirst take, wrong script.\n\n" +
		service.CaptureMarker("c_2") + "\nThe words as they were said.\n\n" +
		service.CaptureMarker("c_3") + "\nThird take."
	if string(body) != want {
		t.Fatalf("note body after retranscription:\n%s\nwant:\n%s", body, want)
	}
	if n := len(h.stt.Sources); n != 1 || h.stt.Sources[0].Language != "ta" {
		t.Errorf("stt calls = %d (%+v), want one in ta: the person's choice outranks the note's ml", n, h.stt.Sources)
	}
	if capture.Language != "ta" || capture.RequestedLanguage != "ta" {
		t.Errorf("languages = sent %q requested %q, want ta and ta", capture.Language, capture.RequestedLanguage)
	}
}

// A checklist item is replaced as one line, and a tick the person made stays:
// the words were transcribed again, not the decision.
func TestRetranscribingAChecklistItemKeepsItOneLineAndTicked(t *testing.T) {
	h, note := retranscribeNote(t, model.NoteKindChecklist)
	ctx := context.Background()
	seedAppendedInto(t, h, note, "c_1", "- [x] first item, wrong script")
	seedAppendedInto(t, h, note, "c_2", "- [ ] second item")

	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(directInvoker{h.pipeline})
	if _, err := svc.RetranscribeCapture(ctx, "user1", "c_1", ""); err != nil {
		t.Fatalf("RetranscribeCapture: %v", err)
	}
	body, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	want := service.CaptureMarker("c_1") + "\n- [x] The words as they were said.\n\n" +
		service.CaptureMarker("c_2") + "\n- [ ] second item"
	if string(body) != want {
		t.Fatalf("checklist body after retranscription:\n%s\nwant:\n%s", body, want)
	}
	if n := len(h.stt.Sources); n != 1 || h.stt.Sources[0].Language != "ml" {
		t.Errorf("stt calls = %d (%+v), want one in the note's ml when no language is asked for", n, h.stt.Sources)
	}
	if strings.Count(string(body), service.CaptureMarker("c_1")) != 1 {
		t.Errorf("marker for c_1 appears more than once:\n%s", body)
	}
}
