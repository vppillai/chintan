// Package httperr writes the one error envelope this API has.
//
// It is RFC 9457 `application/problem+json`, and it is the only shape a non-2xx
// response takes. Nothing writes a bare `{"error":…}`, text/plain from
// http.Error, "404 page not found" from http.NotFound, or an empty-body bare
// status: a client that has to parse several shapes to find out what went wrong
// will get one of them wrong.
//
// Two rules hold here and are the reason the package exists:
//
//  1. Infrastructure error text is logged, never serialised. Putting
//     err.Error() on the wire ships wrapped DynamoDB and S3 messages —
//     including table and bucket names — to anyone who can provoke a failure.
//     Detail is a sentence written for a person, chosen by the caller.
//
//  2. Every 500 logs. An InternalServerError that discards its error parameter
//     leaves the most serious class of failure with no log line at all. Here
//     the error is logged with the request's correlation id, which is also
//     returned to the client so a user can quote it.
//
// The package deliberately imports no repository or service package. Mapping a
// domain error to a status code is the handler's job and happens in exactly one
// place; a layering inversion here — httperr reaching into repository to
// recognise ErrNotFound — puts that decision in two.
package httperr

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/obs"
)

// ContentType is the media type of every error response.
const ContentType = "application/problem+json"

// Problem is an RFC 9457 problem detail.
type Problem struct {
	// Type is a URI identifying the problem kind. "about:blank" means "the
	// status code is the whole story".
	Type string `json:"type"`
	// Title is a short, stable summary. It does not change between occurrences.
	Title string `json:"title"`
	// Status repeats the HTTP status code, so a problem stored or forwarded
	// away from its response carries it.
	Status int `json:"status"`
	// Detail is safe to show a user. It never contains infrastructure text.
	Detail string `json:"detail,omitempty"`
	// Instance identifies this occurrence — the request path.
	Instance string `json:"instance,omitempty"`
	// CorrelationID matches the X-Correlation-Id response header and the server
	// logs, so a user can quote one string and an operator can find the trace.
	CorrelationID string `json:"correlation_id,omitempty"`
	// CurrentVersion is present on 409 so the client can reconcile without a
	// second round trip.
	CurrentVersion *int64 `json:"current_version,omitempty"`
}

// TypeURI is the base for problem type URIs. It is a documentation anchor, not
// a live endpoint, which RFC 9457 explicitly permits.
const TypeURI = "https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#"

// TypeSpendCapped identifies the daily provider spend cap.
//
// It is the one 429 this API produces, and a client must not retry it; the
// gateway's throttling 429 is the other one a client meets, and backing off
// does fix that. RFC 9457 makes `type` the machine-readable discriminator
// precisely so a client does not have to read the title, and a client that
// reads the title is one rewording away from retrying a request that can never
// succeed.
//
// The frontend matches this exact string. Changing it changes the frontend's
// behaviour, and the contract fixtures are what make that visible.
const TypeSpendCapped = TypeURI + "spend-capped"

// TypeRetryable identifies a 503 the client may simply repeat.
//
// It is for an operation that writes to more than one place with no
// transaction across them — moving a recording writes two note bodies — and
// that failed part-way and rolled back. Nothing changed, so repeating the
// request is the right response, and `type` says so where the status alone
// could also mean "this instance is not configured for that".
const TypeRetryable = TypeURI + "retryable"

// Write emits a problem response. It is the only writer of a non-2xx body.
func Write(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if r != nil {
		if p.Instance == "" {
			p.Instance = r.URL.Path
		}
		if p.CorrelationID == "" {
			p.CorrelationID = obs.CorrelationID(r.Context())
		}
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// New builds a problem for a status and detail.
func New(status int, detail string) Problem {
	return Problem{Status: status, Title: http.StatusText(status), Detail: detail}
}

// BadRequest writes 400.
func BadRequest(w http.ResponseWriter, r *http.Request, detail string) {
	Write(w, r, New(http.StatusBadRequest, detail))
}

// Unauthorized writes 401.
//
// The detail is deliberately the same for every cause. A precise message —
// "token expired" versus "wrong audience" — is a probing oracle; the specific
// claim that failed goes to the log.
func Unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	Write(w, r, New(http.StatusUnauthorized, detail))
}

// NotFound writes 404.
//
// A resource belonging to another tenant returns this, never 403: a 403 would
// confirm the identifier exists.
func NotFound(w http.ResponseWriter, r *http.Request, detail string) {
	Write(w, r, New(http.StatusNotFound, detail))
}

// MethodNotAllowed writes 405 with the Allow header RFC 9110 requires.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	if allow != "" {
		w.Header().Set("Allow", allow)
	}
	Write(w, r, New(http.StatusMethodNotAllowed, "this resource does not support that method"))
}

// Conflict writes 409, carrying the current version when there is one.
func Conflict(w http.ResponseWriter, r *http.Request, detail string, currentVersion *int64) {
	p := New(http.StatusConflict, detail)
	p.CurrentVersion = currentVersion
	Write(w, r, p)
}

// PayloadTooLarge writes 413.
func PayloadTooLarge(w http.ResponseWriter, r *http.Request, detail string) {
	Write(w, r, New(http.StatusRequestEntityTooLarge, detail))
}

// ServiceUnavailable writes 503, for a feature this instance is not configured
// for and for a dependency that is not answering.
func ServiceUnavailable(w http.ResponseWriter, r *http.Request, detail string) {
	Write(w, r, New(http.StatusServiceUnavailable, detail))
}

// InternalServerError writes 500 and logs err with the request's correlation
// id.
//
// The error never reaches the client. The correlation id does, so a user can
// report "it failed, here is the id" and the log line is one search away.
func InternalServerError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	id := obs.CorrelationID(ctx)

	attrs := []any{slog.String("path", r.URL.Path), slog.String("method", r.Method)}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	obs.Log(ctx).Error("request failed", attrs...)

	Write(w, r, Problem{
		Status:        http.StatusInternalServerError,
		Title:         http.StatusText(http.StatusInternalServerError),
		Detail:        "the request could not be completed",
		CorrelationID: id,
	})
}

// Retryable writes 503 with TypeRetryable and a Retry-After, for an operation
// that failed part-way, rolled back, and can be repeated as it was.
func Retryable(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("Retry-After", "1")
	p := New(http.StatusServiceUnavailable, detail)
	p.Type = TypeRetryable
	Write(w, r, p)
}
