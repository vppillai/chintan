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
	"github.com/vppillai/chintan/backend/internal/service"
)

// Lambda retries a failed asynchronous invocation twice, about a minute after
// the failure and then about two minutes after that (WorkerLambdaEventInvokeConfig
// in infrastructure/template.yaml sets MaximumRetryAttempts: 2). The tests below
// replay that schedule, so the offsets are written down here; Go cannot read the
// template, and the schedule itself is Lambda's, not ours to configure.
var asyncRetryOffsets = []time.Duration{1 * time.Minute, 3 * time.Minute}

// The lease must outlast a live worker, or a claim could be taken over while
// its holder is still writing — the one case the marker check cannot cover.
// The worker Lambda's Timeout is 900 s.
const workerLambdaTimeout = 900 * time.Second

func TestAppendClaimLeaseOutlastsALiveWorker(t *testing.T) {
	if repository.AppendClaimLease <= workerLambdaTimeout {
		t.Fatalf("AppendClaimLease = %s, which is not longer than the worker Lambda timeout of %s: "+
			"a claim could be taken over from a worker that is still writing",
			repository.AppendClaimLease, workerLambdaTimeout)
	}
}

// rewindAppendClaim ages the capture's claim by d, which is what the passage of
// real time does between one Lambda attempt and the next.
func rewindAppendClaim(t *testing.T, store *memory.Store, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	c, err := store.GetCapture(ctx, "user1", "capture1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if c.AppendClaimedAt == 0 {
		t.Fatalf("capture holds no append claim to age; the test no longer reproduces the retry window")
	}
	c.AppendClaimedAt = time.Now().Add(-d).Unix()
	if _, err := store.PutCapture(ctx, c); err != nil {
		t.Fatalf("age the append claim: %v", err)
	}
}

// A worker that died AFTER writing the note body — a Lambda timeout, five lost
// version conflicts in refreshNoteIndex, a DynamoDB fault — and the first
// automatic retry that follows about a minute later.
//
// That retry arrives inside the 20-minute claim lease. Under SQS it used to
// concede, which acknowledged the message; with nothing left to redeliver, the
// capture sat in `appending` forever with its text already in the note. The
// retry must finish the capture, and must not write the text again — and what
// tells it the text is there is the capture's own marker, not a search for the
// sentence.
func TestRetryOfAnInterruptedAppendFinishesItWithoutRewriting(t *testing.T) {
	var interrupt *failOnceOnPutNote
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		interrupt = &failOnceOnPutNote{Store: s}
		return interrupt
	})
	ctx := context.Background()

	// Attempt 1: the body is written, the completion is not.
	if _, err := f.run(ctx); err == nil {
		t.Fatal("expected the induced index-write failure to surface")
	}
	if !interrupt.didFail() {
		t.Fatal("the test did not actually interrupt the index write")
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("after the interrupted attempt the text appears %d times, want 1", got)
	}
	if !service.HasCaptureMarker(f.body(t), "capture1") {
		t.Fatalf("the append wrote the text without its marker; a retry has nothing exact to look for:\n%s", f.body(t))
	}

	// Retry 1, about a minute later: the claim lease has not elapsed. The
	// marker is in the body, so this attempt must finish the capture rather
	// than concede or fail.
	rewindAppendClaim(t, f.store, asyncRetryOffsets[0])
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("first automatic retry: %v", err)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the retry at T+%s, want exactly 1:\n%s",
			got, asyncRetryOffsets[0], f.body(t))
	}
	if final.Status != model.StatusAppended || final.AppendedAt == 0 {
		t.Fatalf("status = %s, appended_at = %d after the retry; want appended and set. "+
			"Conceding here leaves the capture in-flight with no attempt left to finish it",
			final.Status, final.AppendedAt)
	}
}

// The marker, not the text, is the evidence. A user who edits the paragraph
// the worker just appended — inside the minute before the retry — used to
// destroy the only proof that the append happened, and the retry appended it
// again. Now the edit goes through UpdateNote, which carries the marker.
func TestRetryOfAnInterruptedAppendSurvivesAnEditToTheParagraph(t *testing.T) {
	var interrupt *failOnceOnPutNote
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		interrupt = &failOnceOnPutNote{Store: s}
		return interrupt
	})
	ctx := context.Background()

	if _, err := f.run(ctx); err == nil {
		t.Fatal("expected the induced index-write failure to surface")
	}
	if !interrupt.didFail() {
		t.Fatal("the test did not actually interrupt the index write")
	}

	// The user rewrites the whole note from the editor, which never showed
	// them the marker. What they save is what they saw; what is stored keeps
	// the marker.
	const rewritten = "the user reworded the dictated sentence entirely"
	stored := f.body(t)
	if err := f.objects.Put(ctx, appendNoteKey, []byte(service.CarryCaptureMarkers(stored, rewritten)), "text/markdown"); err != nil {
		t.Fatalf("simulate the editor's save: %v", err)
	}

	rewindAppendClaim(t, f.store, asyncRetryOffsets[0])
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("retry after the edit: %v", err)
	}
	body := f.body(t)
	if strings.Contains(body, appendedText) {
		t.Fatalf("the retry appended the dictation again after the user had edited it away:\n%s", body)
	}
	if !strings.Contains(body, rewritten) {
		t.Fatalf("the user's edit was lost:\n%s", body)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", final.Status)
	}
}

// A worker that died BETWEEN taking the claim and writing the body. Nothing is
// in the note, so a retry inside the lease cannot tell this from a holder that
// is about to write; it must not append, and it must not concede either — it
// fails, so Lambda retries. Both automatic retries fall inside the twenty-minute
// lease, so both fail the same way, the payload dead-letters and the alarm
// fires; the attempt that arrives after the lease — the user's retry, or an
// operator redriving the DLQ — takes the claim over and appends once.
//
// This is the one case the transport change made slower, deliberately: under
// SQS the third delivery at 32 minutes recovered it; here a human is told at
// three. The window is one object read and one write, after every stage that
// can be slow has already persisted, so it is the rarest failure the pipeline
// has — and the alternative, appending inside the lease, races a live holder.
func TestRetryOfAnAppendThatDiedBeforeWritingWaitsForTheLease(t *testing.T) {
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

	// The two automatic retries, both inside the lease with an empty body.
	for i, offset := range asyncRetryOffsets {
		if offset >= repository.AppendClaimLease {
			t.Fatalf("retry %d at T+%s is outside the %s lease; the test no longer models the schedule it describes",
				i+1, offset, repository.AppendClaimLease)
		}
		rewindAppendClaim(t, f.store, offset)
		mid, err := f.run(ctx)
		if !errors.Is(err, errAppendClaimHeld) {
			t.Fatalf("retry %d at T+%s returned err = %v, want errAppendClaimHeld: "+
				"conceding strands the capture; appending races the holder", i+1, offset, err)
		}
		if body := f.body(t); body != "" {
			t.Fatalf("retry %d wrote to the note body inside the lease: %q", i+1, body)
		}
		if mid.Status != model.StatusAppending {
			t.Fatalf("status = %s after retry %d, want appending", mid.Status, i+1)
		}
	}

	// Past the lease — the user pressed retry once the alarm said so — the
	// claim is taken over and the append happens, once.
	rewindAppendClaim(t, f.store, repository.AppendClaimLease+time.Minute)
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("retry after the lease expired: %v", err)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the claim was taken over, want exactly 1:\n%s",
			got, f.body(t))
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", final.Status)
	}
}
