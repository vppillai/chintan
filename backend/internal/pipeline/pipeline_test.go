package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// An upload that lands after its capture row was deleted — DELETE lets a stuck
// upload go after fifteen minutes, the presigned PUT is good for thirty — must
// not fail the invocation three times into the dead-letter queue and leave the
// audio in the bucket for good. The object is removed and the event is done.
func TestWorkerRemovesAnObjectWhoseCaptureWasDeleted(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	key := "tenants/user1/captures/c_1/audio.webm"
	if err := h.objects.Put(ctx, key, []byte("audio bytes"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}

	if err := NewWorker(h.pipeline).Handle(ctx, s3EventOfSize(key, 11)); err != nil {
		t.Fatalf("Handle: %v; a row deleted by hand is not an infrastructure fault, "+
			"so retrying it only fills the dead-letter queue", err)
	}
	if _, err := h.objects.Get(ctx, key); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the orphaned object is still in the bucket (get err = %v)", err)
	}
	if _, err := h.store.GetCapture(ctx, "user1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a row came back for the deleted capture: %v", err)
	}
	if got := h.stt.Calls(); got != 0 {
		t.Fatalf("the speech provider was called %d time(s) for a capture that no longer exists", got)
	}

	// A direct invocation for a missing row carries no object and is still the
	// retryable fault it was: there is nothing to clean up, and the row may be
	// on its way.
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Run for a missing row = %v, want the not-found fault", err)
	}
}
