// Package obs provides structured logging, request correlation, and CloudWatch
// metrics.
//
// v1 had none of this: 21 unstructured log.Printf calls, no request ids, and no
// way to follow one capture across transcribe, route, cleanup and append. It did
// have one good instinct — it never logged user content — and this package keeps
// that property enforceable rather than incidental. See Redact.
package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type correlationKey struct{}
type tenantKey struct{}

// HeaderCorrelationID is the request header a caller may supply to join its own
// trace to ours. It is regenerated if absent or implausible.
const HeaderCorrelationID = "X-Correlation-Id"

const maxCorrelationIDLen = 64

var (
	setupOnce sync.Once
	base      *slog.Logger
)

// Setup installs a JSON handler writing to stdout. Safe to call more than once;
// only the first call takes effect.
//
// Stdout is correct in Lambda, where the runtime forwards it to CloudWatch. A
// CLI should use SetupTo(os.Stderr, ...) instead, because there stdout carries
// the command's result and interleaving log lines into it makes `--json` output
// unparseable.
func Setup(level slog.Level) {
	SetupTo(os.Stdout, level)
}

// SetupTo installs a JSON handler writing to w.
func SetupTo(w io.Writer, level slog.Level) {
	setupOnce.Do(func() {
		base = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
		slog.SetDefault(base)
	})
}

// NewCorrelationID returns a fresh identifier.
func NewCorrelationID() string { return uuid.NewString() }

// SanitizeCorrelationID accepts a caller-supplied id only if it is short and
// printable, so a hostile header cannot inject newlines into the log stream.
func SanitizeCorrelationID(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxCorrelationIDLen {
		return "", false
	}
	for _, r := range v {
		if r < 0x20 || r > 0x7e {
			return "", false
		}
	}
	return v, true
}

// WithCorrelationID returns a context carrying id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the id stored in ctx, or "" if absent.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// WithTenant returns a context carrying the tenant id for logging.
//
// This is for log attribution only. Authorization decisions read auth.Identity,
// never this value.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenantID)
}

// Tenant returns the tenant id stored in ctx, or "" if absent.
func Tenant(ctx context.Context) string {
	id, _ := ctx.Value(tenantKey{}).(string)
	return id
}

// Log returns a logger pre-tagged with whatever correlation and tenant
// information the context carries.
func Log(ctx context.Context) *slog.Logger {
	l := base
	if l == nil {
		l = slog.Default()
	}
	if id := CorrelationID(ctx); id != "" {
		l = l.With(slog.String("correlation_id", id))
	}
	if t := Tenant(ctx); t != "" {
		l = l.With(slog.String("tenant_id", t))
	}
	return l
}

// Redact summarises a string without revealing it.
//
// Transcripts, note bodies, and audio must never reach a log line. When the size
// or shape of user content is genuinely diagnostic, log this instead of the
// content itself.
func Redact(s string) slog.Value {
	return slog.GroupValue(
		slog.Int("bytes", len(s)),
		slog.Int("words", len(strings.Fields(s))),
	)
}
