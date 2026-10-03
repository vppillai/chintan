package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// A run that returns nil with the capture still in a stage is not a success:
// Lambda would read it as one and nothing would come back for the capture.
// The guard fails the invocation and counts it, so the retry runs and the
// alarm sees it. The stage is stubbed because no real stage is meant to do
// this; the incident's stage did.
func TestARunThatLeavesTheCapturePendingFailsTheInvocation(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	defer obs.SetMetricOutput(&metrics)()

	f := newAppendFixture(t, memory.NewObjects(), nil)
	f.h.pipeline.drive = func(ctx context.Context, capture *model.CaptureIndex) (model.CaptureIndex, error) {
		if err := f.h.pipeline.setStatus(ctx, capture, model.StatusAppending); err != nil {
			return *capture, err
		}
		return *capture, nil
	}

	final, err := f.run(ctx)
	if !errors.Is(err, errCaptureOrphaned) {
		t.Fatalf("run = %v, want errCaptureOrphaned", err)
	}
	if final.Status != model.StatusAppending {
		t.Fatalf("status = %s, want the stage left as it was (appending)", final.Status)
	}
	for _, want := range []string{`"CaptureOrphaned":1`, `"Status":"appending"`, `"CaptureStageFailures":1`, `"Stage":"appending"`} {
		if !strings.Contains(metrics.String(), want) {
			t.Errorf("metrics lack %s:\n%s", want, metrics.String())
		}
	}
}
