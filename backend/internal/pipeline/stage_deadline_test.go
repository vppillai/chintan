package pipeline

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// runCapturingMetrics runs one capture and returns what it recorded.
func runCapturingMetrics(t *testing.T, h *harness, id string) (model.CaptureIndex, []emfRecord, error) {
	t.Helper()
	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	final, err := h.pipeline.Run(context.Background(), "user1", id)
	restore()
	return final, decodeMetrics(t, metrics.Bytes()), err
}

// A transcription that outlives its deadline is an infrastructure fault, not a
// verdict: the capture stays at "transcribing" with no transcript, the
// invocation fails so Lambda retries it, and the retry transcribes it. Before
// the stage deadlines the only bound was the HTTP client's 840 s, and when it
// fired the capture was marked failed for good.
func TestATranscriptionTimeoutLeavesTheCaptureRetryable(t *testing.T) {
	stt := &fake.STT{HangCalls: 1, Response: "the gutter is leaking again"}
	h := newHarness(t, harnessOpts{stt: stt, stageTimeout: 20 * time.Millisecond})
	capture := seedUploadedCapture(t, h, "n_stall")

	final, records, err := runCapturingMetrics(t, h, capture.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first run returned %v, want an error wrapping context.DeadlineExceeded so Lambda retries", err)
	}
	if final.Status != model.StatusTranscribing || final.RawKey != "" {
		t.Fatalf("after the stall the capture is %s with raw key %q; want transcribing with no transcript",
			final.Status, final.RawKey)
	}
	stored, getErr := h.store.GetCapture(context.Background(), "user1", capture.ID)
	if getErr != nil {
		t.Fatalf("GetCapture: %v", getErr)
	}
	if stored.Status == model.StatusFailed || stored.Error != "" {
		t.Fatalf("the stall was recorded as a verdict: status %s, error %q", stored.Status, stored.Error)
	}
	rec := findMetric(t, records, "ProviderTimedOut")
	if got := rec.Values["Stage"]; got != "transcribe" {
		t.Errorf("ProviderTimedOut Stage = %v, want transcribe", got)
	}

	// The retry.
	final, _, err = runCapturingMetrics(t, h, capture.ID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("second run left the capture %s, want appended", final.Status)
	}
	if n := stt.Calls(); n != 2 {
		t.Fatalf("STT was called %d times, want 2 (one stall, one transcription)", n)
	}
}

// The retry resumes where the artefacts stop. A cleanup that stalls after the
// transcript is in S3 must not have the recording transcribed — and billed —
// again: the second run goes straight to cleanup.
func TestACleanupTimeoutDoesNotRedoTheTranscription(t *testing.T) {
	// Long enough to be cleaned by the model rather than tidied.
	stt := &fake.STT{Response: "call the roofer on the fourteenth about the gutter over the back door"}
	llm := &fake.LLM{HangCalls: 1}
	h := newHarness(t, harnessOpts{stt: stt, llm: llm, stageTimeout: 20 * time.Millisecond})
	capture := seedUploadedCapture(t, h, "n_clean_stall")

	final, records, err := runCapturingMetrics(t, h, capture.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first run returned %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if final.Status != model.StatusCleaning || final.RawKey == "" || final.CleanKey != "" {
		t.Fatalf("after the stall the capture is %s (raw %q, clean %q); want cleaning with the transcript kept",
			final.Status, final.RawKey, final.CleanKey)
	}
	if got := findMetric(t, records, "ProviderTimedOut").Values["Stage"]; got != "cleanup" {
		t.Errorf("ProviderTimedOut Stage = %v, want cleanup", got)
	}

	final, _, err = runCapturingMetrics(t, h, capture.ID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("second run left the capture %s, want appended", final.Status)
	}
	if n := stt.Calls(); n != 1 {
		t.Fatalf("STT was called %d times across both runs, want 1: the transcript was already stored", n)
	}
	if n := llm.Calls(); n != 2 {
		t.Fatalf("cleanup was called %d times, want 2 (one stall, one answer)", n)
	}
}

// The whole-note clean follows the same rule: a stalled model is not a verdict
// to write on the row, so the previous view (here, none) and the error field
// are left alone and the task fails for its retry.
func TestACleanNoteTimeoutLeavesTheTaskRetryable(t *testing.T) {
	llm := &fake.LLM{NoteHang: 1, NoteResponse: "# Roof\n\n- the gutter leaks again"}
	h := newHarness(t, harnessOpts{llm: llm, stageTimeout: 20 * time.Millisecond})
	seedNoteWithBody(t, h, "n1", dictated, nil)
	worker := NewWorker(h.pipeline)

	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	err := worker.Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured))
	restore()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first run returned %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if n := getNote(t, h, "n1"); n.CleanedError != "" || n.CleanedBody != "" {
		t.Fatalf("the stall was written to the row: error %q, body %q", n.CleanedError, n.CleanedBody)
	}
	if got := findMetric(t, decodeMetrics(t, metrics.Bytes()), "ProviderTimedOut").Values["Stage"]; got != "clean_note" {
		t.Errorf("ProviderTimedOut Stage = %v, want clean_note", got)
	}

	if err := worker.Handle(context.Background(), cleanNoteTask("user1", "n1", model.NoteCleanStructured)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := getNote(t, h, "n1"); n.CleanedBody == "" || n.CleanedError != "" {
		t.Fatalf("the retry did not store the view: body %q, error %q", n.CleanedBody, n.CleanedError)
	}
	if n := len(llm.NoteCalls()); n != 2 {
		t.Fatalf("clean-note was called %d times, want 2", n)
	}
}

// The defaults are the contract with the Lambda budget: every stage bounded,
// and the longest one still leaving most of the 900 s for what follows.
func TestStageDeadlinesDefaultWhenUnset(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	cfg := h.pipeline.cfg
	for name, got := range map[string]time.Duration{
		"transcribe": cfg.TranscribeTimeout,
		"cleanup":    cfg.CleanupTimeout,
		"clean-note": cfg.CleanNoteTimeout,
	} {
		if got <= 0 || got > 5*time.Minute {
			t.Errorf("%s deadline defaulted to %s; want a bound within five minutes", name, got)
		}
	}
}

// The worst-case wall time of each stage is its deadline table, and the
// retry inside the provider clients (provider.retrying) does not add to it:
// a retried call runs under the same context as the first, so the clients'
// longest possible jittered wait has to fit inside the shortest attempt
// with room for the calls themselves, and the stage retries (routing's two
// attempts, ask's two) compose with it by wall time, not by multiplying
// attempts. docs/design/pipeline-deadlines.md carries the same table.
func TestStageWorstCaseWallTimeIsTheDeadlineTable(t *testing.T) {
	// Every jittered wait is under min(MaxWait, BaseWait<<attempt); the sum
	// over the retries is the most a call can spend not calling.
	var clientWaits time.Duration
	for attempt := 1; attempt < provider.ProviderRetryAttempts; attempt++ {
		clientWaits += min(provider.ProviderRetryMaxWait, provider.ProviderRetryBaseWait<<attempt)
	}
	routeWall := routeAttempts * defaultRouteAttemptTimeout
	askRetry := time.Duration(float64(defaultAskAttemptTimeout) * askRetryShare)
	askWall := defaultAskAttemptTimeout + askRetry
	for name, tc := range map[string]struct{ got, want time.Duration }{
		"transcribe":            {defaultTranscribeTimeout, 5 * time.Minute},
		"route (two attempts)":  {routeWall, 30 * time.Second},
		"cleanup":               {defaultCleanupTimeout, 2 * time.Minute},
		"clean-note":            {defaultCleanNoteTimeout, 3 * time.Minute},
		"ask (attempt + retry)": {askWall, 35 * time.Second},
		"client waits in all":   {clientWaits, 6 * time.Second},
	} {
		if tc.got != tc.want {
			t.Errorf("%s worst case is %s; the table in pipeline-deadlines.md says %s — change both or neither", name, tc.got, tc.want)
		}
	}
	// The shortest windows a retried call runs under: a routing attempt and
	// ask's retry. Half of each is left for the calls themselves.
	for name, window := range map[string]time.Duration{"a routing attempt": defaultRouteAttemptTimeout, "ask's retry": askRetry} {
		if clientWaits > window/2 {
			t.Errorf("the client's waits (%s) take more than half of %s (%s)", clientWaits, name, window)
		}
	}
	// One invocation's worst case — two transcriptions, routing, cleanup —
	// inside the Lambda's 900 s with two minutes to spare for the append and
	// the retry protocol.
	if worst := 2*defaultTranscribeTimeout + routeWall + defaultCleanupTimeout; worst > 13*time.Minute {
		t.Errorf("one invocation's worst case is %s, over the 13 min the 900 s budget allows it", worst)
	}
}

// A 529 burst that outlasts the client's retries at the cleanup stage is a
// provider verdict, not an infrastructure fault: the capture ends failed with
// the fixed sentence and a Retry button, nil goes back to Lambda (no
// asynchronous retry), and the transcript stays at RawKey. The person's Retry
// resumes at cleanup — the API moves the row back to the stage after its
// last artefact (service.resumeStatusFor) and the run skips transcription —
// so the recording is transcribed and billed once, and the refused cleanup
// attempts, which report no usage, cost nothing: the day's spend equals a
// run the burst never touched.
func TestA529BurstAtCleanupDoesNotRedoTheTranscription(t *testing.T) {
	const dictation = "call the roofer on the fourteenth about the gutter over the back door"
	controlCounter := newMemCounter()
	control := newHarness(t, harnessOpts{stt: &fake.STT{Response: dictation}, llm: &fake.LLM{}, counter: controlCounter})
	if final, err := control.pipeline.Run(context.Background(), "user1", seedUploadedCapture(t, control, "n_ctrl").ID); err != nil || final.Status != model.StatusAppended {
		t.Fatalf("control run: %s, %v", final.Status, err)
	}

	stt := &fake.STT{Response: dictation}
	llm := &fake.LLM{Err: &provider.StatusError{Op: "llm request failed", StatusCode: 529}}
	counter := newMemCounter()
	h := newHarness(t, harnessOpts{stt: stt, llm: llm, counter: counter})
	capture := seedUploadedCapture(t, h, "n_burst")

	final, err := h.pipeline.Run(context.Background(), "user1", capture.ID)
	if err != nil {
		t.Fatalf("first run returned %v; a provider refusal is a verdict, not a reason for Lambda to retry", err)
	}
	if final.Status != model.StatusFailed || final.Error != captureProviderFailed || final.RawKey == "" || final.CleanKey != "" {
		t.Fatalf("after the burst the capture is %s (%q, raw %q, clean %q); want failed with the transcript kept",
			final.Status, final.Error, final.RawKey, final.CleanKey)
	}

	// The Retry button: service.RetryCapture clears the verdict, moves the
	// row to the stage after its last artefact and invokes the worker.
	final.Error = ""
	final.Status = model.StatusTranscribed
	if _, err := h.store.PutCapture(context.Background(), final); err != nil {
		t.Fatalf("retry write: %v", err)
	}
	llm.Err = nil
	final, err = h.pipeline.Run(context.Background(), "user1", capture.ID)
	if err != nil || final.Status != model.StatusAppended {
		t.Fatalf("retry run: %s, %v; want appended", final.Status, err)
	}
	if n := stt.Calls(); n != 1 {
		t.Fatalf("STT was called %d times across both runs, want 1: the transcript was already stored", n)
	}
	if n := llm.Calls(); n != 2 {
		t.Fatalf("cleanup was called %d times, want 2 (one refusal, one answer)", n)
	}
	if got, want := counter.total(), controlCounter.total(); got != want || want == 0 {
		t.Errorf("the day was charged %d micros, the burst-free control %d; the refusal must cost nothing", got, want)
	}
}
