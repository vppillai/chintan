package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"
)

// Namespace is the CloudWatch namespace every Chintan metric lands in.
const Namespace = "Chintan"

// Unit values accepted by CloudWatch for the metrics this service emits.
const (
	UnitCount        = "Count"
	UnitMilliseconds = "Milliseconds"
	UnitSeconds      = "Seconds"
	UnitBytes        = "Bytes"
	UnitNone         = "None"
)

// Metric is one datum in an Embedded Metric Format record.
type Metric struct {
	Name  string
	Value float64
	Unit  string
}

var (
	metricMu  sync.Mutex
	metricOut io.Writer = os.Stdout
	nowFn               = time.Now
)

// SetMetricOutput redirects EMF records. Intended for tests.
func SetMetricOutput(w io.Writer) func() {
	metricMu.Lock()
	defer metricMu.Unlock()
	prev := metricOut
	metricOut = w
	return func() {
		metricMu.Lock()
		defer metricMu.Unlock()
		metricOut = prev
	}
}

// Emit writes a CloudWatch Embedded Metric Format record to stdout.
//
// EMF means metrics cost one log line rather than a PutMetricData API call, so
// there is no client to configure, no batching to get wrong, and no added
// latency on the request path.
//
// Dimension values are part of the metric's identity: keep them low-cardinality.
// Never pass a tenant id, capture id, or anything else unbounded — that mints a
// distinct metric per value and is billed accordingly.
func Emit(ctx context.Context, dimensions map[string]string, metrics ...Metric) {
	if len(metrics) == 0 {
		return
	}

	dimNames := make([]string, 0, len(dimensions))
	for k := range dimensions {
		dimNames = append(dimNames, k)
	}
	sort.Strings(dimNames)

	defs := make([]map[string]string, 0, len(metrics))
	for _, m := range metrics {
		unit := m.Unit
		if unit == "" {
			unit = UnitNone
		}
		defs = append(defs, map[string]string{"Name": m.Name, "Unit": unit})
	}

	record := map[string]any{
		"_aws": map[string]any{
			"Timestamp": nowFn().UnixMilli(),
			"CloudWatchMetrics": []map[string]any{{
				"Namespace":  Namespace,
				"Dimensions": [][]string{dimNames},
				"Metrics":    defs,
			}},
		},
	}
	for k, v := range dimensions {
		record[k] = v
	}
	for _, m := range metrics {
		record[m.Name] = m.Value
	}
	if id := CorrelationID(ctx); id != "" {
		record["correlation_id"] = id
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		// A metric must never take down a request.
		Log(ctx).Warn("failed to encode metric record", slog.String("error", err.Error()))
		return
	}

	metricMu.Lock()
	defer metricMu.Unlock()
	fmt.Fprintln(metricOut, string(encoded))
}

// Count emits a single counter increment.
func Count(ctx context.Context, name string, dimensions map[string]string) {
	Emit(ctx, dimensions, Metric{Name: name, Value: 1, Unit: UnitCount})
}

// Duration emits an elapsed time in milliseconds.
func Duration(ctx context.Context, name string, d time.Duration, dimensions map[string]string) {
	Emit(ctx, dimensions, Metric{Name: name, Value: float64(d.Milliseconds()), Unit: UnitMilliseconds})
}
