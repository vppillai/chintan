package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// parkedInvoker accepts the hand-off and runs nothing, so a test can set the
// row to the state a dead worker leaves before running the pipeline itself.
type parkedInvoker struct{ directInvoker }

func (parkedInvoker) InvokeCapture(context.Context, string, string, string) error { return nil }

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
		S3MetaKey:     "tenants/user1/notes/note1/meta.json",
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

// A worker that took the append claim for the second transcription and died
// before writing. The earlier paragraph is still in the note under the
// capture's marker, so the marker proves nothing about this attempt: a retry
// inside the lease that took it as proof marked the capture appended with the
// wrong-script paragraph untouched, and no error anywhere (review 2026-09-21,
// T7 follow-up). It must fail the way any retry of an unwritten append does,
// and the attempt after the lease must replace the paragraph.
func TestRetryOfARetranscriptionThatDiedBeforeWritingDoesNotTakeTheOldParagraphAsDone(t *testing.T) {
	h, note := retranscribeNote(t, "")
	ctx := context.Background()
	seedAppendedInto(t, h, note, "c_1", "First take, wrong script.")

	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(parkedInvoker{})
	if _, err := svc.RetranscribeCapture(ctx, "user1", "c_1", "ta"); err != nil {
		t.Fatalf("RetranscribeCapture: %v", err)
	}
	cleanKey, err := keys.CaptureClean("user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	// The state the dead worker left: this attempt's claim, taken a moment
	// ago, and nothing written under it.
	claimedAgo := func(d time.Duration) {
		c, err := h.store.GetCapture(ctx, "user1", "c_1")
		if err != nil {
			t.Fatal(err)
		}
		c.AppendToken, c.AppendClaimedAt = appendToken("c_1", cleanKey), time.Now().Add(-d).Unix()
		if _, err := h.store.PutCapture(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	claimedAgo(0)

	before, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	mid, err := h.pipeline.Run(ctx, "user1", "c_1")
	if !errors.Is(err, errAppendClaimHeld) {
		t.Fatalf("retry inside the lease: err = %v (status %s), want errAppendClaimHeld: the earlier paragraph was taken as this attempt's", err, mid.Status)
	}
	if body, _ := h.objects.Get(ctx, note.S3MarkdownKey); string(body) != string(before) {
		t.Fatalf("the retry wrote inside the lease:\n%s", body)
	}

	// Past the lease the claim is taken over and the paragraph replaced, once.
	claimedAgo(repository.AppendClaimLease + time.Minute)
	final, err := h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("retry after the lease: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s (%s), want appended", final.Status, final.Error)
	}
	body, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	if want := service.CaptureMarker("c_1") + "\nThe words as they were said."; string(body) != want {
		t.Fatalf("note body after the takeover:\n%s\nwant:\n%s", body, want)
	}
	if n := len(h.stt.Sources); n != 1 {
		t.Errorf("stt calls = %d, want one: the takeover resumes from the cleaned text", n)
	}
}

// The editor PATCHes the whole draft when the note's language changes, body
// included. A body that reads the same is not an edit, so the recording's
// marker stays where it is and "transcribe again" replaces the paragraph in
// place instead of appending a second copy (QA 2026-09-21, finding 1).
func TestRetranscribingAfterAnUnchangedBodySaveReplacesInPlace(t *testing.T) {
	h, note := retranscribeNote(t, "")
	ctx := context.Background()
	seedAppendedInto(t, h, note, "c_1", "First take.")
	seedAppendedInto(t, h, note, "c_2", "Second take, wrong script.")
	seedAppendedInto(t, h, note, "c_3", "Third take.")

	sameBody, ml := "First take.\n\nSecond take, wrong script.\n\nThird take.", "ml"
	notes := service.NewNotesService(h.store, h.objects)
	if _, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: &sameBody, Language: &ml}); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(directInvoker{h.pipeline})
	if _, err := svc.RetranscribeCapture(ctx, "user1", "c_2", ""); err != nil {
		t.Fatalf("RetranscribeCapture: %v", err)
	}
	body, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	want := service.CaptureMarker("c_1") + "\nFirst take.\n\n" +
		service.CaptureMarker("c_2") + "\nThe words as they were said.\n\n" +
		service.CaptureMarker("c_3") + "\nThird take."
	if string(body) != want {
		t.Fatalf("note body after a same-body save and retranscription:\n%s\nwant:\n%s", body, want)
	}
}
