package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

// The month's rows, reduced to hops and grouped by source: a device's
// captures under its id with the device lag its recorded_at gives, the
// app's under `app`, a row from before the timing record counted as legacy
// with its total alone, a failed one counted as an outcome. Nothing is
// written.
func TestLatencyGroupsCapturesBySourceAndHop(t *testing.T) {
	part := newFakePartition()
	ctx := context.Background()
	base := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return model.FormatTime(base.Add(d)) }
	put := func(id string, c model.CaptureIndex) {
		t.Helper()
		c.ID, c.UserID = id, "alice"
		blob, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.Put(ctx, Item{"pk": StringAttr(tenantPK("alice")), "sk": StringAttr("CAPTURE#" + id), "data": StringAttr(string(blob))}); err != nil {
			t.Fatal(err)
		}
	}
	put("c_app", model.CaptureIndex{
		Status: model.StatusAppended, CreatedAt: at(0), AppendedAt: base.Add(16 * time.Second).Unix(),
		StageAt: map[string]string{
			"transcribing": at(2 * time.Second), "transcribed": at(12 * time.Second),
			"cleaning": at(13 * time.Second), "appending": at(15 * time.Second), "appended": at(16 * time.Second),
		},
	})
	put("c_dev", model.CaptureIndex{
		Status: model.StatusAppended, Source: model.DeviceSource("dev_x"),
		RecordedAt: at(-12 * time.Second), CreatedAt: at(0),
		StageAt: map[string]string{"cleaning": at(time.Second), "appending": at(3 * time.Second), "appended": at(4 * time.Second)},
	})
	put("c_legacy", model.CaptureIndex{Status: model.StatusAppended, CreatedAt: at(time.Hour), AppendedAt: base.Add(time.Hour + 30*time.Second).Unix()})
	put("c_failed", model.CaptureIndex{Status: model.StatusFailed, CreatedAt: at(2 * time.Hour), StageAt: map[string]string{"transcribing": at(2*time.Hour + time.Second)}})
	// Another month's row is not the month's.
	put("c_august", model.CaptureIndex{Status: model.StatusAppended, CreatedAt: model.FormatTime(base.AddDate(0, -1, 0)), AppendedAt: base.AddDate(0, -1, 0).Unix() + 5})

	e := &env{Part: part, Blobs: newFakeBlobs(), Target: target{Instance: "dev", Environment: "prod"}}
	putsBefore := part.puts
	res, err := runLatency(ctx, e, "2026-09", []string{"alice"})
	if err != nil {
		t.Fatalf("runLatency: %v", err)
	}
	if len(res.Groups) != 2 || res.Groups[0].Source != "app" || res.Groups[1].Source != "device:dev_x" {
		t.Fatalf("groups = %+v", res.Groups)
	}
	app, dev := res.Groups[0], res.Groups[1]
	if app.Captures != 3 || app.Legacy != 1 || app.Outcomes["failed"] != 1 || len(app.Outcomes) != 1 {
		t.Errorf("app = %+v", app)
	}
	if got := app.Hops["total"]; got != (latencyStat{Count: 2, P50: 16_000, P95: 30_000, Max: 30_000}) {
		t.Errorf("app total = %+v; want the StageAt row's 16 s and the legacy row's 30 s", got)
	}
	if got := app.Hops["transcribe"]; got != (latencyStat{Count: 1, P50: 10_000, P95: 10_000, Max: 10_000}) {
		t.Errorf("app transcribe = %+v", got)
	}
	if got := app.Hops["queue"]; got.Count != 2 || got.P50 != 1_000 || got.Max != 2_000 {
		t.Errorf("app queue = %+v; the failed row queued 1 s, the appended one 2 s", got)
	}
	if _, ok := app.Hops["route"]; ok {
		t.Errorf("app has a route hop with no routing stamp: %+v", app.Hops)
	}
	if dev.Captures != 1 || dev.Legacy != 0 || len(dev.Outcomes) != 0 {
		t.Errorf("device = %+v", dev)
	}
	for hop, want := range map[string]int64{"device_lag": 12_000, "queue": 1_000, "clean": 2_000, "append": 1_000, "total": 4_000} {
		if got := dev.Hops[hop]; got.Count != 1 || got.P50 != want {
			t.Errorf("device %s = %+v, want p50 %d", hop, got, want)
		}
	}
	if _, ok := dev.Hops["transcribe"]; ok {
		t.Errorf("a text capture has a transcribe hop: %+v", dev.Hops)
	}
	if part.puts != putsBefore || part.updates != 0 || part.deletes != 0 {
		t.Errorf("the listing changed the table: puts=%d updates=%d deletes=%d", part.puts-putsBefore, part.updates, part.deletes)
	}

	var out bytes.Buffer
	if err := report(&out, false, res); err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, want := range []string{"latency dev (prod) for 2026-09", "app: 3 captures, 2 appended, failed 1, 1 legacy (total only)", "device:dev_x: 1 captures, 1 appended", "device_lag", "12000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("human output lacks %q:\n%s", want, out.String())
		}
	}
	var asJSON bytes.Buffer
	if err := report(&asJSON, true, res); err != nil {
		t.Fatalf("report(json): %v", err)
	}
	if !strings.Contains(asJSON.String(), `"source": "device:dev_x"`) || !strings.Contains(asJSON.String(), `"p50_ms": 12000`) {
		t.Errorf("json output:\n%s", asJSON.String())
	}
}

func TestLatencyCommandRefusesAMalformedMonth(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"latency", "--instance", "dev", "--month", "September"}, &stdout, &stderr, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "yyyy-mm") {
		t.Errorf("err = %v, want a month format error", err)
	}
}

func TestPercentileIsNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30, 40}
	for _, tc := range []struct {
		p    float64
		want int64
	}{{50, 20}, {95, 40}, {100, 40}, {1, 10}} {
		if got := percentile(sorted, tc.p); got != tc.want {
			t.Errorf("percentile(%v, %v) = %d, want %d", sorted, tc.p, got, tc.want)
		}
	}
	if got := percentile([]int64{7}, 50); got != 7 {
		t.Errorf("one sample: %d", got)
	}
}
