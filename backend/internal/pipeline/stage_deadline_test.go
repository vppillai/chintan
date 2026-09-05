package pipeline

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
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
	stt := &fake.STT{Response: "call the roofer on the fourteenth"}
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
