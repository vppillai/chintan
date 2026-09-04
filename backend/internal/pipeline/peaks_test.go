package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// seedCaptureWithPeaksKey is seedUploadedCapture plus the peaks key the API
// records when it issues the presigned PUT — before the client has uploaded
// anything.
func seedCaptureWithPeaksKey(t *testing.T, h *harness) model.CaptureIndex {
	t.Helper()
	capture := seedUploadedCapture(t, h, "note1")
	capture.PeaksKey = "tenants/user1/captures/c_1/peaks.json"
	stored, err := h.store.PutCapture(context.Background(), capture)
	if err != nil {
		t.Fatalf("seed peaks key: %v", err)
	}
	return stored
}

// The API derives has_peaks from the peaks key, and the key is written when the
// upload URL is issued. If the client never used the URL, the worker is the
// party that finds out, and the capture must stop claiming a waveform it does
// not have.
func TestPipelineClearsPeaksKeyWhenTheClientUploadedNone(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Cleaned."}})
	seedCaptureWithPeaksKey(t, h)

	final, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended (error=%q)", final.Status, final.Error)
	}
	if final.PeaksKey != "" {
		t.Errorf("PeaksKey = %q after a pipeline that found no peaks object; want cleared", final.PeaksKey)
	}
	stored, err := h.store.GetCapture(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if stored.PeaksKey != "" {
		t.Errorf("stored PeaksKey = %q; the cleared key was not persisted", stored.PeaksKey)
	}
}

func TestPipelineKeepsPeaksKeyWhenTheObjectExists(t *testing.T) {
	h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Cleaned."}})
	seedCaptureWithPeaksKey(t, h)
	if err := h.objects.Put(context.Background(), "tenants/user1/captures/c_1/peaks.json", []byte(`{"peaks":[0.1]}`), "application/json"); err != nil {
		t.Fatalf("seed peaks: %v", err)
	}

	final, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.PeaksKey != "tenants/user1/captures/c_1/peaks.json" {
		t.Errorf("PeaksKey = %q; a peaks object that exists must keep its key", final.PeaksKey)
	}
}
