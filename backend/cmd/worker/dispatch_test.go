package main

import (
	"encoding/json"
	"testing"
)

// TestSniffDistinguishesTheThreeInvocations is the guard on the one decision
// this binary makes before any handler runs.
//
// Getting it wrong is not a no-op in either direction. The sweep's payload read
// as a capture invocation reaches pipeline.Worker, which finds no capture and
// returns nil — a week of expiries silently skipped. An S3 event read as a
// task would be dropped as unrecognised, and the recording never transcribed.
// So the discriminator is the payload's own fields rather than a
// deployment-time setting that a template edit can put out of step with the
// trigger beside it.
func TestSniffDistinguishesTheThreeInvocations(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want invocation
	}{
		{
			name: "an S3 notification",
			raw:  `{"Records":[{"eventSource":"aws:s3","s3":{"object":{"key":"tenants/u/captures/c/audio.webm"}}}]}`,
			want: invocation{source: sourceS3},
		},
		{
			// The EventBridge rule's constant input.
			name: "the weekly sweep",
			raw:  `{"task":"sweep-expired"}`,
			want: invocation{task: "sweep-expired"},
		},
		{
			// The other EventBridge rule's constant input.
			name: "the daily AWS cost reading",
			raw:  `{"task":"aws-cost"}`,
			want: invocation{task: "aws-cost"},
		},
		{
			// The third rule's constant input.
			name: "the daily storage snapshot",
			raw:  `{"task":"storage-snapshot"}`,
			want: invocation{task: "storage-snapshot"},
		},
		{
			// The clean-note task, from the API or from the worker itself. It
			// goes to the pipeline worker too, which dispatches on the task.
			name: "a clean-note task",
			raw:  `{"task":"clean-note","tenant_id":"u","note_id":"n","mode":"structured"}`,
			want: invocation{task: "clean-note"},
		},
		{
			// The API's payload names a capture directly and has no records at
			// all; it goes to the pipeline worker with the S3 notifications.
			name: "the API's invocation",
			raw:  `{"tenant_id":"u","capture_id":"c","reason":"retry"}`,
			want: invocation{},
		},
		{
			// Lambda can deliver an empty batch. It is not an error and there is
			// nothing to run.
			name: "no records",
			raw:  `{"Records":[]}`,
			want: invocation{},
		},
		{
			name: "something no handler serves",
			raw:  `{"Records":[{"eventSource":"aws:dynamodb","eventName":"REMOVE"}]}`,
			want: invocation{source: "aws:dynamodb"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sniff(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("sniff: %v", err)
			}
			if got != tc.want {
				t.Errorf("sniff = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSniffRejectsAnUndecodableEvent keeps a malformed payload from being read
// as "no records", which would hand it to the pipeline worker as if it were the
// API's payload.
func TestSniffRejectsAnUndecodableEvent(t *testing.T) {
	if _, err := sniff(json.RawMessage(`not json`)); err == nil {
		t.Fatal("sniff accepted a payload that is not JSON")
	}
}
