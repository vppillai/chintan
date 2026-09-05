package obs

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// routeLabelKey carries a *routeLabel down the request context.
type routeLabelKey struct{}

// routeLabel is the one thing in a request's context that is written after the
// context is made: Correlate creates it before the mux runs, the matched
// route's wrapper fills it in, and Correlate reads it once the handler returns.
// A context value cannot flow upward, so it has to be a pointer set in place.
type routeLabel struct {
	mu      sync.Mutex
	pattern string
}

func (l *routeLabel) set(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pattern = p
}

func (l *routeLabel) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pattern
}

// unmatchedRoute labels a request no registered route served: the mux's own
// 404 and 405, or a path outside the API entirely. It carries no part of the
// path, so a probe for /wp-admin or a typo with a note id in it cannot put
// either into the log. Like a matched pattern it is prefixed with the method
// in the access line, since `route` is the only place the method is logged.
const unmatchedRoute = "unmatched"

// SetRoutePattern records the ServeMux pattern that matched this request, so
// the access line Correlate writes names the route rather than a truncated
// path. It is called by the handler package's per-route wrapper, which runs
// inside the mux and knows the pattern; outside the mux, where Correlate lives,
// r.Pattern is empty because the request the mux annotated is a copy the
// middleware chain never sees again. Calling it without Correlate upstream is
// harmless.
func SetRoutePattern(ctx context.Context, pattern string) {
	if l, ok := ctx.Value(routeLabelKey{}).(*routeLabel); ok && pattern != "" {
		l.set(pattern)
	}
}

// RoutePattern returns the pattern recorded by SetRoutePattern, or "".
func RoutePattern(ctx context.Context) string {
	if l, ok := ctx.Value(routeLabelKey{}).(*routeLabel); ok {
		return l.get()
	}
	return ""
}

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
		label := &routeLabel{}
		ctx = context.WithValue(ctx, routeLabelKey{}, label)
		w.Header().Set(HeaderCorrelationID, id)

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		served := r.WithContext(ctx)
		next.ServeHTTP(rec, served)
		elapsed := time.Since(start)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		// The matched route pattern, never the raw path: a note id in a log
		// message is unbounded cardinality and, for search queries, user
		// content. The pattern is the whole route — "POST /v1/notes/purge",
		// "GET /v1/captures/{id}/download" — where the old two-segment prefix
		// logged both of those as "/v1/notes" and "/v1/captures" and could not
		// tell a purge from a create or a download from a list. The pattern
		// carries the method, so there is no separate `method` field: with
		// both, `stats count() by method, route` read "GET GET /v1/notes".
		Log(ctx).Info("request",
			slog.String("route", routePattern(label, served)),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Int64("duration_ms", elapsed.Milliseconds()),
		)
	})
}

// routePattern is the recorded pattern, else the one the mux stamped on the
// request Correlate handed down (present when nothing between the two copied
// the request), else unmatchedRoute. Never any part of the path.
func routePattern(label *routeLabel, served *http.Request) string {
	if p := label.get(); p != "" {
		return p
	}
	if p := served.Pattern; p != "" {
		return p
	}
	return served.Method + " " + unmatchedRoute
}
