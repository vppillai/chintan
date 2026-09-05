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

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// Invocation is the payload the API sends when it hands a capture back to the
// worker — POST /v1/captures/{id}/retry and /target — by invoking the worker
// Lambda asynchronously. The worker also accepts a raw S3 event notification,
// which is how the first attempt at every capture starts.
type Invocation struct {
	TenantID  string `json:"tenant_id"`
	CaptureID string `json:"capture_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// Task names a job that is not a capture — the weekly expiry sweep's
	// EventBridge rule sends {"task":"sweep-expired"}, and the API and the
	// pipeline itself send {"task":"clean-note"} (TaskCleanNote). cmd/worker
	// dispatches on it: the sweep never reaches this package, the clean-note
	// task is handled by Worker.Handle, and any other task that reaches the
	// capture path is refused rather than misread.
	Task string `json:"task,omitempty"`
	// NoteID and Mode address a clean-note task: the note whose cleaned view
	// is regenerated and the mode to write it in.
	NoteID string `json:"note_id,omitempty"`
	Mode   string `json:"mode,omitempty"`
	// CorrelationID is the id the API minted for the request, so the worker's
	// log lines join the API's into one trace.
	CorrelationID string `json:"correlation_id,omitempty"`
}

// Worker runs the pipeline for one asynchronous invocation.
type Worker struct {
	pipeline *Pipeline
}

// NewWorker builds the Lambda handler for the capture pipeline.
func NewWorker(p *Pipeline) *Worker { return &Worker{pipeline: p} }

// Handle processes one asynchronous invocation: either an S3 ObjectCreated
// event from the content bucket or an Invocation from the API.
//
// The return value is the whole protocol. nil means done — the capture
// finished, or failed on its own terms, or belongs to another delivery — and
// Lambda must not retry. An error means an infrastructure fault interrupted
// the work, and Lambda retries the same payload twice more before sending it
// to the dead-letter queue; every stage persists its artefact, so the retry
// resumes where the fault struck rather than re-transcribing.
func (w *Worker) Handle(ctx context.Context, raw json.RawMessage) error {
	if task, ok := parseCleanNoteTask(raw); ok {
		return w.handleCleanNote(ctx, task)
	}

	refs, correlationID, err := parseInvocation(raw)
	if err != nil {
		// A payload we cannot address is not retryable: retrying it twice only
		// fills the DLQ with the same parse failure. Log it and let it go.
		obs.Log(ctx).Error("discarding an unparseable invocation",
			slog.String("error", err.Error()))
		obs.Count(ctx, "WorkerMessagesDiscarded", map[string]string{"Reason": "unparseable"})
		return nil
	}
	if len(refs) == 0 {
		// Notifications for objects the worker does not own — nothing in the
		// bucket's filter should produce one, but a key that is not a recording
		// is not a reason to fail.
		obs.Log(ctx).Debug("invocation addressed no capture")
		return nil
	}

	for _, ref := range refs {
		recCtx := obs.WithCorrelationID(ctx, correlationFor(correlationID, ref))

		// The size check comes before anything that costs money.
		//
		// The presigned PUT bounds nothing: max_bytes is advice in the
		// response body, and the signature covers no length at all, so a URL
		// issued for a one-kilobyte clip accepts five gigabytes. This is the
		// first moment the truth is available and the last moment before the
		// object is handed to a provider that bills by the audio second.
		if ref.SizeBytes > service.MaxCaptureBytes {
			if err := w.pipeline.RejectOversizedCapture(recCtx, ref); err != nil {
				// Recording the verdict failed, not the verdict itself: retry.
				obs.Log(recCtx).Error("could not record an oversized capture; it will be retried",
					slog.String("capture_id", ref.CaptureID),
					slog.String("error", err.Error()))
				return err
			}
			continue
		}

		if _, err := w.pipeline.Run(recCtx, ref.TenantID, ref.CaptureID); err != nil {
			obs.Log(recCtx).Error("capture will be retried",
				slog.String("capture_id", ref.CaptureID),
				slog.String("error", err.Error()))
			return err
		}
	}
	return nil
}

// handleCleanNote runs one clean-note task. A payload that names the task but
// not a note is discarded like any other unparseable invocation: retrying it
// cannot make it addressable.
func (w *Worker) handleCleanNote(ctx context.Context, task Invocation) error {
	if task.TenantID == "" || task.NoteID == "" {
		obs.Log(ctx).Error("discarding a clean-note invocation that names no note")
		obs.Count(ctx, "WorkerMessagesDiscarded", map[string]string{"Reason": "unparseable"})
		return nil
	}
	id, ok := obs.SanitizeCorrelationID(task.CorrelationID)
	if !ok {
		id, ok = obs.SanitizeCorrelationID("clean-" + task.NoteID)
		if !ok {
			id = obs.NewCorrelationID()
		}
	}
	ctx = obs.WithCorrelationID(ctx, id)
	if err := w.pipeline.CleanNote(ctx, task.TenantID, task.NoteID, model.NoteCleanMode(task.Mode)); err != nil {
		obs.Log(ctx).Error("clean-note will be retried",
			slog.String("note_id", task.NoteID),
			slog.String("error", err.Error()))
		return err
	}
	return nil
}

// parseCleanNoteTask reads a clean-note payload. ok is false for anything else,
// including a payload that does not decode: the capture path reports that.
func parseCleanNoteTask(raw json.RawMessage) (Invocation, bool) {
	var inv Invocation
	if err := json.Unmarshal(raw, &inv); err != nil || inv.Task != TaskCleanNote {
		return Invocation{}, false
	}
	return inv, true
}

// correlationFor picks the id one capture's log lines are tied together by.
//
// The API's invocation carries the id it minted, so a retry is one greppable
// trace from the button press through the append. An S3 notification carries
// nothing of ours, so the first attempt at a capture is traced by the capture
// id itself — which is also what the two automatic retries of that attempt
// will use, so all three land in the same trace.
func correlationFor(fromInvocation string, ref CaptureRef) string {
	if id, ok := obs.SanitizeCorrelationID(fromInvocation); ok {
		return id
	}
	if id, ok := obs.SanitizeCorrelationID("capture-" + ref.CaptureID); ok {
		return id
	}
	return obs.NewCorrelationID()
}

// CaptureRef addresses one capture.
type CaptureRef struct {
	TenantID  string
	CaptureID string
	// ObjectKey and SizeBytes are carried only by an S3 notification, which is
	// the first and only place the system learns how many bytes were actually
	// written. A presigned PUT signs neither Content-Length nor a size policy,
	// so the request-time size_bytes is the client's claim and this is the fact.
	// Zero means "this invocation did not come with a measurement".
	ObjectKey string
	SizeBytes int64
}

// parseInvocation resolves a payload to the captures it refers to and the
// correlation id it carries, if any. It accepts both shapes the worker is
// invoked with: the API's Invocation, and an S3 event notification.
func parseInvocation(raw json.RawMessage) ([]CaptureRef, string, error) {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return nil, "", fmt.Errorf("pipeline: empty invocation payload")
	}

	var probe struct {
		Invocation
		Records []json.RawMessage `json:"Records"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return nil, "", fmt.Errorf("pipeline: decode invocation: %w", err)
	}

	if probe.Task != "" {
		return nil, "", fmt.Errorf("pipeline: invocation is the task %q, not a capture", probe.Task)
	}
	if probe.TenantID != "" && probe.CaptureID != "" {
		return []CaptureRef{{TenantID: probe.TenantID, CaptureID: probe.CaptureID}}, probe.CorrelationID, nil
	}
	if len(probe.Records) == 0 {
		return nil, "", fmt.Errorf("pipeline: invocation addressed no capture")
	}

	var event events.S3Event
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return nil, "", fmt.Errorf("pipeline: decode s3 event: %w", err)
	}

	refs := make([]CaptureRef, 0, len(event.Records))
	for _, r := range event.Records {
		ref, ok := captureFromAudioKey(r.S3.Object.Key)
		if !ok {
			continue
		}
		ref.ObjectKey = r.S3.Object.Key
		ref.SizeBytes = r.S3.Object.Size
		refs = append(refs, ref)
	}
	return refs, "", nil
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
