package handler

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// Per-IP limits for the two unauthenticated WebAuthn routes.
//
// Those are the only routes that reach the database without a verified token,
// which makes them the cheapest lever an anonymous caller has on this account's
// bill. The biometric spec asked for this limit and it was never built.
const (
	loginAttemptsPerWindow = 10
	loginWindow            = time.Minute
	// limiterCapacity bounds the map so a spray of forged X-Forwarded-For
	// values cannot turn the limiter itself into the memory leak.
	limiterCapacity = 4096
)

// ipLimiter is a fixed-window counter keyed by client address.
//
// It is deliberately in-process and therefore per-Lambda-container: it is a
// brake on one instance, not a distributed quota, and the stage-level throttle
// and WAF remain the account-wide control. An in-process brake that costs
// nothing is still the difference between an unauthenticated route answering a
// flood and refusing one.
type ipLimiter struct {
	mu      sync.Mutex
	windows map[string]*ipWindow
	limit   int
	window  time.Duration
	now     func() time.Time
}

type ipWindow struct {
	count int
	start time.Time
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{
		windows: make(map[string]*ipWindow),
		limit:   limit,
		window:  window,
		now:     time.Now,
	}
}

// allow records an attempt and reports whether it is within the limit, with the
// seconds until the window resets.
func (l *ipLimiter) allow(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= l.window {
		if len(l.windows) >= limiterCapacity {
			l.evictExpiredLocked(now)
		}
		l.windows[key] = &ipWindow{count: 1, start: now}
		return true, 0
	}
	w.count++
	if w.count > l.limit {
		retry := int((l.window - now.Sub(w.start)).Seconds())
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	return true, 0
}

func (l *ipLimiter) evictExpiredLocked(now time.Time) {
	for k, w := range l.windows {
		if now.Sub(w.start) >= l.window {
			delete(l.windows, k)
		}
	}
	if len(l.windows) < limiterCapacity {
		return
	}
	// Still full of live windows: the instance is genuinely under load. Start
	// over rather than grow without bound; the worst case is one forgiven
	// window, not an unbounded map.
	l.windows = make(map[string]*ipWindow)
}

// rateLimit refuses a caller that is over the per-IP limit.
func (l *ipLimiter) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		ok, retryAfter := l.allow(ip)
		if !ok {
			obs.Count(r.Context(), "RateLimited", map[string]string{"Route": routeOf(r)})
			httperr.TooManyRequests(w, r, "too many attempts; try again shortly", retryAfter)
			return
		}
		next(w, r)
	}
}

// clientIP returns the address the limit is keyed on.
//
// X-Forwarded-For is only trustworthy because this service is only reachable
// through API Gateway, which overwrites it. The left-most entry is the client
// as the gateway saw it. A direct deployment without that guarantee would have
// to key on RemoteAddr alone, which is why the fallback exists.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first := strings.TrimSpace(strings.Split(fwd, ",")[0])
		if first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
