package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/service"
)

// A tenant who set a daily cap of a few cents must not be able to run a
// twenty-minute transcription. The API's SpendGate is a courtesy check taken
// once, before the recording is uploaded; by the time the worker picks the
// capture up, the only thing between the tenant and the provider is the
// breaker — so the breaker is where the tenant's number has to be known.
func TestATenantSetCapStopsTheProviderCallInThePipeline(t *testing.T) {
	h := newHarness(t, harnessOpts{
		// The instance-wide default: meter everything, enforce nothing.
		capMicros: 0,
		// The tenant asked for a cap of one microdollar a day.
		caps: tenantCaps{"user1": 1},
	})
	seedUploadedCapture(t, h, "note1")

	capture, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := h.stt.Calls(); got != 0 {
		t.Fatalf("the speech provider was called %d time(s) despite the tenant's daily cap; "+
			"the cap the user set never reaches the thing that enforces it", got)
	}
	if capture.Status != service.StatusSpendCapped {
		t.Fatalf("status = %s (error=%q), want %s", capture.Status, capture.Error, service.StatusSpendCapped)
	}
}

// The same tenant, under their cap, still gets their capture processed.
func TestATenantWellUnderTheirCapIsNotStopped(t *testing.T) {
	h := newHarness(t, harnessOpts{
		capMicros: 0,
		caps:      tenantCaps{"user1": 1_000_000_000},
		stt:       &fake.STT{Response: "the gutter is leaking again"},
		llm:       &fake.LLM{Response: "The gutter is leaking again."},
	})
	seedUploadedCapture(t, h, "note1")

	capture, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (error=%q), want appended", capture.Status, capture.Error)
	}
}
