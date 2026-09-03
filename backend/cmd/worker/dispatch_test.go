package main

import (
	"encoding/json"
	"testing"
)

// TestSourceOfDistinguishesTheTwoEventSources is the guard on the one decision
// this binary makes before any handler runs.
//
// Getting it wrong is not a no-op in either direction. A stream event read as a
// capture invocation goes to pipeline.Worker, which finds no capture it
// recognises; an S3 event read as a stream event goes to the purge cascade,
// which is the code that deletes objects. Neither may happen by accident, so
// the discriminator is the event's own eventSource field rather than a
// deployment-time setting that a template edit can put out of step with the
// trigger beside it.
func TestSourceOfDistinguishesTheTwoEventSources(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want eventSource
	}{
		{
			name: "an S3 notification",
			raw:  `{"Records":[{"eventSource":"aws:s3","s3":{"object":{"key":"tenants/u/captures/c/audio.webm"}}}]}`,
			want: sourceS3,
		},
		{
			name: "a DynamoDB stream batch",
			raw:  `{"Records":[{"eventSource":"aws:dynamodb","eventName":"REMOVE","dynamodb":{"SequenceNumber":"1"}}]}`,
			want: sourceDynamoDBStream,
		},
		{
			// The API's payload names a capture directly and has no records at
			// all; it goes to the pipeline worker with the S3 notifications.
			name: "the API's invocation",
			raw:  `{"tenant_id":"u","capture_id":"c","reason":"retry"}`,
			want: "",
		},
		{
			// Lambda can deliver an empty batch. It is not an error and there is
			// nothing to run.
			name: "no records",
			raw:  `{"Records":[]}`,
			want: "",
		},
		{
			name: "something neither handler serves",
			raw:  `{"Records":[{"eventSource":"aws:sqs","messageId":"m1","body":"{}"}]}`,
			want: "aws:sqs",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sourceOf(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("sourceOf: %v", err)
			}
			if got != tc.want {
				t.Errorf("sourceOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSourceOfRejectsAnUndecodableEvent keeps a malformed payload from being
// read as "no records", which would hand it to the pipeline worker as if it
// were the API's payload.
func TestSourceOfRejectsAnUndecodableEvent(t *testing.T) {
	if _, err := sourceOf(json.RawMessage(`not json`)); err == nil {
		t.Fatal("sourceOf accepted a payload that is not JSON")
	}
}
