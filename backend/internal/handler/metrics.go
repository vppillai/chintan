package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/vppillai/chintan/backend/internal/obs"
)

// routeKey carries the registered pattern down to handlers that want to label a
// metric without reading the raw path.
type routeKey struct{}

// routeOf returns the registered route pattern for this request, or "unmatched".
//
// It is deliberately never the raw path: a note id in a metric dimension is
// unbounded cardinality and is billed as such, and a search path carries the
// query — which is user content and must not reach a log line.
func routeOf(r *http.Request) string {
	if v, ok := r.Context().Value(routeKey{}).(string); ok && v != "" {
		return v
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// statusWriter records the status code for the metric without buffering.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// instrument emits one EMF record per request: an outcome counter and a
// latency.
//
// The dimensions are the registered route and the status class. Class rather
// than exact status keeps the metric count bounded while still letting an alarm
// fire on 5xx.
//
// There is deliberately no Method dimension. The registered pattern already
// begins with the method — "GET /v1/notes" — so Method is a function of Route,
// never an independent axis. It cost bytes on every record and implied a
// breakdown that does not exist.
//
// There is likewise no separate 5xx counter. CloudWatch bills one metric per
// unique name and dimension-set combination, and a counter carrying these same
// dimensions filtered to Status="5xx" is ApiRequests with a filter applied —
// something the query side does for free.
func instrument(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), routeKey{}, pattern)
		r = r.WithContext(ctx)

		rec := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		obs.Emit(ctx, map[string]string{
			"Route":  pattern,
			"Status": statusClass(rec.status),
		},
			obs.Metric{Name: "ApiRequests", Value: 1, Unit: obs.UnitCount},
			obs.Metric{Name: "ApiLatency", Value: float64(elapsed.Milliseconds()), Unit: obs.UnitMilliseconds},
		)
	})
}

func statusClass(status int) string {
	return strconv.Itoa(status/100) + "xx"
}
