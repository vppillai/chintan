package obs

import (
	"bytes"
	"context"
	"encoding/json"
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

// A note id or search query in a log line is unbounded cardinality, and for
// search it is user content.
func TestRoutePatternDoesNotLeakPathDetail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/notes/note_secret_12345", nil)
	if got := routePattern(req); strings.Contains(got, "note_secret_12345") {
		t.Fatalf("route %q leaked the note id", got)
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
