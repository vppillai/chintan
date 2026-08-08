package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"github.com/vppillai/chintan/backend/internal/obs"
)

// CorrelationAttribute is the SQS message attribute carrying the id that ties
// one capture's logs together from upload through append.
const CorrelationAttribute = "correlation_id"

// Message is the body the API puts on the queue when it re-enqueues a capture.
//
// The queue also receives raw S3 ObjectCreated notifications, which carry no
// body of ours at all — the worker recognises both.
type Message struct {
	TenantID  string `json:"tenant_id"`
	CaptureID string `json:"capture_id"`
	Reason    string `json:"reason,omitempty"`
}

// Worker drains the capture queue.
type Worker struct {
	pipeline *Pipeline
}

// NewWorker builds the SQS handler.
func NewWorker(p *Pipeline) *Worker { return &Worker{pipeline: p} }

// Handle processes one SQS batch and reports which records failed.
//
// The event source is configured with BatchSize 1 and ReportBatchItemFailures:
// a batch failure must not redeliver captures that already appended, and the
// per-item report is what makes a partial failure mean only itself.
func (w *Worker) Handle(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse

	for _, record := range event.Records {
		recCtx := obs.WithCorrelationID(ctx, correlationFrom(record))

		refs, err := parseRecord(record)
		if err != nil {
			// A message we cannot address is not retryable: redelivering it three
			// times only fills the DLQ with the same parse failure. Log it and let
			// it go.
			obs.Log(recCtx).Error("discarding an unparseable queue message",
				slog.String("message_id", record.MessageId),
				slog.String("error", err.Error()))
			obs.Count(recCtx, "WorkerMessagesDiscarded", map[string]string{"Reason": "unparseable"})
			continue
		}
		if len(refs) == 0 {
			// S3 test events and notifications for objects the worker does not own.
			obs.Log(recCtx).Debug("queue message addressed no capture",
				slog.String("message_id", record.MessageId))
			continue
		}

		for _, ref := range refs {
			if _, err := w.pipeline.Run(recCtx, ref.TenantID, ref.CaptureID); err != nil {
				obs.Log(recCtx).Error("capture will be retried",
					slog.String("message_id", record.MessageId),
					slog.String("capture_id", ref.CaptureID),
					slog.String("error", err.Error()))
				resp.BatchItemFailures = append(resp.BatchItemFailures,
					events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
				break
			}
		}
	}

	return resp, nil
}

// correlationFrom prefers the id the API minted, so one capture is one greppable
// trace from the create request through the append.
//
// An S3 ObjectCreated notification carries no attribute of ours, so the first
// upload of a capture starts a fresh trace here. That is the honest outcome: the
// alternative is inventing a join that does not exist.
func correlationFrom(record events.SQSMessage) string {
	if attr, ok := record.MessageAttributes[CorrelationAttribute]; ok && attr.StringValue != nil {
		if id, ok := obs.SanitizeCorrelationID(*attr.StringValue); ok {
			return id
		}
	}
	return obs.NewCorrelationID()
}

// CaptureRef addresses one capture.
type CaptureRef struct {
	TenantID  string
	CaptureID string
}

// parseRecord resolves a queue message to the captures it refers to. It accepts
// both shapes the queue carries: our own Message, and an S3 event notification.
func parseRecord(record events.SQSMessage) ([]CaptureRef, error) {
	body := strings.TrimSpace(record.Body)
	if body == "" {
		return nil, fmt.Errorf("pipeline: empty message body")
	}

	var probe struct {
		TenantID  string            `json:"tenant_id"`
		CaptureID string            `json:"capture_id"`
		Event     string            `json:"Event"`
		Records   []json.RawMessage `json:"Records"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return nil, fmt.Errorf("pipeline: decode message: %w", err)
	}

	if probe.TenantID != "" && probe.CaptureID != "" {
		return []CaptureRef{{TenantID: probe.TenantID, CaptureID: probe.CaptureID}}, nil
	}
	if probe.Event == "s3:TestEvent" {
		return nil, nil
	}
	if len(probe.Records) == 0 {
		return nil, fmt.Errorf("pipeline: message addressed no capture")
	}

	var event events.S3Event
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return nil, fmt.Errorf("pipeline: decode s3 event: %w", err)
	}

	refs := make([]CaptureRef, 0, len(event.Records))
	for _, r := range event.Records {
		ref, ok := captureFromAudioKey(r.S3.Object.Key)
		if !ok {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// audioKeyPattern mirrors keys.CaptureAudio. The identifier charset is the one
// internal/keys enforces on the way in, so a key that does not match it did not
// come from this system.
var audioKeyPattern = regexp.MustCompile(`^tenants/([A-Za-z0-9_-]+)/captures/([A-Za-z0-9_-]+)/audio\.[A-Za-z0-9]+$`)

// captureFromAudioKey recovers the tenant and capture from an uploaded object
// key. S3 percent-encodes the key in a notification, so it is decoded first.
func captureFromAudioKey(key string) (CaptureRef, bool) {
	decoded, err := url.QueryUnescape(strings.ReplaceAll(key, "+", "%20"))
	if err != nil {
		decoded = key
	}
	m := audioKeyPattern.FindStringSubmatch(decoded)
	if m == nil {
		return CaptureRef{}, false
	}
	return CaptureRef{TenantID: m[1], CaptureID: m[2]}, true
}
