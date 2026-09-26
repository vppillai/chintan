package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// s3EventAt is s3EventOfSize with the record's eventTime, which is what S3
// stamps and what a Lambda retry redelivers unchanged.
func s3EventAt(key string, at time.Time) json.RawMessage {
	body, err := json.Marshal(events.S3Event{
		Records: []events.S3EventRecord{{
			EventName: "ObjectCreated:Put",
			EventTime: at,
			S3: events.S3Entity{
				Bucket: events.S3Bucket{Name: "chintan-content-test"},
				Object: events.S3Object{Key: key, Size: 11},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// An upload that lands after its capture row was deleted — DELETE lets a stuck
// upload go after fifteen minutes, the presigned PUT is good for thirty — must
// not fail the invocation three times into the dead-letter queue and leave the
// audio in the bucket for good. But GetCapture is an eventually consistent
// read, so the first delivery may simply be early: it fails and is retried as
// it always was, and it is the retry, a minute on, that removes the object.
func TestWorkerRemovesAnObjectWhoseCaptureWasDeleted(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	key := "tenants/user1/captures/c_1/audio.webm"
	if err := h.objects.Put(ctx, key, []byte("audio bytes"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	worker := NewWorker(h.pipeline)
	event := s3EventAt(key, h.clock.Now())

	// First delivery, seconds after the write: the row may be on its way.
	if err := worker.Handle(ctx, event); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("first delivery: Handle = %v, want the not-found fault so Lambda retries it", err)
	}
	if _, err := h.objects.Get(ctx, key); err != nil {
		t.Fatalf("the first delivery removed the object (get err = %v); a stale read would have lost a live recording", err)
	}

	// The retry, a minute later: the row is still gone, so it was deleted.
	h.clock.Advance(time.Minute)
	if err := worker.Handle(ctx, event); err != nil {
		t.Fatalf("retry: Handle = %v; a row deleted by hand is not an infrastructure fault, "+
			"so retrying it only fills the dead-letter queue", err)
	}
	if _, err := h.objects.Get(ctx, key); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the orphaned object is still in the bucket (get err = %v)", err)
	}
	if _, err := h.store.GetCapture(ctx, "user1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a row came back for the deleted capture: %v", err)
	}
	if got := h.stt.Calls(); got != 0 {
		t.Fatalf("the speech provider was called %d time(s) for a capture that no longer exists", got)
	}

	// A direct invocation for a missing row carries no object and is still the
	// retryable fault it was: there is nothing to clean up, and the row may be
	// on its way.
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Run for a missing row = %v, want the not-found fault", err)
	}
}
