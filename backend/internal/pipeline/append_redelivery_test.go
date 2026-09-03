package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// queueVisibilityTimeout is CaptureQueue.VisibilityTimeout in
// infrastructure/template.yaml. It is written down here because the redelivery
// schedule the tests below replay is derived from it, and Go cannot read the
// template.
const queueVisibilityTimeout = 960 * time.Second

// The lease must outlast a live worker, or a claim could be taken over while
// its holder is still writing — the one case the note-body check cannot cover.
// The worker Lambda's Timeout is 900 s.
const workerLambdaTimeout = 900 * time.Second

func TestAppendClaimLeaseOutlastsALiveWorker(t *testing.T) {
	if repository.AppendClaimLease <= workerLambdaTimeout {
		t.Fatalf("AppendClaimLease = %s, which is not longer than the worker Lambda timeout of %s: "+
			"a claim could be taken over from a worker that is still writing",
			repository.AppendClaimLease, workerLambdaTimeout)
	}
}

// The redelivery schedule has to actually reach the lease. Three receives over
// a 960 s visibility timeout put the last one at 1920 s; if the lease were ever
// raised above that, an attempt that died between claiming and writing could
// never be taken over before the message dead-letters.
func TestRedeliveryScheduleReachesTheClaimLease(t *testing.T) {
	const maxReceiveCount = 3 // CaptureQueue.RedrivePolicy.maxReceiveCount
	lastDelivery := time.Duration(maxReceiveCount-1) * queueVisibilityTimeout
	if lastDelivery <= repository.AppendClaimLease {
		t.Fatalf("the last SQS delivery arrives at T+%s, inside the %s claim lease: "+
			"an interrupted append could never be taken over before the message dead-letters",
			lastDelivery, repository.AppendClaimLease)
	}
}

// rewindAppendClaim ages the capture's claim by d, which is what the passage of
// real time does between one SQS delivery and the next.
func rewindAppendClaim(t *testing.T, store *memory.Store, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	c, err := store.GetCapture(ctx, "user1", "capture1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if c.AppendClaimedAt == 0 {
		t.Fatalf("capture holds no append claim to age; the test no longer reproduces the redelivery window")
	}
	c.AppendClaimedAt = time.Now().Add(-d).Unix()
	if _, err := store.PutCapture(ctx, c); err != nil {
		t.Fatalf("age the append claim: %v", err)
	}
}

// A worker that died AFTER writing the note body — a Lambda timeout, five lost
// version conflicts in refreshNoteIndex, a DynamoDB fault — and the one SQS
// redelivery that follows at T0+960s.
//
// That redelivery arrives inside the 20-minute claim lease. It used to concede,
// which acked the message; with the message gone there was no third delivery
// and the capture sat in `appending` forever with its text already in the note.
// This is the live defect this test pins: the redelivery must finish the
// capture, and must not write the text again.
func TestRedeliveryOfAnInterruptedAppendFinishesItWithoutRewriting(t *testing.T) {
	var interrupt *failOnceOnPutNote
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		interrupt = &failOnceOnPutNote{Store: s}
		return interrupt
	})
	ctx := context.Background()

	// Delivery 1: the body is written, the completion is not.
	if _, err := f.run(ctx); err == nil {
		t.Fatal("expected the induced index-write failure to surface")
	}
	if !interrupt.didFail() {
		t.Fatal("the test did not actually interrupt the index write")
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("after the interrupted attempt the text appears %d times, want 1", got)
	}

	// Delivery 2, at T0+960s: the queue's visibility timeout has elapsed but
	// the claim lease has not. The text is in the body, so this delivery must
	// finish the capture rather than concede.
	rewindAppendClaim(t, f.store, queueVisibilityTimeout)
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("redelivery at the visibility timeout: %v", err)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the redelivery at T+%s, want exactly 1:\n%s",
			got, queueVisibilityTimeout, f.body(t))
	}
	if final.Status != model.StatusAppended || final.AppendedAt == 0 {
		t.Fatalf("status = %s, appended_at = %d after the redelivery; want appended and set. "+
			"Conceding here leaves the capture in-flight with no delivery left to finish it",
			final.Status, final.AppendedAt)
	}
}

// A worker that died BETWEEN taking the claim and writing the body. Nothing is
// in the note, so a redelivery inside the lease cannot tell this from a holder
// that is about to write; it must not append, and it must not concede either —
// it asks to be redelivered. The delivery that arrives after the lease takes the
// claim over and does the append once.
func TestRedeliveryOfAnAppendThatDiedBeforeWritingWaitsForTheLease(t *testing.T) {
	f := newAppendFixture(t, memory.NewObjects(), nil)
	ctx := context.Background()

	// The state the dead worker left: claim held under this capture's token,
	// status appending, nothing written.
	c, err := f.store.GetCapture(ctx, "user1", "capture1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	c.Status = model.StatusAppending
	c.AppendToken = appendToken("capture1", appendCleanKey)
	c.AppendClaimedAt = time.Now().Unix()
	if _, err := f.store.PutCapture(ctx, c); err != nil {
		t.Fatalf("seed the abandoned claim: %v", err)
	}

	// Delivery at T0+960s: inside the lease, body empty.
	rewindAppendClaim(t, f.store, queueVisibilityTimeout)
	mid, err := f.run(ctx)
	if !errors.Is(err, errAppendClaimHeld) {
		t.Fatalf("redelivery inside the lease returned err = %v, want errAppendClaimHeld: "+
			"conceding acks the message and strands the capture; appending races the holder", err)
	}
	if body := f.body(t); body != "" {
		t.Fatalf("the in-lease redelivery wrote to the note body: %q", body)
	}
	if mid.Status != model.StatusAppending {
		t.Fatalf("status = %s after the in-lease redelivery, want appending", mid.Status)
	}

	// Delivery at T0+1920s: past the lease, so the claim is taken over and the
	// append happens — once.
	rewindAppendClaim(t, f.store, 2*queueVisibilityTimeout)
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("redelivery after the lease expired: %v", err)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the claim was taken over, want exactly 1:\n%s",
			got, f.body(t))
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", final.Status)
	}
}
