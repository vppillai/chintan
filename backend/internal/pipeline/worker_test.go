package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// s3Event builds the notification the content bucket invokes the worker with
// when a recording lands, which is the only thing that starts a capture in
// production.
func s3Event(key string) json.RawMessage {
	body, err := json.Marshal(events.S3Event{
		Records: []events.S3EventRecord{{
			EventName: "ObjectCreated:Put",
			S3: events.S3Entity{
				Bucket: events.S3Bucket{Name: "chintan-content-test"},
				Object: events.S3Object{Key: key},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// seedUploadedCapture puts a tenant, a note, an audio object and a capture row
// in place, exactly as POST /v1/captures plus the client's PUT would.
func seedUploadedCapture(t *testing.T, h *harness, noteID string) model.CaptureIndex {
	t.Helper()
	ctx := context.Background()

	if noteID != "" {
		if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
			ID:            noteID,
			Title:         "Destination",
			UpdatedAt:     model.Now(),
			S3MarkdownKey: "tenants/user1/notes/" + noteID + "/note.md",
		}); err != nil {
			t.Fatalf("seed note: %v", err)
		}
		if err := h.objects.Put(ctx, "tenants/user1/notes/"+noteID+"/note.md", []byte(""), "text/markdown"); err != nil {
			t.Fatalf("seed note body: %v", err)
		}
	}
	if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio bytes"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	capture, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: noteID, Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm",
		DurationMS: 12_000, CreatedAt: model.Now(),
	})
	if err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	return capture
}

// The whole pipeline, driven the way production drives it: an S3 ObjectCreated
// notification invoking the worker and nothing else.
func TestWorkerDrivesTheWholePipelineFromAnS3Event(t *testing.T) {
	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{
			Text:     "the gutter is leaking again",
			Language: "en",
			Duration: 12.5,
			Segments: []provider.Segment{
				{Start: 0, End: 6.25, Text: "the gutter is"},
				{Start: 6.25, End: 12.5, Text: " leaking again"},
			},
			Words: []provider.Word{{Start: 0, End: 0.4, Word: "the"}},
		}},
		llm: &fake.LLM{Response: "The gutter is leaking again."},
	})
	seedUploadedCapture(t, h, "note1")

	worker := NewWorker(h.pipeline)
	if err := worker.Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ctx := context.Background()
	capture, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended (error=%q)", capture.Status, capture.Error)
	}

	body, err := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
	if err != nil {
		t.Fatalf("read note body: %v", err)
	}
	if !strings.Contains(string(body), "The gutter is leaking again.") {
		t.Fatalf("note body missing the cleaned text: %q", body)
	}

	// Timestamps: segments.json is what makes tap-to-seek possible, and
	// duration_ms is what the player and the spend estimate both read.
	if capture.DurationMS != 12_500 {
		t.Errorf("duration_ms = %d, want 12500", capture.DurationMS)
	}
	if capture.SegmentsKey != "tenants/user1/captures/c_1/segments.json" {
		t.Fatalf("segments_key = %q", capture.SegmentsKey)
	}
	raw, err := h.objects.Get(ctx, capture.SegmentsKey)
	if err != nil {
		t.Fatalf("read segments.json: %v", err)
	}
	var doc transcriptDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode segments.json: %v", err)
	}
	if doc.DurationMS != 12_500 || len(doc.Segments) != 2 || len(doc.Words) != 1 {
		t.Fatalf("segments.json = %+v", doc)
	}
	if doc.Segments[1].StartMS != 6_250 {
		t.Errorf("second segment starts at %dms, want 6250", doc.Segments[1].StartMS)
	}

	// The recording was handed over as a presigned URL, not as bytes.
	if n := len(h.stt.Sources); n != 1 {
		t.Fatalf("stt called %d times, want 1", n)
	}
	if h.stt.Sources[0].URL == "" {
		t.Error("the speech provider was not given a presigned URL")
	}
	if h.stt.Sources[0].Body != nil {
		t.Error("the recording was buffered into the worker instead of streamed")
	}
}

// B2. The pipeline outlasts API Gateway's fixed 30-second integration ceiling,
// because nothing on the request path is waiting for it.
//
// The clock is advanced rather than slept through: the assertion is about the
// elapsed time the pipeline is willing to consume, not about how long the test
// takes.
func TestACaptureLongerThanTheGatewayCeilingStillCompletes(t *testing.T) {
	clock := newTestClock()
	stt := &fake.STT{Response: "a long drive worth of thinking", Duration: 1_200}
	llm := &fake.LLM{Response: "A long drive worth of thinking."}
	router := &fake.Router{}

	// Each provider call burns fifteen seconds of the pipeline's clock. Three of
	// them is comfortably past the ceiling that returns 504 on the request path.
	stt.OnCall = func() { clock.Advance(45 * time.Second) }
	router.OnCall = func() { clock.Advance(20 * time.Second) }
	llm.OnCall = func() { clock.Advance(25 * time.Second) }

	h := newHarness(t, harnessOpts{stt: stt, llm: llm, router: router})
	h.clock = clock
	cfg := Config{
		Store: h.store, Objects: h.objects, STT: stt, LLM: llm, Router: router,
		Notes: h.creator, Breaker: newBreaker(0),
		STTProvider: "groq", STTModel: "whisper-large-v3-turbo",
		LLMProvider: "openai", LLMModel: "test-model",
		Now: clock.Now,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.pipeline = p
	seedUploadedCapture(t, h, "")

	// The request path: creating the capture must touch no provider at all.
	svc := service.NewCaptureService(h.store, h.objects).
		WithInvoker(recordingInvoker{})
	if _, err := svc.BeginCapture(context.Background(), "user1", service.CaptureRequest{ContentType: "audio/webm"}); err != nil {
		t.Fatalf("BeginCapture: %v", err)
	}
	if n := stt.Calls() + llm.Calls() + router.CallCount(); n != 0 {
		t.Fatalf("POST /v1/captures made %d provider calls; it must make none", n)
	}

	before := clock.Now()
	worker := NewWorker(p)
	if err := worker.Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if elapsed := clock.Now().Sub(before); elapsed <= 30*time.Second {
		t.Fatalf("the pipeline consumed %s; the test no longer exercises anything past the 30s gateway ceiling", elapsed)
	}

	capture, err := h.store.GetCapture(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended (error=%q)", capture.Status, capture.Error)
	}
}

// recordingInvoker accepts hand-offs and does nothing, standing in for the
// asynchronous Lambda invocation on the request path.
type recordingInvoker struct{}

func (recordingInvoker) InvokeCapture(context.Context, string, string, string) error { return nil }

// A cap rejection is a budget decision, not a fault: it gets its own status and
// the provider is never contacted.
func TestSpendCapStopsTheCaptureBeforeTheProviderIsCalled(t *testing.T) {
	stt := &fake.STT{Response: "words"}
	// One microdollar of headroom against a five-minute default estimate priced
	// at two micros a second.
	h := newHarness(t, harnessOpts{stt: stt, capMicros: 1})
	seedUploadedCapture(t, h, "note1")

	worker := NewWorker(h.pipeline)
	if err := worker.Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	capture, err := h.store.GetCapture(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != service.StatusSpendCapped {
		t.Fatalf("status = %s, want spend_capped", capture.Status)
	}
	if capture.Error == "" {
		t.Error("a spend-capped capture must carry an explanation the UI can show")
	}
	if n := stt.Calls(); n != 0 {
		t.Fatalf("the speech provider was called %d times past the cap; Do must own the call", n)
	}
	if capture.RawKey != "" {
		t.Error("a capped capture must not claim a transcript it never obtained")
	}
}

// A spend-capped capture is not a dead end: raising the cap and retrying picks
// it up from the top, since nothing was produced.
func TestRetryResumesFromTheLastGoodStageWithoutRetranscribing(t *testing.T) {
	ctx := context.Background()

	// A capture that already has its cleaned text and failed at the append. The
	// speech provider must never be touched again — re-transcribing twenty
	// minutes of audio to redo one S3 write is exactly the waste resumability
	// exists to prevent.
	stt := &fake.STT{ShouldFail: true}
	llm := &fake.LLM{ShouldFail: true}
	h := newHarness(t, harnessOpts{stt: stt, llm: llm})

	if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "note1", Title: "Destination", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := h.objects.Put(ctx, "tenants/user1/notes/note1/note.md", []byte("earlier line"), "text/markdown"); err != nil {
		t.Fatalf("seed note body: %v", err)
	}
	if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/raw.txt", []byte("raw words"), "text/plain"); err != nil {
		t.Fatalf("seed raw: %v", err)
	}
	if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/clean.txt", []byte("Clean words."), "text/plain"); err != nil {
		t.Fatalf("seed clean: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "note1", Status: model.StatusFailed,
		Error:     "induced append failure",
		Mode:      model.CleanupFaithful,
		AudioKey:  "tenants/user1/captures/c_1/audio.webm",
		RawKey:    "tenants/user1/captures/c_1/raw.txt",
		CleanKey:  "tenants/user1/captures/c_1/clean.txt",
		CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	// The API's retry: reset to the last good stage and hand the capture to the
	// worker. It runs no pipeline of its own.
	svc := service.NewCaptureService(h.store, h.objects).WithInvoker(recordingInvoker{})
	if _, err := svc.RetryCapture(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("RetryCapture: %v", err)
	}
	reset, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if reset.Status != model.StatusCleaned {
		t.Fatalf("retry reset the capture to %s, want cleaned", reset.Status)
	}

	// The worker is then invoked with the API's payload rather than an S3 event.
	worker := NewWorker(h.pipeline)
	if err := worker.Handle(ctx, json.RawMessage(`{"tenant_id":"user1","capture_id":"c_1","reason":"retry"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	capture, err := h.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended (error=%q)", capture.Status, capture.Error)
	}
	if n := stt.Calls(); n != 0 {
		t.Fatalf("the speech provider was called %d times on resume, want 0", n)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the cleanup model was called %d times on resume, want 0", n)
	}

	body, err := h.objects.Get(ctx, "tenants/user1/notes/note1/note.md")
	if err != nil {
		t.Fatalf("read note body: %v", err)
	}
	if got := strings.Count(string(body), "Clean words."); got != 1 {
		t.Fatalf("cleaned text appears %d times, want 1:\n%s", got, body)
	}
}

// An infrastructure fault fails the invocation, which is what makes Lambda
// retry it; a capture that failed on its own terms does not, because three
// identical failures only fill the dead-letter queue.
func TestInfrastructureFailureFailsTheInvocation(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// No capture row at all: GetCapture fails, which is a store fault.
	worker := NewWorker(h.pipeline)

	if err := worker.Handle(context.Background(), s3Event("tenants/user1/captures/c_missing/audio.webm")); err == nil {
		t.Fatal("Handle returned nil for a store fault; Lambda would treat the capture as done and never retry it")
	}
}

func TestProviderFailureIsNotRetried(t *testing.T) {
	h := newHarness(t, harnessOpts{stt: &fake.STT{ShouldFail: true}})
	seedUploadedCapture(t, h, "note1")

	worker := NewWorker(h.pipeline)
	if err := worker.Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
		t.Fatalf("Handle: %v; a provider failure is a verdict, not a fault to retry", err)
	}

	capture, err := h.store.GetCapture(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", capture.Status)
	}
}

// A payload that addresses no capture is dropped, not failed: retrying it
// twice and dead-lettering it would raise the "a recording was lost" alarm for
// something that was never a recording.
func TestWorkerIgnoresPayloadsItDoesNotOwn(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	worker := NewWorker(h.pipeline)

	for name, body := range map[string]json.RawMessage{
		"the worker's output": s3Event("tenants/user1/captures/c_1/raw.txt"),
		"a foreign key":       s3Event("something/else/entirely"),
		"half an invocation":  json.RawMessage(`{"tenant_id":"user1"}`),
		"garbage":             json.RawMessage(`not json at all`),
		"nothing":             json.RawMessage(``),
	} {
		t.Run(name, func(t *testing.T) {
			if err := worker.Handle(context.Background(), body); err != nil {
				t.Fatalf("Handle: %v, want nil", err)
			}
		})
	}
}

func TestCaptureFromAudioKey(t *testing.T) {
	cases := map[string]struct {
		key  string
		want CaptureRef
		ok   bool
	}{
		"webm":      {"tenants/u1/captures/c_1/audio.webm", CaptureRef{TenantID: "u1", CaptureID: "c_1"}, true},
		"m4a":       {"tenants/u1/captures/c_1/audio.m4a", CaptureRef{TenantID: "u1", CaptureID: "c_1"}, true},
		"encoded":   {"tenants%2Fu1%2Fcaptures%2Fc_1%2Faudio.wav", CaptureRef{TenantID: "u1", CaptureID: "c_1"}, true},
		"raw text":  {"tenants/u1/captures/c_1/raw.txt", CaptureRef{}, false},
		"peaks":     {"tenants/u1/captures/c_1/peaks.json", CaptureRef{}, false},
		"note body": {"tenants/u1/notes/n1/note.md", CaptureRef{}, false},
		"traversal": {"tenants/../captures/c_1/audio.webm", CaptureRef{}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := captureFromAudioKey(tc.key)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("captureFromAudioKey(%q) = %+v, %v; want %+v, %v", tc.key, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// One capture is one greppable trace, which means the id the API minted has to
// survive the hand-off to the worker.
func TestCorrelationIDIsTakenFromTheInvocation(t *testing.T) {
	ref := CaptureRef{TenantID: "user1", CaptureID: "c_1"}
	if got := correlationFor("trace-abc-123", ref); got != "trace-abc-123" {
		t.Fatalf("correlation id = %q, want trace-abc-123", got)
	}

	// A hostile value must not reach the log stream verbatim.
	if got := correlationFor("bad\nid", ref); got == "bad\nid" || got == "" {
		t.Fatalf("correlation id = %q, want a safe id", got)
	}

	// An S3 notification carries no id, so the capture's own id traces it —
	// and traces the two automatic retries of the same attempt with it.
	if got := correlationFor("", ref); got != "capture-c_1" {
		t.Fatalf("correlation id = %q, want capture-c_1", got)
	}
}

// capturingLambda records the one Invoke the API makes.
type capturingLambda struct{ in *lambda.InvokeInput }

func (c *capturingLambda) Invoke(_ context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	c.in = in
	return &lambda.InvokeOutput{StatusCode: 202}, nil
}

// The API's retry and the worker meet only through this payload, so the test
// closes the loop: what Invoker sends is what parseInvocation reads, and the
// correlation id the request carried comes out the other side.
func TestInvokerSendsWhatTheWorkerAccepts(t *testing.T) {
	client := &capturingLambda{}
	inv := NewInvoker(client, "arn:aws:lambda:us-west-2:123456789012:function:chintan-worker-dev-prod:live")

	ctx := obs.WithCorrelationID(context.Background(), "trace-abc-123")
	if err := inv.InvokeCapture(ctx, "user1", "c_1", "retry"); err != nil {
		t.Fatalf("InvokeCapture: %v", err)
	}
	if client.in == nil {
		t.Fatal("Invoke was not called")
	}
	if got := aws.ToString(client.in.FunctionName); !strings.HasSuffix(got, ":live") {
		t.Fatalf("invoked %q, want the worker's live alias", got)
	}
	if client.in.InvocationType != types.InvocationTypeEvent {
		t.Fatalf("invocation type = %q, want Event: the request path must not wait for the pipeline", client.in.InvocationType)
	}

	refs, correlationID, err := parseInvocation(client.in.Payload)
	if err != nil {
		t.Fatalf("the worker cannot parse the API's payload: %v", err)
	}
	if len(refs) != 1 || refs[0] != (CaptureRef{TenantID: "user1", CaptureID: "c_1"}) {
		t.Fatalf("refs = %+v, want the one capture the API named", refs)
	}
	if correlationID != "trace-abc-123" {
		t.Fatalf("correlation id = %q, want the request's", correlationID)
	}
}

// A 202 is the only answer that means "queued". Anything else is not accepted
// and the request must not report success over it.
func TestInvokerRejectsANonAcceptedStatus(t *testing.T) {
	inv := NewInvoker(statusLambda(200), "arn:aws:lambda:us-west-2:123456789012:function:f:live")
	if err := inv.InvokeCapture(context.Background(), "user1", "c_1", "retry"); err == nil {
		t.Fatal("a 200 from an Event invocation was reported as success")
	}
}

type statusLambda int32

func (s statusLambda) Invoke(context.Context, *lambda.InvokeInput, ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	return &lambda.InvokeOutput{StatusCode: int32(s)}, nil
}

// The pipeline refuses to exist without a breaker, so there is no build in which
// a paid API is reachable unmetered.
func TestPipelineRefusesToStartWithoutABreaker(t *testing.T) {
	_, err := New(Config{
		Store:   memory.NewStore(),
		Objects: memory.NewObjects(),
		STT:     &fake.STT{},
		LLM:     &fake.LLM{},
	})
	if err == nil {
		t.Fatal("a pipeline was built with no breaker; every provider call must pass the spend check")
	}
}
