package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The person deleted a checklist recording's mis-scripted lines by hand and
// asked for it to be transcribed again. None of the old words are in the list
// any more, which for a regeneration means "the person removed these, do not
// put them back"; for the person's own Transcribe again it is the opposite,
// and the new items must land under the marker rather than nowhere while the
// capture reads appended.
func TestRetranscribingAChecklistRecordingWhoseLinesWereDeletedLandsTheNewItems(t *testing.T) {
	h, note := retranscribeNote(t, model.NoteKindChecklist)
	ctx := context.Background()
	seedAppendedInto(t, h, note, "c_1", "- [ ] first, wrong script\n- [ ] second, wrong script")
	cleanKey, err := keys.CaptureClean("user1", "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.objects.Put(ctx, cleanKey, []byte("first, wrong script\nsecond, wrong script"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	row, _ := h.store.GetCapture(ctx, "user1", "c_1")
	row.CleanKey = cleanKey
	if _, err := h.store.PutCapture(ctx, row); err != nil {
		t.Fatal(err)
	}
	// The person's save: the wrong-script lines gone, a typed line kept.
	if err := h.objects.Put(ctx, note.S3MarkdownKey, []byte("- [ ] typed\n"+service.CaptureMarker("c_1")), "text/markdown"); err != nil {
		t.Fatal(err)
	}

	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(directInvoker{h.pipeline})
	if _, err := svc.RetranscribeCapture(ctx, "user1", "c_1", ""); err != nil {
		t.Fatalf("RetranscribeCapture: %v", err)
	}
	capture, _ := h.store.GetCapture(ctx, "user1", "c_1")
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (%s), want appended", capture.Status, capture.Error)
	}
	body, _ := h.objects.Get(ctx, note.S3MarkdownKey)
	for _, item := range []string{"The words", "as they", "were said"} {
		if !strings.Contains(string(body), "- [ ] "+item) {
			t.Fatalf("the new item %q is not in the list after Transcribe again:\n%s", item, body)
		}
	}
	if !strings.Contains(string(body), "- [ ] typed") {
		t.Fatalf("the typed line is gone:\n%s", body)
	}
}
