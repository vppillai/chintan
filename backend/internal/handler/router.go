package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// APIPrefix is the one place the version lives. Hardcoding "/v1" into more than
// one path pattern makes moving the version a hunt for every copy.
const APIPrefix = "/v1"

// Deps is everything the HTTP surface needs.
//
// It is a struct rather than a parameter list because the list had already
// reached six positional arguments and was about to reach twelve; a caller
// swapping two same-typed pointers would have compiled and misbehaved.
//
// Nil is meaningful for two fields and nothing else:
//   - Spend nil disables the pre-flight budget check; the breaker still enforces.
//   - Store nil disables idempotent replay, which then answers 503 rather than
//     silently doing the work twice.
type Deps struct {
	Notes     *service.NotesService
	Settings  *service.SettingsService
	Captures  *service.CaptureService
	Search    *service.SearchService
	Tags      *service.TagsService
	Export    *service.ExportService
	Readiness *service.ReadinessService
	Spend     SpendGate
	// Usage answers GET /v1/usage from the per-tenant rows the worker's
	// breaker writes. Nil answers 503, like the other optional services.
	Usage usage.Reader

	// Store backs idempotent replay. It is the raw store rather than a service
	// because idempotency is a property of the request, not of any one domain.
	Store repository.Store

	// Verifier verifies bearer tokens. A nil verifier fails closed.
	Verifier auth.Verifier

	AllowedOrigin string

	// SpendCapMicros is the instance-wide daily provider budget, reported by
	// GET /v1/settings so the UI can show the ceiling that is actually
	// enforced. It is the template's DailySpendCapMicros; nothing a tenant
	// sends changes it.
	SpendCapMicros int64
}

// SpendGate reports whether the instance has already spent its daily budget.
type SpendGate interface {
	Capped(ctx context.Context) (bool, error)
}

type router struct {
	Deps
	authenticated func(http.Handler) http.Handler
	mux           *http.ServeMux
	// bodyLimits maps a registered pattern to its request cap, so the
	// idempotency wrapper reads a body under the same limit the handler would.
	bodyLimits map[string]int64
}

// New builds the HTTP surface.
func New(deps Deps) http.Handler {
	if deps.AllowedOrigin == "" {
		deps.AllowedOrigin = os.Getenv("ALLOWED_ORIGIN")
	}

	rt := &router{
		Deps:          deps,
		authenticated: middleware.Auth(deps.Verifier),
		mux:           http.NewServeMux(),
		bodyLimits:    map[string]int64{},
	}
	rt.routes()

	// Correlate is outermost so every response — including one produced by the
	// 404 and 405 fallbacks below — carries an id a user can quote.
	return obs.Correlate(middleware.CORS(deps.AllowedOrigin)(problemFallback(rt.mux)))
}

// routeOption configures one registration.
type routeOption func(*routeConfig)

type routeConfig struct {
	public     bool
	idempotent bool
	bodyLimit  int64
}

// public marks a route as reachable without a bearer token. There are exactly
// two: liveness and readiness. Sign-in itself happens against Cognito, never
// against this API.
func public() routeOption { return func(c *routeConfig) { c.public = true } }

// idempotent honours Idempotency-Key on this route.
func idempotent() routeOption { return func(c *routeConfig) { c.idempotent = true } }

// body sets this route's request body cap.
func body(limit int64) routeOption { return func(c *routeConfig) { c.bodyLimit = limit } }

// handle registers one method+path pattern.
//
// Every route goes through here, which is what makes the cross-cutting concerns
// uniform: there is no route without metrics, and no authenticated route where
// somebody forgot the middleware.
func (rt *router) handle(pattern string, h http.HandlerFunc, opts ...routeOption) {
	cfg := routeConfig{bodyLimit: MaxSmallRequestBytes}
	for _, opt := range opts {
		opt(&cfg)
	}
	rt.bodyLimits[pattern] = cfg.bodyLimit

	wrapped := h
	if cfg.idempotent {
		wrapped = rt.idempotent(wrapped)
	}
	var final http.Handler = wrapped
	if !cfg.public {
		final = rt.authenticated(final)
	}
	rt.mux.Handle(pattern, instrument(pattern, final))
}

// bodyLimit returns the cap registered for the route serving r.
func (rt *router) bodyLimit(r *http.Request) int64 {
	if limit, ok := rt.bodyLimits[routeOf(r)]; ok {
		return limit
	}
	return MaxSmallRequestBytes
}

// problemFallback converts net/http's own 404 and 405 responses into the one
// error envelope.
//
// ServeMux produces both by itself — and produces the Allow header on 405,
// which RFC 9110 requires. Rewriting the body here keeps that behaviour and
// stops the API having a second error shape for exactly the two statuses a
// client is most likely to meet by accident.
func problemFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&fallbackWriter{ResponseWriter: w, request: r}, r)
	})
}

type fallbackWriter struct {
	http.ResponseWriter
	request  *http.Request
	rewrote  bool
	finished bool
}

func (f *fallbackWriter) WriteHeader(status int) {
	if !f.finished && isMuxDefault(status, f.Header().Get("Content-Type")) {
		f.rewriteAsProblem(status)
		return
	}
	f.finished = true
	f.ResponseWriter.WriteHeader(status)
}

func (f *fallbackWriter) Write(b []byte) (int, error) {
	if f.rewrote {
		// The default handler's plain-text body is dropped; ours is already out.
		return len(b), nil
	}
	if !f.finished {
		f.finished = true
	}
	return f.ResponseWriter.Write(b)
}

func (f *fallbackWriter) Flush() {
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// isMuxDefault recognises a response written by http.Error rather than by one of
// our handlers. Ours set problem+json before writing the header.
func isMuxDefault(status int, contentType string) bool {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return false
	}
	return contentType == "" || strings.HasPrefix(contentType, "text/plain")
}

func (f *fallbackWriter) rewriteAsProblem(status int) {
	f.rewrote = true
	f.finished = true

	detail := "no route matches this path"
	if status == http.StatusMethodNotAllowed {
		detail = "this resource does not support that method"
	}
	p := httperr.New(status, detail)
	p.Type = "about:blank"
	p.Instance = f.request.URL.Path
	p.CorrelationID = obs.CorrelationID(f.request.Context())

	encoded, err := json.Marshal(p)
	if err != nil {
		f.ResponseWriter.WriteHeader(status)
		return
	}
	// Allow, set by ServeMux before this call, is deliberately left in place.
	f.Header().Del("X-Content-Type-Options")
	f.Header().Set("Content-Type", httperr.ContentType)
	f.ResponseWriter.WriteHeader(status)
	_, _ = f.ResponseWriter.Write(append(encoded, '\n'))
}
