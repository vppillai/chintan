package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// The timing record stays in the row: recorded_at and stage_at are telemetry
// for the worker's metrics and `chintanctl latency`, and reach neither the
// capture on the wire nor, through it, the app.
func TestCaptureWireCarriesNoTimingRecord(t *testing.T) {
	h := newHarness(t)
	c := h.putCapture(t, model.CaptureIndex{
		ID: "c_timed", UserID: "user1", Status: model.StatusAppended, CreatedAt: model.Now(),
		RecordedAt: model.Now(), StageAt: map[string]string{string(model.StatusAppending): model.Now()},
	})
	w := h.do(t, http.MethodGet, "/v1/captures/"+c.ID, "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var keys map[string]json.RawMessage
	decodeInto(t, w, &keys)
	for _, key := range []string{"recorded_at", "stage_at"} {
		if _, ok := keys[key]; ok {
			t.Errorf("the capture on the wire carries %s: %s", key, w.Body.String())
		}
	}
	if _, ok := keys["created_at"]; !ok {
		t.Fatalf("the capture on the wire is not a capture: %s", w.Body.String())
	}
}
