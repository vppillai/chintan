package provider

import (
	"errors"
	"fmt"
	"net/http"
)

// StatusError is a provider's HTTP rejection.
//
// It exists so a caller can tell three things apart that are otherwise one
// error string: a credential the provider will never accept, a throttle that
// resolves itself, and everything else. The distinction is not cosmetic. A 401
// or 403 means the key is revoked, expired or wrong, and every capture from
// here until somebody replaces it will fail identically — that is worth waking
// an operator for on the first occurrence. A 429 is ordinary rate limiting and
// is usually gone by the next attempt; alerting on one of those trains the
// operator to ignore the alert that matters.
//
// The body is deliberately not carried. A provider's error body can echo the
// prompt back, and the prompt is the user's transcript
// (scripts/check-log-hygiene.sh enforces that this stays true).
type StatusError struct {
	// Op is the call that was refused, in the words the log already used, so
	// Error() renders exactly the string these adapters produced before the type
	// existed.
	Op string
	// StatusCode is the HTTP status the provider answered with.
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("provider: %s: status %d", e.Op, e.StatusCode)
}

// statusOf recovers the provider status from an error that may be wrapped by
// the breaker, the pipeline, or both.
func statusOf(err error) (int, bool) {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode, true
	}
	return 0, false
}

// IsAuthRejection reports whether a provider refused the credential rather than
// the request.
//
// 401 and 403 are treated alike on purpose. Providers disagree about which one
// a revoked key produces — some answer 401 for a bad key and 403 for a key
// whose plan no longer covers the model — and from this side both mean the same
// thing: this instance's key will not work again until a human changes it.
func IsAuthRejection(err error) bool {
	code, ok := statusOf(err)
	return ok && (code == http.StatusUnauthorized || code == http.StatusForbidden)
}

// IsRateLimited reports whether a provider throttled the request.
//
// Only 429. A 503 is also transient but is an outage rather than a quota, and
// conflating them would put a provider's bad afternoon and this account's
// exhausted quota behind one alarm with one threshold.
func IsRateLimited(err error) bool {
	code, ok := statusOf(err)
	return ok && code == http.StatusTooManyRequests
}

// IsServerError reports whether a provider failed on its own side: any 5xx,
// including MiniMax's 529 "overloaded". These are the rejections a second
// attempt a moment later has a real chance of clearing, which is what makes
// them worth one retry where a 4xx (our request, our key, our prompt) is not.
func IsServerError(err error) bool {
	code, ok := statusOf(err)
	return ok && code >= 500
}
