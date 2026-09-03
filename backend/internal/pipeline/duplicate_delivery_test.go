package pipeline

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// foreignWriteBeforeFirstPut lands one write from "the other delivery" in the
// window between this pipeline's read and its first conditional write, so the
// pipeline loses that write every single time.
//
// This is the deterministic form of the race. Driving it with two goroutines and
// hoping they overlap is how the concurrency test passed for a while without
// ever exercising the conflict at all: one goroutine simply finished before the
// other started, and the assertion held for the wrong reason.
type foreignWriteBeforeFirstPut struct {
	repository.Store
	mu     sync.Mutex
	done   bool
	before func(ctx context.Context, store repository.Store, current model.CaptureIndex)
}

func (s *foreignWriteBeforeFirstPut) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	s.mu.Lock()
	first := !s.done
	s.done = true
	s.mu.Unlock()

	if first {
		//nolint:staticcheck // QF1008: s.Store.X is this wrapper's "call the real store" idiom, and the s.Store.PutCapture below it cannot drop the field without recursing.
		current, err := s.Store.GetCapture(ctx, c.UserID, c.ID)
		if err != nil {
			return model.CaptureIndex{}, err
		}
		s.before(ctx, s.Store, current)
	}
	return s.Store.PutCapture(ctx, c)
}

// A duplicate delivery that arrives after the other one has already appended
// must exit successfully. It has nothing left to do, and reporting a failure
// would have Lambda retry it twice more and then dead-letter it — an alarm
// raised against a transport behaving exactly as an at-least-once transport
// behaves.
func TestADuplicateDeliveryOfAFinishedCaptureExitsCleanly(t *testing.T) {
	ctx := context.Background()
	var objects repository.Objects = memory.NewObjects()

	f := newAppendFixture(t, objects, func(s repository.Store) repository.Store {
		return &foreignWriteBeforeFirstPut{
			Store: s,
			before: func(ctx context.Context, store repository.Store, current model.CaptureIndex) {
				// The other delivery got there first: it wrote the text into the
				// note body, claimed the append, and marked the capture done.
				if err := objects.Put(ctx, appendNoteKey, []byte(appendedText), "text/markdown"); err != nil {
					t.Errorf("foreign append: %v", err)
				}
				claimed, _, err := store.ClaimCaptureAppend(ctx, current.UserID, current.ID,
					appendToken(current.ID, current.CleanKey))
				if err != nil || !claimed {
					t.Errorf("foreign claim: claimed=%v err=%v", claimed, err)
				}
				if _, err := store.CompleteCaptureAppend(ctx, current.UserID, current.ID,
					appendToken(current.ID, current.CleanKey)); err != nil {
					t.Errorf("foreign complete: %v", err)
				}
			},
		}
	})

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("a duplicate delivery of a finished capture must not fail; Lambda would retry it: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want the reloaded truth (appended), not this delivery's stale copy", final.Status)
	}
	if !service.CaptureIsTerminal(final.Status) {
		t.Fatalf("status = %s, want a terminal state", final.Status)
	}

	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("note body contains the dictated text %d times, want exactly 1:\n%s", got, f.body(t))
	}
}

// A duplicate delivery that arrives while the other one is still mid-flight must
// also exit successfully, and must write nothing.
//
// Conceding drops no work: if the owner dies, its own invocation fails and is
// retried, and that retry finishes the interrupted attempt.
func TestADuplicateDeliveryOfAnInFlightCaptureConcedesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	var objects repository.Objects = memory.NewObjects()

	f := newAppendFixture(t, objects, func(s repository.Store) repository.Store {
		return &foreignWriteBeforeFirstPut{
			Store: s,
			before: func(ctx context.Context, store repository.Store, current model.CaptureIndex) {
				// The other delivery has entered the append stage and holds the
				// claim, but has not written the body yet.
				owner := current
				owner.Status = service.StatusAppending
				if _, err := store.PutCapture(ctx, owner); err != nil {
					t.Errorf("foreign status write: %v", err)
				}
				claimed, _, err := store.ClaimCaptureAppend(ctx, current.UserID, current.ID,
					appendToken(current.ID, current.CleanKey))
				if err != nil || !claimed {
					t.Errorf("foreign claim: claimed=%v err=%v", claimed, err)
				}
			},
		}
	})

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("a duplicate delivery of an in-flight capture must not fail: %v", err)
	}
	if service.CaptureIsTerminal(final.Status) {
		t.Fatalf("status = %s; the owner has not finished, so this delivery must not claim it has", final.Status)
	}
	if final.Status != service.StatusAppending {
		t.Fatalf("status = %s, want the reloaded appending state", final.Status)
	}

	// The conceding delivery must not have touched the note body: the owner
	// still has the append to do, and a second copy of the text is the exact
	// defect the claim exists to prevent.
	if body := f.body(t); body != "" {
		t.Fatalf("the conceding delivery wrote to the note body: %q", body)
	}
}

// A lost write is visible as a counter rather than as silence, because a
// sustained rate of these means something is invoking the worker twice for
// every capture.
func TestConcedingEmitsADuplicateDeliveryCounter(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	defer restore()

	var objects repository.Objects = memory.NewObjects()
	f := newAppendFixture(t, objects, func(s repository.Store) repository.Store {
		return &foreignWriteBeforeFirstPut{
			Store: s,
			before: func(ctx context.Context, store repository.Store, current model.CaptureIndex) {
				owner := current
				owner.Status = service.StatusAppending
				if _, err := store.PutCapture(ctx, owner); err != nil {
					t.Errorf("foreign status write: %v", err)
				}
			},
		}
	})

	if _, err := f.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(metrics.String(), `"DuplicateDelivery":1`) {
		t.Fatalf("no DuplicateDelivery metric was emitted; a conceded delivery would be invisible:\n%s", metrics.String())
	}
}

// The reload itself can fail, and that genuinely is retryable: we know this
// delivery lost, but not to what, so the message must come back.
func TestAFailedReloadAfterALostWriteStaysRetryable(t *testing.T) {
	ctx := context.Background()
	var objects repository.Objects = memory.NewObjects()

	f := newAppendFixture(t, objects, func(s repository.Store) repository.Store {
		return &failReloadAfterConflict{
			Store: &foreignWriteBeforeFirstPut{
				Store: s,
				before: func(ctx context.Context, store repository.Store, current model.CaptureIndex) {
					owner := current
					owner.Status = service.StatusAppending
					if _, err := store.PutCapture(ctx, owner); err != nil {
						t.Errorf("foreign status write: %v", err)
					}
				},
			},
		}
	})

	if _, err := f.run(ctx); err == nil {
		t.Fatal("a capture that lost a write and could not be reloaded must be retried, not silently dropped")
	}
}

// failReloadAfterConflict answers the reload that follows a conceded write, and
// nothing else: the first GetCapture is the pipeline's own load at Run.
type failReloadAfterConflict struct {
	repository.Store
	mu   sync.Mutex
	seen int
}

func (s *failReloadAfterConflict) GetCapture(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
	s.mu.Lock()
	s.seen++
	n := s.seen
	s.mu.Unlock()
	if n > 1 {
		return model.CaptureIndex{}, context.DeadlineExceeded
	}
	return s.Store.GetCapture(ctx, tenantID, captureID)
}
