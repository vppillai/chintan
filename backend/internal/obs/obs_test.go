package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCorrelationIDRoundTrip(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "abc-123")
	if got := CorrelationID(ctx); got != "abc-123" {
		t.Fatalf("got %q", got)
	}
	if got := CorrelationID(context.Background()); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestSanitizeCorrelationID(t *testing.T) {
	good := []string{"abc-123", "0123456789", "a"}
	for _, v := range good {
		if _, ok := SanitizeCorrelationID(v); !ok {
			t.Fatalf("%q should be accepted", v)
		}
	}

	// A hostile header must not be able to inject a newline into the log
	// stream, or pad it with an unbounded value.
	bad := []string{"", "   ", "with\nnewline", "with\rcarriage", "tab\there", strings.Repeat("x", 65), "emoji-🙂"}
	for _, v := range bad {
		if _, ok := SanitizeCorrelationID(v); ok {
			t.Fatalf("%q should be rejected", v)
		}
	}
}

func TestCorrelateEchoesSuppliedID(t *testing.T) {
	var seen string
	h := Correlate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set(HeaderCorrelationID, "caller-supplied")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen != "caller-supplied" {
		t.Fatalf("handler saw %q", seen)
	}
	if got := w.Header().Get(HeaderCorrelationID); got != "caller-supplied" {
		t.Fatalf("response header %q", got)
	}
}

func TestCorrelateGeneratesIDWhenHostile(t *testing.T) {
	var seen string
	h := Correlate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set(HeaderCorrelationID, "injected\nlog line")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen == "" || strings.ContainsAny(seen, "\n\r") {
		t.Fatalf("generated id is unusable: %q", seen)
	}
}

// captureLogs routes the package logger into a buffer for one test and returns
// the access lines it wrote, decoded.
func captureLogs(t *testing.T, run func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	run()

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, line)
		}
		if rec["msg"] == "request" {
			lines = append(lines, rec)
		}
	}
	return lines
}

// The access line names the matched ServeMux pattern, so POST /v1/notes/purge
// is distinguishable from POST /v1/notes and GET /v1/captures/{id}/download from
// the list — which the old two-segment prefix could not do. The pattern is
// recorded from inside the mux, because outside it r.Pattern is empty.
func TestCorrelateLogsTheMatchedRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	for _, pattern := range []string{"POST /v1/notes", "POST /v1/notes/purge", "GET /v1/captures/{id}/download"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			SetRoutePattern(r.Context(), r.Pattern)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	// A middleware that copies the request, as the real chain does, so the
	// mux's annotation never reaches Correlate's own *Request.
	copying := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(r.Context()))
	})
	h := Correlate(copying)

	tests := []struct {
		method, path, want string
	}{
		{http.MethodPost, "/v1/notes", "POST /v1/notes"},
		{http.MethodPost, "/v1/notes/purge", "POST /v1/notes/purge"},
		{http.MethodGet, "/v1/captures/c_18d1eef7f15ea266/download?kind=peaks", "GET /v1/captures/{id}/download"},
	}
	for _, tt := range tests {
		lines := captureLogs(t, func() {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tt.method, tt.path, nil))
		})
		if len(lines) != 1 {
			t.Fatalf("%s %s: %d access lines, want 1", tt.method, tt.path, len(lines))
		}
		if got := lines[0]["route"]; got != tt.want {
			t.Errorf("%s %s: route = %v, want %q", tt.method, tt.path, got, tt.want)
		}
	}
}

// A request no route served is labelled as such. Nothing of the path — not a
// note id, not a search query, not a probe for /wp-admin — reaches the line.
func TestCorrelateLabelsUnmatchedRequestsWithoutThePath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/notes", func(w http.ResponseWriter, r *http.Request) {
		SetRoutePattern(r.Context(), r.Pattern)
	})
	h := Correlate(mux)

	for _, path := range []string{"/v1/notes/note_secret_12345", "/v1/search?q=my+secret+plans", "/wp-admin"} {
		lines := captureLogs(t, func() {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		})
		if len(lines) != 1 {
			t.Fatalf("%s: %d access lines, want 1", path, len(lines))
		}
		route, _ := lines[0]["route"].(string)
		if route != unmatchedRoute {
			t.Errorf("%s: route = %q, want %q", path, route, unmatchedRoute)
		}
		for _, leak := range []string{"note_secret_12345", "secret", "wp-admin"} {
			if strings.Contains(route, leak) {
				t.Errorf("route %q leaked %q", route, leak)
			}
		}
	}
}

func TestRoutePatternIsInertWithoutCorrelate(t *testing.T) {
	ctx := context.Background()
	SetRoutePattern(ctx, "GET /v1/notes")
	if got := RoutePattern(ctx); got != "" {
		t.Errorf("RoutePattern = %q on a context Correlate never saw", got)
	}
}

func TestRedactRevealsShapeNotContent(t *testing.T) {
	secret := "the contractor said the flashing is the problem"
	v := Redact(secret)
	encoded := v.String()
	if strings.Contains(encoded, "contractor") || strings.Contains(encoded, "flashing") {
		t.Fatalf("Redact leaked content: %s", encoded)
	}
	if !strings.Contains(encoded, "bytes=") || !strings.Contains(encoded, "words=") {
		t.Fatalf("Redact should report shape, got %s", encoded)
	}
}

func TestEmitProducesValidEMF(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	ctx := WithCorrelationID(context.Background(), "corr-1")
	Emit(ctx, map[string]string{"Instance": "dev", "Stage": "transcribe"},
		Metric{Name: "CaptureLatency", Value: 1234, Unit: UnitMilliseconds},
		Metric{Name: "CaptureCount", Value: 1, Unit: UnitCount},
	)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("emitted record is not valid JSON: %v (%s)", err, buf.String())
	}

	aws, ok := rec["_aws"].(map[string]any)
	if !ok {
		t.Fatalf("missing _aws block: %s", buf.String())
	}
	cw, ok := aws["CloudWatchMetrics"].([]any)
	if !ok || len(cw) != 1 {
		t.Fatalf("missing CloudWatchMetrics: %s", buf.String())
	}
	block := cw[0].(map[string]any)
	if block["Namespace"] != Namespace {
		t.Fatalf("namespace=%v", block["Namespace"])
	}

	// Dimension names must appear both in the definition and as top-level keys,
	// or CloudWatch silently drops the metric.
	dims := block["Dimensions"].([]any)[0].([]any)
	for _, d := range dims {
		if _, present := rec[d.(string)]; !present {
			t.Fatalf("dimension %v declared but not present as a field", d)
		}
	}
	if rec["CaptureLatency"].(float64) != 1234 {
		t.Fatalf("value=%v", rec["CaptureLatency"])
	}
	if rec["correlation_id"] != "corr-1" {
		t.Fatalf("correlation_id=%v", rec["correlation_id"])
	}
}

func TestEmitIgnoresEmptyMetricSet(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	Emit(context.Background(), map[string]string{"Instance": "dev"})
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %s", buf.String())
	}
}

func TestCountAndDurationHelpers(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	Count(context.Background(), "Widgets", map[string]string{"Instance": "dev"})
	Duration(context.Background(), "Elapsed", 250*time.Millisecond, map[string]string{"Instance": "dev"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 records, got %d: %s", len(lines), buf.String())
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second["Elapsed"].(float64) != 250 {
		t.Fatalf("Elapsed=%v", second["Elapsed"])
	}
}

// TestEmitWithRollupDeclaresBothIdentities covers what makes a dimensioned
// counter alarmable without a Metrics Insights query.
//
// CloudWatch bills and addresses a metric by name plus dimension set, so a
// counter that only ever declares ["Provider"] has no identity an alarm can
// name unless the alarm names every provider value. The empty set is that
// identity. If it stops being emitted, the alarms pointed at it receive no
// datapoints and — under TreatMissingData: notBreaching — stay green through
// exactly the outage they exist to report.
func TestEmitWithRollupDeclaresBothIdentities(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	EmitWithRollup(context.Background(), map[string]string{"Provider": "groq"},
		Metric{Name: "ProviderKeyRejected", Value: 1, Unit: UnitCount})

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("emitted record is not valid JSON: %v (%s)", err, buf.String())
	}
	block := rec["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)
	sets := block["Dimensions"].([]any)
	if len(sets) != 2 {
		t.Fatalf("declared %d dimension sets, want 2: %s", len(sets), buf.String())
	}
	if first := sets[0].([]any); len(first) != 1 || first[0] != "Provider" {
		t.Errorf("first dimension set = %v, want [Provider]", first)
	}
	if second := sets[1].([]any); len(second) != 0 {
		t.Errorf("second dimension set = %v, want the empty set an alarm can read", second)
	}
	// The dimension value still has to be a top-level field or CloudWatch drops
	// the dimensioned copy.
	if rec["Provider"] != "groq" {
		t.Errorf("Provider field = %v, want groq", rec["Provider"])
	}
}

// TestEmitWithRollupOnADimensionlessMetricDeclaresOneIdentity keeps the rollup
// from declaring the same empty set twice, which would ask CloudWatch to
// publish one metric under one identity two times.
func TestEmitWithRollupOnADimensionlessMetricDeclaresOneIdentity(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	EmitWithRollup(context.Background(), nil, Metric{Name: "Lonely", Value: 1, Unit: UnitCount})

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	block := rec["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if sets := block["Dimensions"].([]any); len(sets) != 1 {
		t.Fatalf("declared %d dimension sets, want 1: %s", len(sets), buf.String())
	}
}

// TestEmitStillDeclaresOneIdentity guards the metrics that must not become
// more expensive. Every existing counter goes through Emit, and quietly adding
// a rollup to all of them would add an identity per metric name across the
// whole surface.
func TestEmitStillDeclaresOneIdentity(t *testing.T) {
	var buf bytes.Buffer
	restore := SetMetricOutput(&buf)
	defer restore()

	Count(context.Background(), "Widgets", map[string]string{"Instance": "dev"})

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	block := rec["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if sets := block["Dimensions"].([]any); len(sets) != 1 {
		t.Fatalf("Count declared %d dimension sets, want 1: %s", len(sets), buf.String())
	}
}
