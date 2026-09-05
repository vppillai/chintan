package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/service"
)

// cleanNoteTask is the payload the API and the worker itself send.
func cleanNoteTask(tenantID, noteID string, mode model.NoteCleanMode) json.RawMessage {
	raw, err := json.Marshal(Invocation{
		Task: TaskCleanNote, TenantID: tenantID, NoteID: noteID, Mode: string(mode),
		CorrelationID: "00000000-0000-4000-8000-000000000001",
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// seedNoteWithBody puts a note and its body in place, with the worker's
// markers ahead of two dictated paragraphs.
func seedNoteWithBody(t *testing.T, h *harness, noteID, body string, mutate func(*model.NoteIndex)) model.NoteIndex {
	t.Helper()
	ctx := context.Background()
	n := model.NoteIndex{
		ID: noteID, Title: "Roof", UpdatedAt: model.Now(),
		S3MarkdownKey: "tenants/user1/notes/" + noteID + "/note.md",
		S3MetaKey:     "tenants/user1/notes/" + noteID + "/meta.json",
	}
	if mutate != nil {
		mutate(&n)
	}
	stored, err := h.store.PutNote(ctx, "user1", n)
	if err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := h.objects.Put(ctx, n.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	return stored
}

func getNote(t *testing.T, h *harness, noteID string) model.NoteIndex {
	t.Helper()
	n, err := h.store.GetNote(context.Background(), "user1", noteID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	return n
}

var dictated = service.CaptureMarker("c_1") + "\nthe gutter leaks again\n\n" + service.CaptureMarker("c_2") + "\ncall the roofer on the fourteenth"

// The task end to end: the worker reads the body with the markers stripped,
// makes one reserved LLM call, and stores the view with its mode and time,
// stale false, error cleared.
func TestCleanNoteTaskStoresTheViewFromTheMarkerStrippedBody(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "# Roof\n\n- the gutter leaks again\n- call the roofer on the fourteenth"}
	counter := newMemCounter()
	h := newHarness(t, harnessOpts{llm: llmFake, counter: counter})
	seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) {
		n.CleanedStale = true
		n.CleanedError = "an earlier failure"
	})

	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	calls := llmFake.NoteCalls()
	if len(calls) != 1 {
		t.Fatalf("the model was called %d times, want exactly one whole-note call", len(calls))
	}
	if calls[0].Mode != model.NoteCleanStructured {
		t.Errorf("mode sent = %q", calls[0].Mode)
	}
	if strings.Contains(calls[0].Body, "<!-- chintan:capture:") {
		t.Errorf("the append markers reached the model: %q", calls[0].Body)
	}
	if calls[0].Body != "the gutter leaks again\n\ncall the roofer on the fourteenth" {
		t.Errorf("body sent = %q", calls[0].Body)
	}

	n := getNote(t, h, "n1")
	if n.CleanedBody != "# Roof\n\n- the gutter leaks again\n- call the roofer on the fourteenth" {
		t.Errorf("cleaned body = %q", n.CleanedBody)
	}
	if n.CleanedMode != model.NoteCleanStructured || n.CleanedAt == "" {
		t.Errorf("view metadata: mode=%q at=%q", n.CleanedMode, n.CleanedAt)
	}
	if n.CleanedStale {
		t.Error("a view just generated from the current body is stale")
	}
	if n.CleanedError != "" {
		t.Errorf("a successful run left the earlier error in place: %q", n.CleanedError)
	}
	if counter.total() <= 0 {
		t.Error("the call was not reserved against the day's spend counter; every provider call goes through the breaker")
	}
	// The body itself is untouched: the view is a second document, not an edit.
	raw, _ := h.objects.Get(context.Background(), n.S3MarkdownKey)
	if string(raw) != dictated {
		t.Errorf("the task rewrote the note body: %q", raw)
	}
}

// The spend cap refuses the call before the provider is contacted, and the row
// says so in words the user can read.
func TestCleanNoteTaskIsStoppedByTheSpendCapBeforeTheModelIsCalled(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake, capMicros: 1})
	seedNoteWithBody(t, h, "n1", strings.Repeat("words and more words. ", 200), nil)

	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanPolished)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(llmFake.NoteCalls()) != 0 {
		t.Fatal("the model was called past the cap; Do must own the call")
	}
	n := getNote(t, h, "n1")
	if n.CleanedError != cleanNoteSpendCapped {
		t.Errorf("cleaned error = %q, want %q", n.CleanedError, cleanNoteSpendCapped)
	}
	if n.CleanedBody != "" {
		t.Errorf("a capped run stored a body: %q", n.CleanedBody)
	}
	if n.CleanedMode != model.NoteCleanPolished || n.CleanedAt == "" {
		t.Errorf("a failed first run must still describe the attempt: mode=%q at=%q", n.CleanedMode, n.CleanedAt)
	}
}

// A body over the input cap is refused before the model sees it; a result
// over the output cap is refused whole rather than truncated. Both keep the
// previous view and record why.
func TestCleanNoteTaskEnforcesTheSizeCapsAndKeepsThePreviousView(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		llmFake := &fake.LLM{}
		h := newHarness(t, harnessOpts{llm: llmFake})
		seedNoteWithBody(t, h, "n1", strings.Repeat("x", model.MaxCleanNoteInputBytes+1), func(n *model.NoteIndex) {
			n.CleanedBody, n.CleanedMode, n.CleanedAt = "# Old view", model.NoteCleanStructured, "2026-01-01T00:00:00.000000000Z"
			n.CleanedStale = true
		})
		if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(llmFake.NoteCalls()) != 0 {
			t.Error("an over-cap body reached the model")
		}
		n := getNote(t, h, "n1")
		if n.CleanedError != cleanNoteTooLong {
			t.Errorf("cleaned error = %q", n.CleanedError)
		}
		if n.CleanedBody != "# Old view" || n.CleanedAt != "2026-01-01T00:00:00.000000000Z" || !n.CleanedStale {
			t.Errorf("the previous view was not kept as it was: %+v", n)
		}
	})
	t.Run("output", func(t *testing.T) {
		llmFake := &fake.LLM{NoteResponse: strings.Repeat("y", model.MaxCleanedBodyBytes+1)}
		h := newHarness(t, harnessOpts{llm: llmFake})
		seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) {
			n.CleanedBody, n.CleanedMode, n.CleanedAt = "# Old view", model.NoteCleanStructured, "2026-01-01T00:00:00.000000000Z"
		})
		if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		n := getNote(t, h, "n1")
		if n.CleanedError != cleanNoteOutputTooLong {
			t.Errorf("cleaned error = %q", n.CleanedError)
		}
		if n.CleanedBody != "# Old view" {
			t.Errorf("an over-cap result replaced or truncated the previous view: %d bytes", len(n.CleanedBody))
		}
	})
}

// Nothing usable — an empty answer, or the fence echoed back and nothing else —
// is a stored error beside the previous view, never an empty view.
func TestCleanNoteTaskRecordsAnUnusableAnswerAndKeepsThePreviousView(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: llm.FenceMarker + "\n\n" + llm.FenceMarker}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) {
		n.CleanedBody, n.CleanedMode, n.CleanedAt = "# Old view", model.NoteCleanPolished, "2026-01-01T00:00:00.000000000Z"
	})
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n := getNote(t, h, "n1")
	if n.CleanedError != cleanNoteUnusable {
		t.Errorf("cleaned error = %q", n.CleanedError)
	}
	if n.CleanedBody != "# Old view" || n.CleanedMode != model.NoteCleanPolished {
		t.Errorf("the previous view was not kept: %+v", n)
	}
}

// A provider failure is a verdict, not a retry: the same call would fail the
// same way, and the row tells the user to try again.
func TestCleanNoteTaskRecordsAProviderFailureWithoutRetrying(t *testing.T) {
	llmFake := &fake.LLM{NoteErr: &provider.StatusError{Op: "llm request failed", StatusCode: 503}}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedNoteWithBody(t, h, "n1", dictated, nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle returned %v; a provider failure is recorded, not retried", err)
	}
	if got := getNote(t, h, "n1").CleanedError; got != cleanNoteProviderFail {
		t.Errorf("cleaned error = %q", got)
	}
}

func TestCleanNoteTaskOnAnEmptyNoteRecordsThatThereIsNothingToClean(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedNoteWithBody(t, h, "n1", service.CaptureMarker("c_1")+"\n", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(llmFake.NoteCalls()) != 0 {
		t.Error("the model was called for a note with no text")
	}
	if got := getNote(t, h, "n1").CleanedError; got != cleanNoteEmpty {
		t.Errorf("cleaned error = %q", got)
	}
}

// A body write that lands while the model is working means the view is stale
// the moment it is stored — and the run must say so rather than copy the flag
// it read before the call.
func TestCleanNoteTaskMarksTheViewStaleWhenTheBodyMovedDuringTheCall(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	seeded := seedNoteWithBody(t, h, "n1", dictated, nil)
	var once sync.Once
	h.llm.OnCall = func() {
		once.Do(func() {
			// An editor save, mid-call.
			if err := h.objects.Put(context.Background(), seeded.S3MarkdownKey, []byte(dictated+"\n\nand one more thing"), "text/markdown"); err != nil {
				t.Errorf("Put: %v", err)
			}
		})
	}
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n := getNote(t, h, "n1")
	if n.CleanedBody == "" {
		t.Fatal("no view was stored")
	}
	if !n.CleanedStale {
		t.Error("the body changed during the call but the stored view claims to be current")
	}
}

// A note that is gone, or archived, is nothing to do — and nothing to retry.
func TestCleanNoteTaskOnAMissingOrArchivedNoteIsDone(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "gone", model.NoteCleanStructured)); err != nil {
		t.Errorf("Handle(missing) = %v, want nil", err)
	}
	seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) { n.DeletedAt = model.Now() })
	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Errorf("Handle(archived) = %v, want nil", err)
	}
	if len(llmFake.NoteCalls()) != 0 {
		t.Error("the model was called for a note that cannot be cleaned")
	}
	// A task that names no note is discarded like any other unparseable
	// payload rather than retried.
	if err := NewWorker(h.pipeline).Handle(context.Background(), json.RawMessage(`{"task":"clean-note","tenant_id":"user1"}`)); err != nil {
		t.Errorf("Handle(no note) = %v, want nil", err)
	}
}

// recordingCleanInvoker captures every clean-note hand-off the pipeline makes.
type recordingCleanInvoker struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *recordingCleanInvoker) InvokeCleanNote(_ context.Context, tenantID, noteID string, mode model.NoteCleanMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.calls = append(r.calls, tenantID+"/"+noteID+"/"+string(mode))
	return nil
}

func (r *recordingCleanInvoker) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// After a successful append to a note with auto_clean the pipeline hands the
// note back to the worker, in the note's own mode, once — and never for a note
// that did not ask.
func TestAppendToAnAutoCleanNoteInvokesTheCleanNoteTask(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoClean bool
		mode      model.NoteCleanMode
		want      []string
	}{
		{"auto_clean on, polished", true, model.NoteCleanPolished, []string{"user1/note1/polished"}},
		{"auto_clean on, default mode", true, "", []string{"user1/note1/structured"}},
		{"auto_clean off", false, model.NoteCleanPolished, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoker := &recordingCleanInvoker{}
			h := newHarness(t, harnessOpts{stt: &fake.STT{Response: "the gutter leaks"}})
			h.pipeline.cfg.CleanInvoker = invoker
			seedUploadedCapture(t, h, "note1")
			note := getNote(t, h, "note1")
			note.AutoClean = tc.autoClean
			note.CleanMode = tc.mode
			note.CleanedBody, note.CleanedAt = "# Old", model.Now()
			if _, err := h.store.PutNote(context.Background(), "user1", note); err != nil {
				t.Fatalf("PutNote: %v", err)
			}

			if err := NewWorker(h.pipeline).Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			capture, err := h.store.GetCapture(context.Background(), "user1", "c_1")
			if err != nil || capture.Status != model.StatusAppended {
				t.Fatalf("capture = %+v, %v; want appended", capture, err)
			}
			if got := invoker.got(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("clean-note hand-offs = %v, want %v", got, tc.want)
			}
			// Whatever the preference, the append made the existing view stale.
			if !getNote(t, h, "note1").CleanedStale {
				t.Error("the append did not mark the existing view stale")
			}
			if len(h.llm.NoteCalls()) != 0 {
				t.Error("the append ran the whole-note model inline although an invoker is configured")
			}
		})
	}
}

// Without an invoker the worker regenerates the view itself: it is already off
// the request path. A hand-off that fails never fails the capture.
func TestAppendToAnAutoCleanNoteRunsTheCleanInlineWithoutAnInvokerAndSurvivesAFailedHandOff(t *testing.T) {
	t.Run("inline", func(t *testing.T) {
		h := newHarness(t, harnessOpts{stt: &fake.STT{Response: "the gutter leaks"}, llm: &fake.LLM{Response: "The gutter leaks.", NoteResponse: "# Roof\n\nThe gutter leaks."}})
		seedUploadedCapture(t, h, "note1")
		note := getNote(t, h, "note1")
		note.AutoClean = true
		if _, err := h.store.PutNote(context.Background(), "user1", note); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		if err := NewWorker(h.pipeline).Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		n := getNote(t, h, "note1")
		if n.CleanedBody != "# Roof\n\nThe gutter leaks." || n.CleanedStale {
			t.Errorf("inline auto-clean did not store a current view: body=%q stale=%v", n.CleanedBody, n.CleanedStale)
		}
	})
	t.Run("failed hand-off", func(t *testing.T) {
		h := newHarness(t, harnessOpts{stt: &fake.STT{Response: "the gutter leaks"}})
		h.pipeline.cfg.CleanInvoker = &recordingCleanInvoker{err: errors.New("lambda: 429 TooManyRequestsException")}
		seedUploadedCapture(t, h, "note1")
		note := getNote(t, h, "note1")
		note.AutoClean = true
		if _, err := h.store.PutNote(context.Background(), "user1", note); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		if err := NewWorker(h.pipeline).Handle(context.Background(), s3Event("tenants/user1/captures/c_1/audio.webm")); err != nil {
			t.Fatalf("Handle: %v; a failed clean-note hand-off must not fail the capture", err)
		}
		if capture, _ := h.store.GetCapture(context.Background(), "user1", "c_1"); capture.Status != model.StatusAppended {
			t.Errorf("capture status = %s, want appended", capture.Status)
		}
	})
}

// The invoker's clean-note payload is the one the worker dispatches on, and it
// carries the request's correlation id so the two log trails join.
func TestInvokerSendsACleanNoteTaskTheWorkerAccepts(t *testing.T) {
	client := &capturingLambda{}
	inv := NewInvoker(client, "arn:aws:lambda:us-west-2:123456789012:function:chintan-worker-dev-prod:live")
	ctx := context.Background()
	if err := inv.InvokeCleanNote(ctx, "user1", "n1", model.NoteCleanPolished); err != nil {
		t.Fatalf("InvokeCleanNote: %v", err)
	}
	if client.in == nil {
		t.Fatal("Invoke was not called")
	}
	var sent map[string]any
	if err := json.Unmarshal(client.in.Payload, &sent); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for k, want := range map[string]string{"task": TaskCleanNote, "tenant_id": "user1", "note_id": "n1", "mode": "polished"} {
		if sent[k] != want {
			t.Errorf("payload[%s] = %v, want %q", k, sent[k], want)
		}
	}
	if _, ok := sent["capture_id"]; ok {
		t.Error("a clean-note payload names a capture")
	}
	task, ok := parseCleanNoteTask(client.in.Payload)
	if !ok || task.NoteID != "n1" || task.TenantID != "user1" || task.Mode != "polished" {
		t.Errorf("the worker does not read back what the invoker sent: %+v ok=%v", task, ok)
	}
	if _, isCapture := parseCleanNoteTask(json.RawMessage(`{"tenant_id":"u","capture_id":"c"}`)); isCapture {
		t.Error("a capture invocation was read as a clean-note task")
	}
}

// A run whose mode is no longer the one stamped on the row was superseded by a
// later request; it neither calls the model nor writes. Before the stamp, two
// runs in different modes both wrote and the later to finish decided the
// stored mode — the live evidence was a polished view stored after structured
// had been asked for last.
func TestCleanNoteTaskLeavesTheNoteToALaterRequestInAnotherMode(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "# Roof"}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) {
		n.CleanedBody, n.CleanedMode, n.CleanedAt = "# Old", model.NoteCleanStructured, model.Now()
		n.CleanedRequestedAt, n.CleanedRequestedMode = model.Now(), model.NoteCleanPolished
	})

	if err := NewWorker(h.pipeline).Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v; a superseded run is a verdict, not a retry", err)
	}
	if got := len(llmFake.NoteCalls()); got != 0 {
		t.Fatalf("the superseded run called the model %d time(s); that bills a view nobody asked for", got)
	}
	n := getNote(t, h, "n1")
	if n.CleanedBody != "# Old" || n.CleanedMode != model.NoteCleanStructured {
		t.Errorf("the superseded run wrote: body=%q mode=%q", n.CleanedBody, n.CleanedMode)
	}
	if n.CleanedRequestedMode != model.NoteCleanPolished {
		t.Errorf("the superseded run cleared the polished request's stamp: %q", n.CleanedRequestedMode)
	}
}

// A request that lands while the model works supersedes the run in flight —
// whatever its mode — and the older run's view is not written over the one
// the newer run will store. A run that does write clears its stamp, so the
// next request invokes again.
func TestCleanNoteTaskYieldsToARequestThatLandedDuringTheCallAndClearsItsOwnStamp(t *testing.T) {
	llmFake := &fake.LLM{NoteResponse: "# From the older body"}
	h := newHarness(t, harnessOpts{llm: llmFake})
	first := model.FormatTime(h.clock.Now())
	seedNoteWithBody(t, h, "n1", dictated, func(n *model.NoteIndex) {
		n.CleanedRequestedAt, n.CleanedRequestedMode = first, model.NoteCleanStructured
	})
	ctx := context.Background()

	// While the model works, an append asks for a fresh run in the same mode.
	llmFake.OnCall = func() {
		later := h.clock.Now().Add(5 * time.Second)
		if _, _, err := service.RecordCleanRequest(ctx, h.store, "user1", getNote(t, h, "n1"), model.NoteCleanStructured, later, false); err != nil {
			t.Errorf("stamp the later request: %v", err)
		}
	}
	if err := NewWorker(h.pipeline).Handle(ctx, cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	n := getNote(t, h, "n1")
	if n.CleanedBody != "" {
		t.Errorf("the older run wrote its view %q over a newer request", n.CleanedBody)
	}
	if n.CleanedRequestedAt == first || n.CleanedRequestedAt == "" {
		t.Errorf("stamp after the yield = %q; want the later request's, untouched", n.CleanedRequestedAt)
	}

	// The later run, invoked for that stamp, writes and clears it.
	llmFake.OnCall = nil
	llmFake.NoteResponse = "# From the current body"
	if err := NewWorker(h.pipeline).Handle(ctx, cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("Handle (later run): %v", err)
	}
	n = getNote(t, h, "n1")
	if n.CleanedBody != "# From the current body" {
		t.Errorf("cleaned body = %q, want the later run's view", n.CleanedBody)
	}
	if n.CleanedRequestedAt != "" || n.CleanedRequestedMode != "" {
		t.Errorf("a finished run left its stamp on the row (%q %q); the next request would be answered as queued against nothing", n.CleanedRequestedAt, n.CleanedRequestedMode)
	}
}
