package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
// a transport fault — a connection reset, a closed idle connection, a body
// cut short. A 4xx other than 429 is this request or this key and is
// returned at once; so is anything that is not an HTTP failure, such as a
// reply that would not decode.
//
// The budget is the context's. Every wait and every retried call run under
// the same ctx the stage handed in, so a retry can never push a stage past
// its deadline (pipeline-deadlines.md): a wait that would not end before the
// deadline is not taken and the refusal is returned as it stands, and a
// context cancelled during a wait ends the wait. The wait is the provider's
// Retry-After when it sent one, else full jitter under ProviderRetryMaxWait.
//
// Each retry counts ProviderRetried{Provider,Status}; the caller's own
// classification of the final error (ProviderRateLimited, the stage's
// verdict) is unchanged because the final error is the last refusal itself.
func retrying(ctx context.Context, providerName string, attempts int, call func(ctx context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := call(ctx)
		if err == nil {
			return nil
		}
		status, transient := retryStatus(err)
		if !transient || attempt >= attempts || ctx.Err() != nil {
			return err
		}
		wait := retryWait(err, attempt)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
			return err
		}
		obs.Log(ctx).Warn("provider refused the call; retrying",
			slog.String("provider", providerName),
			slog.String("status", status),
			slog.Int("attempt", attempt),
			slog.Int64("wait_ms", wait.Milliseconds()))
		obs.CountWithRollup(ctx, "ProviderRetried", map[string]string{"Provider": providerName, "Status": status})
		if serr := retrySleep(ctx, wait); serr != nil {
			return fmt.Errorf("%w; retry interrupted: %w", err, serr)
		}
	}
}

// retryStatus names the refusal for the metric and says whether one more try
// has a real chance: the HTTP status for a 429 or 5xx, "transport" for a
// request that never got an answer.
func retryStatus(err error) (string, bool) {
	if code, ok := statusOf(err); ok {
		if code == http.StatusTooManyRequests || code >= 500 {
			return strconv.Itoa(code), true
		}
		return "", false
	}
	var ue *url.Error
	if errors.As(err, &ue) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return "transport", true
	}
	return "", false
}

// retryWait is how long to wait before the numbered attempt's successor: the
// provider's Retry-After when it sent one, else full jitter — uniform in
// [0, min(ProviderRetryMaxWait, ProviderRetryBaseWait·2^attempt)).
func retryWait(err error, attempt int) time.Duration {
	var se *StatusError
	if errors.As(err, &se) && se.RetryAfter > 0 {
		return se.RetryAfter
	}
	ceiling := min(ProviderRetryMaxWait, ProviderRetryBaseWait<<attempt)
	return time.Duration(rand.Int64N(int64(ceiling)))
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
