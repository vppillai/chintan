package obs

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// statusRecorder captures the status code for the access log without buffering
// the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the wrapped writer when it supports flushing.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Correlate assigns every request a correlation id, echoes it back, and writes
// one structured access log line per request.
//
// The id crosses into the worker in the payload of the asynchronous Lambda
// invocation, so one capture is one greppable trace from the retry request
// through the append.
func Correlate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := SanitizeCorrelationID(r.Header.Get(HeaderCorrelationID))
		if !ok {
			id = NewCorrelationID()
		}
		ctx := WithCorrelationID(r.Context(), id)
		w.Header().Set(HeaderCorrelationID, id)

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		elapsed := time.Since(start)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		// The route pattern, not the raw path: a note id in a metric dimension
		// or a log message is unbounded cardinality and, for search queries,
		// user content.
		Log(ctx).Info("request",
			slog.String("method", r.Method),
			slog.String("route", routePattern(r)),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Int64("duration_ms", elapsed.Milliseconds()),
		)
	})
}

// routePattern returns the matched ServeMux pattern when one is available,
// falling back to a coarse prefix. It never returns the raw path, which can
// contain note ids and search terms.
func routePattern(r *http.Request) string {
	if p := r.Pattern; p != "" {
		return p
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	// Keep the version and collection only: "/v1/notes". Anything beyond that
	// is an identifier or a query.
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return "/" + strings.Join(parts, "/")
}
