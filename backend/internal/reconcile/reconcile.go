// Package reconcile finds captures the pipeline left behind and finishes
// them, one way or the other.
//
// A capture moves through the worker's stages by one invocation at a time,
// and the only things that come back for one that stops are Lambda's two
// retries of a failed invocation and the person's own Retry. An invocation
// that returns nil with the capture still in a stage gets neither: nothing is
// retried, nothing is dead-lettered, no alarm reads it, and the row sits in
// `appending` until someone opens the app and wonders. The pipeline guards
// against returning that way, and this task is the backstop for whatever the
// guard does not see — a worker killed between its last write and its exit,
// a stage added later that returns the wrong thing.
//
// An EventBridge rule invokes the worker every fifteen minutes with
// {"task":"reconcile-stuck"}. Run asks the store for every pending capture
// last written before service.CaptureStuckAfter ago, runs the pipeline once
// more for each — a resume is idempotent: every stage's artefact is on the
// row, and the append is guarded by its claim and its marker — and marks
// failed, with one fixed sentence, any capture the run still left pending,
// so the person sees a Retry button rather than a spinner.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// Task is the payload field value the EventBridge rule sends, and cmd/worker
// dispatches on. The template's rule and this constant must agree.
const Task = "reconcile-stuck"

// Verdict is the one sentence a capture this task gives up on carries. Fixed,
// because it reaches the person; the worker log carries the rest.
const Verdict = "Filing did not finish. Retry to try again."

// Store is the slice of repository.Store the task reads and writes.
type Store interface {
	StuckCaptures(ctx context.Context, before string) ([]model.CaptureIndex, error)
	GetCapture(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error)
	PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error)
}

// Objects is the slice of repository.Objects the task reads: whether an
// `uploaded` capture's recording has landed.
type Objects interface {
	Exists(ctx context.Context, key string) (bool, error)
}

// Runner runs the pipeline for one capture: pipeline.Pipeline.Run.
type Runner func(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error)

// Reaper runs one reconcile pass.
type Reaper struct {
	store   Store
	objects Objects
	run     Runner
	now     func() time.Time
}

// New builds the reaper. Every dependency is required: without the store
// there is nothing to find, without the bucket an upload still in flight
// would be run against a missing object, and without the pipeline nothing
// is resumed.
func New(store Store, objects Objects, run Runner) (*Reaper, error) {
	switch {
	case store == nil:
		return nil, fmt.Errorf("reconcile: store is required")
	case objects == nil:
		return nil, fmt.Errorf("reconcile: objects are required")
	case run == nil:
		return nil, fmt.Errorf("reconcile: runner is required")
	}
	return &Reaper{store: store, objects: objects, run: run, now: time.Now}, nil
}

// Report is what one pass did.
type Report struct {
	// Stuck is how many pending captures were past the stuck rule.
	Stuck int
	// Finished is how many of them the second run carried to a terminal
	// status — appended, or failed on the pipeline's own verdict.
	Finished int
	// Failed is how many it could not, now marked failed with Verdict.
	Failed int
}

// Run resumes every stuck capture once and fails the ones still pending.
//
// One capture does not stop the others. A capture that stays pending is a
// verdict, recorded on the row; it is not an error from this task, which
// returns one only when the store could not be read, so Lambda's retry is
// spent on a store fault and not on a capture that already got its answer.
func (r *Reaper) Run(ctx context.Context) (Report, error) {
	var report Report
	now := r.now()
	before := model.FormatTime(now.Add(-service.CaptureStuckAfter))

	stuck, err := r.store.StuckCaptures(ctx, before)
	if err != nil {
		return report, fmt.Errorf("reconcile: list stuck captures: %w", err)
	}
	for _, c := range stuck {
		// The store's cutoff is the plain rule; CaptureStuck adds the append
		// claim's lease, inside which the holder may still be writing.
		if !service.CaptureStuck(c, now) {
			continue
		}
		cctx := obs.WithTenant(ctx, c.UserID)
		log := obs.Log(cctx).With(slog.String("capture_id", c.ID), slog.String("status", string(c.Status)))

		if c.Status == model.StatusUploaded && c.AudioKey != "" {
			// An `uploaded` row with no object is an upload still on its
			// way, or one the app was closed in the middle of: the row is
			// old because nothing has happened to it, not because a worker
			// died. Running it would hand the provider a link to nothing,
			// fail the capture and page, and a PUT landing afterwards would
			// find a terminal row. The server's own Retry refuses this row
			// (service.RetryCapture, uploadMayStillLand), and so does this.
			present, err := r.objects.Exists(cctx, c.AudioKey)
			if err != nil {
				log.Error("could not check for the recording's audio; the next pass will try again", slog.String("error", err.Error()))
				continue
			}
			if !present {
				log.Info("uploaded capture has no recording yet; leaving it for the upload")
				continue
			}
		}

		report.Stuck++
		log.Warn("capture is stuck in a pipeline stage; running it again")
		final, runErr := r.run(cctx, c.UserID, c.ID)
		if runErr == nil {
			if service.CaptureIsPending(final.Status) {
				// The pipeline returns nil with a pending status only for a
				// capture another delivery owns (errDeliveryConceded): a
				// person's Retry or a Lambda retry took it while this pass
				// was reading. That delivery finishes it; failing it here
				// would write over live work.
				log.Info("stuck capture is owned by a concurrent delivery; leaving it to that one",
					slog.String("final_status", string(final.Status)))
				continue
			}
			report.Finished++
			log.Info("stuck capture finished on its second run", slog.String("final_status", string(final.Status)))
			obs.Count(cctx, "CaptureReaped", map[string]string{"Outcome": "finished"})
			continue
		}
		log.Error("stuck capture did not finish on its second run", slog.String("error", runErr.Error()))
		failed, err := r.fail(cctx, c.UserID, c.ID)
		if err != nil {
			log.Error("could not mark the stuck capture failed; the next pass will try again", slog.String("error", err.Error()))
			continue
		}
		if !failed {
			// Finished, or moved on, by someone else between the run and
			// the read: not this pass's to count.
			continue
		}
		report.Failed++
		obs.Count(cctx, "CaptureReaped", map[string]string{"Outcome": "failed"})
	}

	obs.Log(ctx).Info("reconcile pass finished",
		slog.Int("stuck", report.Stuck),
		slog.Int("finished", report.Finished),
		slog.Int("failed", report.Failed))
	return report, nil
}

// fail marks the capture failed with Verdict, under the row's current version
// and only while it is still pending, and reports whether it did: a run that
// finished the capture after returning an error, or a person's Retry in
// between, stands.
func (r *Reaper) fail(ctx context.Context, tenantID, captureID string) (bool, error) {
	current, err := r.store.GetCapture(ctx, tenantID, captureID)
	if err != nil {
		return false, err
	}
	if !service.CaptureIsPending(current.Status) {
		return false, nil
	}
	current.Status = model.StatusFailed
	current.Error = Verdict
	current.LastProgressAt = model.FormatTime(r.now())
	if _, err := r.store.PutCapture(ctx, current); err != nil {
		if errors.Is(err, repository.ErrVersionConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
