// Package logging provides structured JSON logging with correlation IDs.
//
// §9.2 and §Phase 0 impose a hard constraint that shapes this whole package:
// **no transcript content, audio, or PII reaches CloudWatch.** Log IDs and
// metrics only.
//
// Voice recordings are among the most sensitive content categories a product can
// hold, and the usual way transcript text ends up in a log is not a deliberate
// log line — it is an error being wrapped with the value that caused it, or a
// struct being dumped at debug level. So the API here does not accept arbitrary
// values: attributes go through helpers that either name a safe scalar or
// deliberately redact. Anything wanting to log content has to reach for
// Redacted(), which is a visible choice in a diff.
//
// The cost dimension matters too: CloudWatch ingestion is $0.50/GB and retention
// is explicit at 14 days (§10.1). Logging transcripts would be both a privacy
// breach and the largest line item on the bill.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// contextKey is unexported so no other package can collide with these keys.
type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	tenantIDKey      contextKey = "tenant_id"
)

// redactedPlaceholder is what stands in for content that must not be logged. A
// fixed string rather than an empty value, so a redaction is visibly a
// redaction and not mistaken for missing data.
const redactedPlaceholder = "[redacted]"

// New returns the process logger, writing JSON to stdout for CloudWatch.
//
// Level comes from LOG_LEVEL. Debug is available but deliberately does not
// unlock content logging: there is no level at which transcript text becomes
// loggable, because the retention window and the privacy posture do not change
// with verbosity.
func New() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		// AddSource is off: it inflates every line for information a stack
		// trace already carries on the paths that need one, and log volume is
		// a direct cost (§10.1).
		AddSource: false,
	})
	return slog.New(handler)
}

// WithCorrelationID attaches a correlation ID to a context.
//
// One ID follows a request through the sync API, and — because an upload
// triggers an S3 event rather than a direct call — is carried onto the object's
// metadata so the worker's log lines join up with the request that caused them.
// Without that hand-off, a pipeline failure is traceable only by timestamp
// proximity.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID reads the correlation ID, or "" if absent.
func CorrelationID(ctx context.Context) string {
	v, _ := ctx.Value(correlationIDKey).(string)
	return v
}

// WithTenantID attaches the tenant to a context for logging.
//
// The tenant ID is an identifier, not content, so it is safe to log — and it is
// necessary: a cross-tenant access attempt is an audit event at WARN (§9.1), and
// an unattributed warning is not actionable.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// TenantID reads the tenant from a context, or "" if absent.
func TenantID(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

// FromContext returns a logger carrying whatever correlation and tenant context
// is present, so call sites do not have to remember to attach them.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	l := base
	if id := CorrelationID(ctx); id != "" {
		l = l.With(slog.String("correlation_id", id))
	}
	if t := TenantID(ctx); t != "" {
		l = l.With(slog.String("tenant_id", t))
	}
	return l
}

// Redacted returns a log attribute that records a field was present without
// recording what it held.
//
// Use this for anything derived from user speech: transcript text, item bodies,
// thread titles, search queries, Telegram message text. The length is kept
// because "the cleanup patch was rejected on a 4,812-character block" is
// diagnostic and the characters themselves are not.
//
// If you are about to log content and reaching past this function, that is the
// moment §9.2 is about.
func Redacted(key string, value string) slog.Attr {
	return slog.Group(key,
		slog.String("value", redactedPlaceholder),
		slog.Int("length", len(value)),
	)
}

// ErrorAttr logs an error without its message when the message might carry
// content.
//
// Provider errors are the specific risk: several APIs echo the offending input
// back in an error body, so wrapping one and logging it verbatim is how
// transcript text reaches a log without anyone writing a line that logs
// transcript text. Where an error is known to be content-free — a validation
// failure, a missing config key — log it directly with slog.Any; this helper is
// for errors that crossed a provider boundary.
func ErrorAttr(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	return slog.Group("error",
		slog.String("type", errorType(err)),
		slog.String("message", redactedPlaceholder),
	)
}

func errorType(err error) string {
	// The concrete type name is content-free and is usually the whole of what
	// makes an error class actionable.
	t := strings.TrimPrefix(strings.TrimPrefix(
		typeName(err), "*"), "errors.")
	if t == "" {
		return "error"
	}
	return t
}
