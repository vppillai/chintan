package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// The timing record: every persist stamps the stage it enters, once, and the
// worker emits the queue delay when it picks a capture up and the end-to-end
// time when the capture lands — by source, app or device, never by device
// id.
func TestPipelineKeepsATimingRecordAndEmitsItsTwoMetrics(t *testing.T) {
	ctx := context.Background()

	t.Run("audio from a device", func(t *testing.T) {
		var metrics bytes.Buffer
		defer obs.SetMetricOutput(&metrics)()
		stt := &fake.STT{Response: "the gutter is leaking", Duration: 12}
		llm := &fake.LLM{Response: "The gutter is leaking."}
		h := newHarness(t, harnessOpts{stt: stt, llm: llm})
		stt.OnCall = func() { h.clock.Advance(4 * time.Second) }
		llm.OnCall = func() { h.clock.Advance(2 * time.Second) }
		seedNote(t, h.store, h.objects, "note1")
		key := "tenants/user1/captures/c_1/audio.webm"
		if err := h.objects.Put(ctx, key, []byte("audio"), "audio/webm"); err != nil {
			t.Fatal(err)
		}
		created := h.clock.Now().Add(-30 * time.Second)
		if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_1", UserID: "user1", NoteID: "note1", Status: model.StatusUploaded, AudioKey: key,
			CreatedAt: model.FormatTime(created), RecordedAt: model.FormatTime(created.Add(-12 * time.Second)),
			Source: model.DeviceSource("dev_1"),
		}); err != nil {
			t.Fatal(err)
		}

		final, err := h.pipeline.Run(ctx, "user1", "c_1")
		if err != nil || final.Status != model.StatusAppended {
			t.Fatalf("Run = %s, %v", final.Status, err)
		}
		assertStagesInOrder(t, final.StageAt, model.StatusTranscribing, model.StatusTranscribed, model.StatusCleaning, model.StatusAppending, model.StatusAppended)
		if _, ok := final.StageAt[string(model.StatusRouting)]; ok {
			t.Errorf("a targeted capture was stamped as routing: %v", final.StageAt)
		}
		assertOneDuration(t, &metrics, "CaptureQueueDelay", "device", 30_000)
		// Thirty seconds in the queue, then the two provider calls advanced the clock.
		assertOneDuration(t, &metrics, "CaptureEndToEnd", "device", 36_000)
	})

	t.Run("text from the app", func(t *testing.T) {
		var metrics bytes.Buffer
		defer obs.SetMetricOutput(&metrics)()
		h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Buy milk."}})
		seedNote(t, h.store, h.objects, "note1")
		if err := h.objects.Put(ctx, "tenants/user1/captures/c_t/raw.txt", []byte("buy milk"), "text/plain"); err != nil {
			t.Fatal(err)
		}
		created := h.clock.Now().Add(-5 * time.Second)
		if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_t", UserID: "user1", NoteID: "note1", Status: model.StatusTranscribed, CreatedAt: model.FormatTime(created),
			RawKey: "tenants/user1/captures/c_t/raw.txt", TargetSource: model.TargetSourceClient,
		}); err != nil {
			t.Fatal(err)
		}

		final, err := h.pipeline.Run(ctx, "user1", "c_t")
		if err != nil || final.Status != model.StatusAppended {
			t.Fatalf("Run = %s, %v", final.Status, err)
		}
		assertStagesInOrder(t, final.StageAt, model.StatusCleaning, model.StatusAppending, model.StatusAppended)
		if _, ok := final.StageAt[string(model.StatusTranscribing)]; ok {
			t.Errorf("text was stamped as transcribing: %v", final.StageAt)
		}
		assertOneDuration(t, &metrics, "CaptureQueueDelay", "app", 5_000)
		assertOneDuration(t, &metrics, "CaptureEndToEnd", "app", 5_000)
	})

	t.Run("a retry keeps the first stamp", func(t *testing.T) {
		c := model.CaptureIndex{}
		c.StageEntered(model.StatusCleaning, "2026-09-26T10:00:00.000000000Z")
		before := c.StageAt
		c.StageEntered(model.StatusCleaning, "2026-09-26T10:05:00.000000000Z")
		c.StageEntered(model.StatusAppending, "2026-09-26T10:06:00.000000000Z")
		if c.StageAt["cleaning"] != "2026-09-26T10:00:00.000000000Z" || c.StageAt["appending"] != "2026-09-26T10:06:00.000000000Z" {
			t.Fatalf("stage_at = %v", c.StageAt)
		}
		if _, ok := before["appending"]; ok {
			t.Fatal("the earlier map was written through")
		}
	})
}

// assertStagesInOrder wants every stage stamped and the stamps not
// decreasing along the pipeline's order. FormatTime's layout is fixed-width,
// so a string comparison is a time comparison.
func assertStagesInOrder(t *testing.T, stageAt map[string]string, order ...model.CaptureStatus) {
	t.Helper()
	prev := ""
	for _, s := range order {
		at, ok := stageAt[string(s)]
		if !ok {
			t.Fatalf("stage_at lacks %s: %v", s, stageAt)
		}
		if at < prev {
			t.Fatalf("%s at %s is before the stage before it at %s", s, at, prev)
		}
		prev = at
	}
}

// assertOneDuration finds exactly one EMF record carrying name, with the
// Source dimension and the value in milliseconds given.
func assertOneDuration(t *testing.T, metrics *bytes.Buffer, name, source string, wantMS float64) {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(metrics.String(), "\n") {
		if !strings.Contains(line, `"`+name+`"`) {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("metric line is not JSON: %v (%s)", err, line)
		}
		found = append(found, rec)
	}
	if len(found) != 1 {
		t.Fatalf("%d %s records, want 1:\n%s", len(found), name, metrics.String())
	}
	if found[0]["Source"] != source || found[0][name] != wantMS {
		t.Fatalf("%s = %v (Source %v), want %v for %s", name, found[0][name], found[0]["Source"], wantMS, source)
	}
	for k, v := range found[0] {
		if s, ok := v.(string); ok && strings.Contains(s, "dev_") {
			t.Fatalf("the metric names the device: %s=%q", k, s)
		}
	}
}
