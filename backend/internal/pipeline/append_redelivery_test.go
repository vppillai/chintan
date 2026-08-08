package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// queueVisibilityTimeout is CaptureQueue.VisibilityTimeout in
// infrastructure/template.yaml:330. It is written down here because the append
// claim lease has to outlast it and Go cannot read the template: if the two
// numbers are only related by somebody remembering, they will drift.
const queueVisibilityTimeout = 960 * time.Second

// A lease shorter than the visibility timeout is a duplicate append with a
// schedule. SQS returns the message at T+VisibilityTimeout; if the claim taken
// at T0 is already stale by then, the redelivery takes it over and writes the
// same paragraph again.
func TestAppendClaimLeaseOutlastsTheQueueVisibilityTimeout(t *testing.T) {
	if repository.AppendClaimLease <= queueVisibilityTimeout {
		t.Fatalf("AppendClaimLease = %s, which is not longer than the queue visibility timeout of %s: "+
			"every redelivery arrives holding a stale claim and appends the text a second time",
			repository.AppendClaimLease, queueVisibilityTimeout)
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

// The full SQS redelivery schedule against a worker that died after writing the
// note body: deliveries at T0, T0+960s and T0+1920s, which is what
// maxReceiveCount 3 over a 960 s visibility timeout produces.
//
// The first delivery writes the paragraph and dies before CompleteCaptureAppend
// — a Lambda timeout, five lost version conflicts in refreshNoteIndex, or a
// DynamoDB fault. Every later delivery must find the append already done,
// however stale the claim has become. Correctness here must not rest on the
// lease being hand-maintained above the queue's timeout.
func TestRedeliveryOfAnInterruptedAppendWritesTheTextOnlyOnce(t *testing.T) {
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

	// Delivery 2, at T0+960s: the queue's visibility timeout has elapsed.
	rewindAppendClaim(t, f.store, queueVisibilityTimeout)
	if _, err := f.run(ctx); err != nil {
		t.Fatalf("redelivery at the visibility timeout: %v", err)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the redelivery at T+%s, want exactly 1:\n%s",
			got, queueVisibilityTimeout, f.body(t))
	}

	// Delivery 3, at T0+1920s: past any plausible lease, so the claim is taken
	// over. The takeover is legitimate — the original holder really is gone —
	// and it is exactly here that a claim-only guard re-appends.
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
		t.Fatalf("status = %s, want appended: a delivery that found the text already written "+
			"must still finish the capture rather than leave it in-flight", final.Status)
	}
}
