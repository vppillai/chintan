package main

import (
	"encoding/json"
	"testing"
)

// TestSourceOfDistinguishesTheTwoEventSources is the guard on the one decision
// this binary makes before any handler runs.
//
// Getting it wrong is not a no-op in either direction. A stream event read as a
// queue event goes to pipeline.Worker, which finds no message body it
// recognises; a queue event read as a stream event goes to the purge cascade,
// which is the code that deletes objects. Neither may happen by accident, so
// the discriminator is the event's own eventSource field rather than a
// deployment-time setting that a template edit can put out of step with the
// event source mapping beside it.
func TestSourceOfDistinguishesTheTwoEventSources(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want eventSource
	}{
		{
			name: "an SQS batch",
			raw:  `{"Records":[{"eventSource":"aws:sqs","messageId":"m1","body":"{}"}]}`,
			want: sourceSQS,
		},
		{
			name: "a DynamoDB stream batch",
			raw:  `{"Records":[{"eventSource":"aws:dynamodb","eventName":"REMOVE","dynamodb":{"SequenceNumber":"1"}}]}`,
			want: sourceDynamoDBStream,
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
			raw:  `{"Records":[{"eventSource":"aws:s3"}]}`,
			want: "aws:s3",
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
// read as "no records", which would report a whole batch as successfully
// handled without handling any of it.
func TestSourceOfRejectsAnUndecodableEvent(t *testing.T) {
	if _, err := sourceOf(json.RawMessage(`not json`)); err == nil {
		t.Fatal("sourceOf accepted a payload that is not JSON")
	}
}
