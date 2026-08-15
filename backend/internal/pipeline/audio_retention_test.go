package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// This session found a capture uploaded on 2026-08-08 that had sat at
// `uploaded` for six days: the S3 event that should have driven the worker
// never arrived. By the time something finally retried it, the retention
// lifecycle rule — which back then required only the artifact and
// retention-tier tags, both set once at upload and never touched again — had
// already deleted the audio, on a clock that had no idea the pipeline had
// never run at all. The capture correctly failed with a 404 fetching its own
// source, and the thought in it was gone.
//
// MarkProcessed and the lifecycle rule's new third tag close that: the rule
// now also requires ProcessedTagKey, and markAudioProcessedIfSafe is the one
// place that sets it — gated on RawKey rather than on the capture reaching a
// terminal status, because that is the actual point past which the audio
// object is no longer needed for anything (resumeStatusFor prefers RawKey
// explicitly, so a retry from any later stage never re-reads it).

func TestAudioStaysProtectedWhileTranscriptionHasNotSucceeded(t *testing.T) {
	objects := memory.NewObjects()
	h := newHarness(t, harnessOpts{
		objects: objects,
		stt:     &fake.STT{Err: errors.New("provider: groq request: connection reset")},
	})

	ctx := context.Background()
	const audioKey = "tenants/user1/captures/c_stuck/audio.webm"
	if err := objects.Put(ctx, audioKey, []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_stuck", UserID: "user1", Status: model.StatusUploaded,
		AudioKey: audioKey, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	final, err := h.pipeline.Run(ctx, "user1", "c_stuck")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != model.StatusFailed {
		t.Fatalf("status = %s, want %s", final.Status, model.StatusFailed)
	}
	if final.RawKey != "" {
		t.Fatalf("RawKey = %q, want empty — transcription did not succeed", final.RawKey)
	}
	if objects.Tags(audioKey)[repository.ProcessedTagKey] == repository.ProcessedTagValue {
		t.Fatal("audio was tagged processed although transcription never succeeded; " +
			"a retry needs this exact object, and the lifecycle rule would now be free to delete it")
	}
}

func TestAudioIsTaggedProcessedOnceTranscriptionSucceeds(t *testing.T) {
	objects := memory.NewObjects()
	h := newHarness(t, harnessOpts{
		objects: objects,
		stt:     &fake.STT{Response: "the one and only dictated sentence"},
		router:  &fake.Router{Decision: provider.RouteDecision{Action: provider.RouteNew, Title: "Roof repair"}},
	})

	ctx := context.Background()
	const audioKey = "tenants/user1/captures/c_ok/audio.webm"
	if err := objects.Put(ctx, audioKey, []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_ok", UserID: "user1", Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: audioKey, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	final, err := h.pipeline.Run(ctx, "user1", "c_ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want %s", final.Status, model.StatusAppended)
	}
	if final.RawKey == "" {
		t.Fatal("RawKey is empty; the fixture is not exercising what this test claims to")
	}
	if got := objects.Tags(audioKey)[repository.ProcessedTagKey]; got != repository.ProcessedTagValue {
		t.Fatalf("audio ProcessedTagKey = %q, want %q — the lifecycle rule will never expire this object",
			got, repository.ProcessedTagValue)
	}
}

// A capture parked at needs_target is not terminal — service.CaptureIsTerminal
// says so, deliberately, because the pipeline has not finished with it, the
// user still has to answer "which note?". But transcription has already
// succeeded by the time routing runs, so the audio's only job is done, and
// this asserts the tag is set anyway: gating on RawKey rather than on
// CaptureIsTerminal is the actual design decision this fix makes, not
// incidental to it.
func TestAudioIsTaggedProcessedEvenWhileNeedsTarget(t *testing.T) {
	objects := memory.NewObjects()
	h := newHarness(t, harnessOpts{
		objects: objects,
		stt:     &fake.STT{Response: "a sentence with nowhere obvious to go"},
		noNotes: true,
	})

	ctx := context.Background()
	const audioKey = "tenants/user1/captures/c_ask/audio.webm"
	if err := objects.Put(ctx, audioKey, []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_ask", UserID: "user1", Status: model.StatusUploaded,
		AudioKey: audioKey, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	final, err := h.pipeline.Run(ctx, "user1", "c_ask")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != model.StatusNeedsTarget {
		t.Fatalf("status = %s, want %s", final.Status, model.StatusNeedsTarget)
	}
	if got := objects.Tags(audioKey)[repository.ProcessedTagKey]; got != repository.ProcessedTagValue {
		t.Fatalf("audio ProcessedTagKey = %q, want %q — needs_target is not terminal, "+
			"but transcription already succeeded so the audio's job here is done",
			got, repository.ProcessedTagValue)
	}
}
