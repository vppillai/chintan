package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// The API reads a capture's row before it invokes the worker for it, so an
// invocation that names a row which is not there is a capture deleted since —
// the recording itself, or its note for good. It used to fail three times
// into the dead-letter queue and page for a deletion the person asked for.
func TestAnInvocationForADeletedCaptureEndsQuietly(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	raw, _ := json.Marshal(Invocation{TenantID: "user1", CaptureID: "c_gone", Reason: "retry"})
	if err := NewWorker(h.pipeline).Handle(context.Background(), raw); err != nil {
		t.Fatalf("Handle = %v, want nil: there is nothing to retry", err)
	}
}

// The same deletion landing while the worker is on the capture: its next
// conditional write loses, and the reload finds no row. That is the end of
// the delivery, not a fault.
func TestACaptureDeletedUnderTheWorkerStopsTheDeliveryWithoutAnError(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWrapping(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		return &foreignWriteBeforeFirstPut{
			Store: s,
			before: func(ctx context.Context, store repository.Store, current model.CaptureIndex) {
				if err := store.DeleteCapture(ctx, current.UserID, current.ID); err != nil {
					t.Errorf("delete under the worker: %v", err)
				}
			},
		}
	}, harnessOpts{})
	seedUploadedCapture(t, h, "note1")

	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if _, err := h.store.GetCapture(ctx, "user1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("capture row = %v, want it to stay deleted", err)
	}
	if body, _ := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md"); len(body) != 0 {
		t.Errorf("note body = %q, want nothing written for a deleted capture", body)
	}
}
