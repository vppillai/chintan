package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/vppillai/chintan/backend/internal/obs"
)

// retrying runs call up to attempts times, waiting between a refusal and the
// next try, and returns the last error when every try is refused.
//
// What is retried: a 429, any 5xx (MiniMax's 529 "overloaded" included) and
// a transport fault — a connection refused or reset, a connection the
// provider closed before answering, a body cut short (retryStatus). A 4xx
// other than 429 is this request or this key and is returned at once; so is
// anything that is not an HTTP failure — a reply that would not decode, a
// name that does not resolve, a certificate that does not verify — because
// a moment later changes none of those.
//
// The budget is the context's. Every wait and every retried call run under
// the same ctx the stage handed in, so a retry can never push a stage past
// its deadline (pipeline-deadlines.md): the wait and a request as long as
// the refused one have to end before the deadline, or the refusal is
// returned as it stands — a retried call cut by the deadline would reach
// the stage as a timeout, which is retryable, where the refusal is a
// verdict. A context cancelled during a wait ends the wait. The wait is
// the provider's Retry-After when it sent one and it is within
// ProviderRetryMaxRetryAfter, else full jitter under ProviderRetryMaxWait.
//
// Each retry counts ProviderRetried{Provider,Status}; the caller's own
// classification of the final error (ProviderRateLimited, the stage's
// verdict) is unchanged because the final error is the last refusal itself,
// wrapped with the attempt count once more than one was made.
func retrying(ctx context.Context, providerName string, attempts int, call func(ctx context.Context) error) error {
	for attempt := 1; ; attempt++ {
		started := time.Now()
		err := call(ctx)
		if err == nil {
			return nil
		}
		took := time.Since(started)
		status, transient := retryStatus(err)
		wait, waitable := retryWait(err, attempt)
		if !transient || !waitable || attempt >= attempts || ctx.Err() != nil {
			return afterAttempts(err, attempt)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait+took {
			return afterAttempts(err, attempt)
		}
		obs.Log(ctx).Warn("provider refused the call; retrying",
			slog.String("provider", providerName),
			slog.String("status", status),
			slog.Int("attempt", attempt),
			slog.Int64("wait_ms", wait.Milliseconds()))
		obs.CountWithRollup(ctx, "ProviderRetried", map[string]string{"Provider": providerName, "Status": retryClass(status)})
		if serr := retrySleep(ctx, wait); serr != nil {
			return fmt.Errorf("%w; retry interrupted: %w", err, serr)
		}
	}
}

// afterAttempts is the refusal the caller gets: as it was after one call, and
// with the count once more were made. %w keeps the StatusError reachable.
func afterAttempts(err error, attempt int) error {
	if attempt <= 1 {
		return err
	}
	return fmt.Errorf("%w (after %d attempts)", err, attempt)
}

// retryStatus names the refusal for the log and says whether one more try
// has a real chance: the HTTP status for a 429 or 5xx, "transport" for a
// request that never got an answer from a provider that was reached.
//
// The transport class is deliberately narrow — a *net.OpError (dial
// refused, reset by peer, read reset), ECONNRESET itself, a body cut short
// and a connection closed before the response line (the url.Error whose
// cause is io.EOF) — not every url.Error: a name that does not resolve
// (*net.DNSError) and a certificate that does not verify are this
// configuration, not the provider's afternoon, and are returned at once. A
// context ending inside the transport is never seen here as transient
// because the loop checks ctx.Err() first.
func retryStatus(err error) (string, bool) {
	if code, ok := statusOf(err); ok {
		if code == http.StatusTooManyRequests || code >= 500 {
			return strconv.Itoa(code), true
		}
		return "", false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "", false
	}
	var op *net.OpError
	var ue *url.Error
	switch {
	case errors.As(err, &op),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.As(err, &ue) && errors.Is(ue.Err, io.EOF):
		return "transport", true
	}
	return "", false
}

// retryClass is the Status dimension of ProviderRetried: a fixed set of
// three, since a dimension value is a billed identity. The exact status is
// in the WARN line beside it.
func retryClass(status string) string {
	switch status {
	case "429", "transport":
		return status
	default:
		return "5xx"
	}
}

// retryWait is how long to wait before the numbered attempt's successor, and
// whether to wait at all: the provider's Retry-After when it sent one
// within ProviderRetryMaxRetryAfter, none when it asked for longer, else
// full jitter — uniform in [0, min(ProviderRetryMaxWait, ProviderRetryBaseWait·2^attempt)).
func retryWait(err error, attempt int) (time.Duration, bool) {
	var se *StatusError
	if errors.As(err, &se) && se.RetryAfter > 0 {
		return se.RetryAfter, se.RetryAfter <= ProviderRetryMaxRetryAfter
	}
	ceiling := min(ProviderRetryMaxWait, ProviderRetryBaseWait<<attempt)
	return time.Duration(rand.Int64N(int64(ceiling))), true
}

// retrySleep waits d or until ctx ends. A variable so a test can record the
// waits instead of taking them.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseRetryAfter reads a Retry-After header, delay-seconds or an HTTP date,
// as a duration from now; zero when absent, unreadable or already past.
func parseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		return max(0, time.Duration(secs)*time.Second)
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(0, at.Sub(now))
	}
	return 0
}
