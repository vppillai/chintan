package reconcile

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

var now = time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)

func seed(t *testing.T, store *repository.DynamoStore, id string, status model.CaptureStatus, last time.Time) {
	t.Helper()
	if _, err := store.PutCapture(context.Background(), model.CaptureIndex{
		ID: id, UserID: "tenant-a", NoteID: "n1", Status: status,
		CreatedAt: model.FormatTime(last), LastProgressAt: model.FormatTime(last),
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// A capture stuck past the rule is run once more; one the run finishes is
// counted finished, one it leaves pending is marked failed with the fixed
// sentence; a fresh pending capture and one inside its append lease are left
// alone.
func TestRunFinishesOrFailsEveryStuckCapture(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	defer obs.SetMetricOutput(&metrics)()

	store := dynamofake.NewStore()
	stale := now.Add(-service.CaptureStuckAfter - time.Minute)
	seed(t, store, "resumes", model.StatusCleaned, stale)
	seed(t, store, "stays", model.StatusAppending, stale)
	seed(t, store, "fresh", model.StatusTranscribing, now.Add(-time.Minute))
	// Inside the claim lease the holder may still be writing: left alone.
	seed(t, store, "leased", model.StatusAppending, stale)
	if _, _, err := store.ClaimCaptureAppend(ctx, "tenant-a", "leased", "token"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	var ran []string
	run := func(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
		ran = append(ran, captureID)
		c, err := store.GetCapture(ctx, tenantID, captureID)
		if err != nil {
			return c, err
		}
		if captureID == "resumes" {
			c.Status = model.StatusAppended
			return store.PutCapture(ctx, c)
		}
		return c, errors.New("pipeline: run ended with the capture still pending")
	}
	r, err := New(store, memory.NewObjects(), run)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }

	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report != (Report{Stuck: 2, Finished: 1, Failed: 1}) {
		t.Fatalf("report = %+v, want 2 stuck, 1 finished, 1 failed", report)
	}
	if strings.Join(ran, ",") != "stays,resumes" && strings.Join(ran, ",") != "resumes,stays" {
		t.Fatalf("ran %v, want exactly the two stuck captures", ran)
	}
	stays, _ := store.GetCapture(ctx, "tenant-a", "stays")
	if stays.Status != model.StatusFailed || stays.Error != Verdict {
		t.Fatalf("stays = %s %q, want failed with the verdict", stays.Status, stays.Error)
	}
	for _, id := range []string{"fresh", "leased"} {
		c, _ := store.GetCapture(ctx, "tenant-a", id)
		if c.Status == model.StatusFailed {
			t.Errorf("%s was failed; it was not stuck", id)
		}
	}
	for _, want := range []string{`"CaptureReaped":1`, `"Outcome":"finished"`, `"Outcome":"failed"`} {
		if !strings.Contains(metrics.String(), want) {
			t.Errorf("metrics lack %s:\n%s", want, metrics.String())
		}
	}
}

// A capture the person retried while the pass was running keeps that: the
// failure is written only under the version the pass read.
func TestRunDoesNotFailACaptureSomeoneElseMoved(t *testing.T) {
	ctx := context.Background()
	store := dynamofake.NewStore()
	seed(t, store, "moved", model.StatusCleaned, now.Add(-time.Hour))

	run := func(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
		c, _ := store.GetCapture(ctx, tenantID, captureID)
		// Finished by another delivery after this run read it.
		c.Status = model.StatusAppended
		if _, err := store.PutCapture(ctx, c); err != nil {
			return c, err
		}
		return c, errors.New("lost a write")
	}
	r, _ := New(store, memory.NewObjects(), run)
	r.now = func() time.Time { return now }
	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("report = %+v, want nothing failed", report)
	}
	c, _ := store.GetCapture(ctx, "tenant-a", "moved")
	if c.Status != model.StatusAppended {
		t.Fatalf("status = %s, want the other delivery's appended kept", c.Status)
	}
}

// A run that returns nil with the capture still pending is one that conceded
// to a concurrent delivery (the pipeline fails any other such return). That
// delivery is still writing the row; the pass must not write failed over it.
func TestRunLeavesACaptureAConcurrentDeliveryOwns(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	defer obs.SetMetricOutput(&metrics)()
	store := dynamofake.NewStore()
	seed(t, store, "owned", model.StatusCleaned, now.Add(-time.Hour))

	run := func(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
		// The other delivery moved the row on; this run conceded to it.
		c, _ := store.GetCapture(ctx, tenantID, captureID)
		c.Status = model.StatusAppending
		c.LastProgressAt = model.FormatTime(now)
		return store.PutCapture(ctx, c)
	}
	r, _ := New(store, memory.NewObjects(), run)
	r.now = func() time.Time { return now }
	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report != (Report{Stuck: 1}) {
		t.Fatalf("report = %+v, want 1 stuck and nothing finished or failed", report)
	}
	c, _ := store.GetCapture(ctx, "tenant-a", "owned")
	if c.Status != model.StatusAppending || c.Error != "" {
		t.Fatalf("capture = %s %q, want the concurrent delivery's appending left alone", c.Status, c.Error)
	}
	if strings.Contains(metrics.String(), "CaptureReaped") {
		t.Fatalf("a conceded run was counted:\n%s", metrics.String())
	}
}

// An `uploaded` row whose recording has not landed is an upload in flight,
// not a stuck capture: running it would hand the provider a link to nothing
// and fail it, and the PUT landing afterwards would find a terminal row.
func TestRunLeavesAnUploadedCaptureWhoseRecordingHasNotLanded(t *testing.T) {
	ctx := context.Background()
	var metrics bytes.Buffer
	defer obs.SetMetricOutput(&metrics)()
	store := dynamofake.NewStore()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "pending-upload", UserID: "tenant-a", Status: model.StatusUploaded,
		AudioKey:  "tenants/tenant-a/captures/pending-upload/audio.webm",
		CreatedAt: model.FormatTime(now.Add(-16 * time.Minute)), LastProgressAt: model.FormatTime(now.Add(-16 * time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}

	ran := false
	run := func(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
		ran = true
		return model.CaptureIndex{}, errors.New("must not run")
	}
	r, _ := New(store, memory.NewObjects(), run)
	r.now = func() time.Time { return now }
	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran || report != (Report{}) {
		t.Fatalf("ran = %v, report = %+v; an upload in flight must be left alone", ran, report)
	}
	c, _ := store.GetCapture(ctx, "tenant-a", "pending-upload")
	if c.Status != model.StatusUploaded {
		t.Fatalf("status = %s, want uploaded", c.Status)
	}
	if strings.Contains(metrics.String(), "CaptureReaped") {
		t.Fatalf("an upload in flight was counted:\n%s", metrics.String())
	}
}
