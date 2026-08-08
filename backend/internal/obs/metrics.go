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
	emit(ctx, dimensions, false, metrics...)
}

// EmitWithRollup emits the same metrics under two identities: the dimensioned
// one, and a dimensionless rollup.
//
// It exists because a dimensioned metric cannot be alarmed on cheaply. A
// CloudWatch alarm has to name a dimension set, and Emit declares exactly one —
// so an alarm on a metric dimensioned by Provider must either name every
// provider (a template edit per provider, and silence for the one nobody
// added) or be a Metrics Insights query alarm, which is billed per metric
// analysed with no free tier. The rollup costs one extra metric identity, which
// is inside the free ten, and lets an ordinary standard-resolution alarm read
// it for nothing. The dimensioned copy is still there, and is what says which
// provider it was once the alarm has fired.
//
// Both identities carry the same value, so summing across them double-counts.
// Nothing does, and nothing should: the dimensionless one is for the alarm and
// the dimensioned one is for the answer.
func EmitWithRollup(ctx context.Context, dimensions map[string]string, metrics ...Metric) {
	emit(ctx, dimensions, true, metrics...)
}

func emit(ctx context.Context, dimensions map[string]string, rollup bool, metrics ...Metric) {
	if len(metrics) == 0 {
		return
	}

	dimNames := make([]string, 0, len(dimensions))
	for k := range dimensions {
		dimNames = append(dimNames, k)
	}
	sort.Strings(dimNames)

	// EMF publishes one metric per dimension set listed here. An empty set is
	// how a dimensionless metric is declared; asking for a rollup of nothing
	// would declare the same identity twice.
	dimensionSets := [][]string{dimNames}
	if rollup && len(dimNames) > 0 {
		dimensionSets = append(dimensionSets, []string{})
	}

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
				"Dimensions": dimensionSets,
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
	// Discarded deliberately, for the same reason the encode failure above only
	// warns: metricOut is the Lambda's stdout, Emit returns nothing, and a
	// metric must never take down a request. There is nowhere to report a
	// failed write to that is not the thing that just failed.
	_, _ = fmt.Fprintln(metricOut, string(encoded))
}

// Count emits a single counter increment.
func Count(ctx context.Context, name string, dimensions map[string]string) {
	Emit(ctx, dimensions, Metric{Name: name, Value: 1, Unit: UnitCount})
}

// CountWithRollup is Count plus the dimensionless identity an alarm can read.
// Use it only for counters something actually alarms on; every other counter
// should stay on Count and cost one identity per dimension value rather than
// one more.
func CountWithRollup(ctx context.Context, name string, dimensions map[string]string) {
	EmitWithRollup(ctx, dimensions, Metric{Name: name, Value: 1, Unit: UnitCount})
}

// Duration emits an elapsed time in milliseconds.
func Duration(ctx context.Context, name string, d time.Duration, dimensions map[string]string) {
	Emit(ctx, dimensions, Metric{Name: name, Value: float64(d.Milliseconds()), Unit: UnitMilliseconds})
}
