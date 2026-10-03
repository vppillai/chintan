package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// refuseClaims is the store whose first n ClaimCaptureAppend calls are
// refused with the row as it stands — a free claim, nobody's token — the way
// a claim conditioned on a stale read, or one that lost the version race to a
// duplicate delivery's status write, is refused. The calls after n are real.
type refuseClaims struct {
	repository.Store
	mu    sync.Mutex
	n     int
	calls int
}

func (r *refuseClaims) ClaimCaptureAppend(ctx context.Context, tenantID, captureID, token string) (bool, model.CaptureIndex, error) {
	r.mu.Lock()
	r.calls++
	refuse := r.calls <= r.n
	r.mu.Unlock()
	if refuse {
		current, err := r.GetCapture(ctx, tenantID, captureID)
		return false, current, err
	}
	return r.Store.ClaimCaptureAppend(ctx, tenantID, captureID, token)
}

// A claim refused while nobody holds it is asked again, once, and the append
// proceeds when the second claim is granted. This is the incident's shape:
// the refusal was read as a foreign owner and the attempt returned nil,
// leaving the capture in `appending` for good.
func TestAClaimRefusedWithNobodyHoldingItIsRetriedOnce(t *testing.T) {
	ctx := context.Background()
	store := &refuseClaims{n: 1}
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		store.Store = s
		return store
	})

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", final.Status)
	}
	if got := f.body(t); strings.Count(got, appendedText) != 1 {
		t.Fatalf("body = %q, want the paragraph exactly once", got)
	}
	if store.calls != 2 {
		t.Fatalf("ClaimCaptureAppend was called %d times, want 2 (the refusal and the retry)", store.calls)
	}
}

// Refused twice with nobody holding it, the attempt fails the invocation —
// counted under the stage, so the alarm sees it — rather than returning nil
// with the capture still pending.
func TestAClaimRefusedTwiceWithNobodyHoldingItFailsTheInvocation(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	defer obs.SetMetricOutput(&metrics)()

	store := &refuseClaims{n: 2}
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		store.Store = s
		return store
	})

	_, err := f.run(ctx)
	if !errors.Is(err, errAppendClaimLost) {
		t.Fatalf("run = %v, want errAppendClaimLost", err)
	}
	if got := f.body(t); got != "" {
		t.Fatalf("body = %q, want nothing written", got)
	}
	for _, want := range []string{`"CaptureStageFailures":1`, `"Stage":"appending"`} {
		if !strings.Contains(metrics.String(), want) {
			t.Errorf("metrics lack %s; a lost claim would be invisible to the alarm:\n%s", want, metrics.String())
		}
	}
}
