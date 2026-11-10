package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

type failingInvoker struct{ Invoker }

// recordingPuts remembers every key written, for a test that must find what
// a failed request left behind without knowing its capture id.
type recordingPuts struct {
	*memory.Objects
	keys []string
}

func (o *recordingPuts) Put(ctx context.Context, key string, body []byte, ct string) error {
	o.keys = append(o.keys, key)
	return o.Objects.Put(ctx, key, body, ct)
}

func (failingInvoker) InvokeCapture(context.Context, string, string, string) error {
	return errors.New("lambda said no")
}

// A typed capture whose hand-off failed answered an error and left a
// `transcribed` row and its text behind, which the reaper filed a quarter of
// an hour on beside the client's retry.
func TestIngestTextLeavesNothingBehindWhenTheHandOffFails(t *testing.T) {
	h := newEditHarness(t)
	objects := &recordingPuts{Objects: h.objects}
	captures := NewCaptureService(h.store, objects).WithNoteCreator(h.notes).WithInvoker(failingInvoker{})
	if _, err := captures.IngestText(h.ctx, "u1", CaptureRequest{}, "remember the milk"); err == nil {
		t.Fatal("want an error")
	}
	page, err := h.store.ListCaptures(h.ctx, "u1", repository.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("the request failed, yet %d capture row(s) remain at %s for the reaper to file", len(page.Items), page.Items[0].Status)
	}
	if len(objects.keys) != 1 {
		t.Fatalf("objects written = %v, want the text alone", objects.keys)
	}
	if h.objectExists(objects.keys[0]) {
		t.Errorf("the text of the failed capture is still stored at %s", objects.keys[0])
	}
}
