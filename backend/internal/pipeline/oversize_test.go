package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// s3EventOfSize is the notification the content bucket invokes the worker with,
// carrying the size S3 actually wrote. A presigned PUT signs neither
// Content-Length nor a length policy, so this is the first moment anything in
// the system learns how big the recording really is.
func s3EventOfSize(key string, size int64) json.RawMessage {
	body, err := json.Marshal(events.S3Event{
		Records: []events.S3EventRecord{{
			EventName: "ObjectCreated:Put",
			S3: events.S3Entity{
				Bucket: events.S3Bucket{Name: "chintan-content-test"},
				Object: events.S3Object{Key: key, Size: size},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// Ask for a URL for a 1 KB clip, PUT five gigabytes. S3 takes it. The worker is
// the first and last place that can refuse it, and it has to refuse before the
// object is streamed to a provider that bills by the audio second.
func TestWorkerRefusesAnObjectLargerThanTheCaptureLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	seedUploadedCapture(t, h, "note1")

	oversize := service.MaxCaptureBytes + 1
	worker := NewWorker(h.pipeline)
	if err := worker.Handle(context.Background(),
		s3EventOfSize("tenants/user1/captures/c_1/audio.webm", oversize)); err != nil {
		t.Fatalf("Handle: %v; an oversized upload is the client's fault, "+
			"not an infrastructure fault, so retrying it only fills the dead-letter queue", err)
	}

	if got := h.stt.Calls(); got != 0 {
		t.Fatalf("the speech provider was called %d time(s) on a %d byte object; "+
			"the upload limit is advisory and the bill is not", got, oversize)
	}

	ctx := context.Background()
	capture, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", capture.Status)
	}
	if capture.Error == "" || !strings.Contains(strings.ToLower(capture.Error), "large") {
		t.Fatalf("capture error = %q, want something that names the size limit", capture.Error)
	}

	if _, err := h.objects.Get(ctx, "tenants/user1/captures/c_1/audio.webm"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the oversized object is still in the bucket (get err = %v); "+
			"with versioning on and no retention rule it stays there and is billed for", err)
	}
}

// An ordinary recording still goes all the way through: the size check must not
// become a second way for a capture to fail.
func TestWorkerAcceptsAnObjectInsideTheCaptureLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	seedUploadedCapture(t, h, "note1")

	worker := NewWorker(h.pipeline)
	if err := worker.Handle(context.Background(),
		s3EventOfSize("tenants/user1/captures/c_1/audio.webm", 11)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	capture, err := h.store.GetCapture(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (error=%q), want appended", capture.Status, capture.Error)
	}
}
