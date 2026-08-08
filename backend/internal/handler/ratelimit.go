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
	// limiterCapacity bounds the map so a spray of distinct keys cannot turn the
	// limiter itself into the memory leak. At capacity the limiter refuses
	// untracked callers rather than clearing the table, so filling it is not a
	// way to forgive a window.
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
	if w, ok := l.windows[key]; ok && now.Sub(w.start) < l.window {
		w.count++
		if w.count > l.limit {
			retry := int((l.window - now.Sub(w.start)).Seconds())
			if retry < 1 {
				retry = 1
			}
			return false, retry
		}
		return true, 0
	} else if ok {
		// The key is already tracked and its window has run out. Reusing the
		// slot costs no capacity, so it never needs an eviction.
		w.count, w.start = 1, now
		return true, 0
	}

	if len(l.windows) >= limiterCapacity {
		l.evictExpiredLocked(now)
		if len(l.windows) >= limiterCapacity {
			// Still full of live windows: this instance is seeing more distinct
			// addresses inside one window than it is willing to track. Refuse
			// the untracked caller rather than emptying the table, which is what
			// let ~limiterCapacity attacker-chosen keys forgive every counter in
			// flight — including the attacker's own — and so turned the memory
			// bound into a way to buy unlimited attempts.
			return false, int(l.window.Seconds())
		}
	}
	l.windows[key] = &ipWindow{count: 1, start: now}
	return true, 0
}

// evictExpiredLocked drops windows that have run out. It never drops a live one:
// forgetting a counter that is still inside its window is forgiving it.
func (l *ipLimiter) evictExpiredLocked(now time.Time) {
	for k, w := range l.windows {
		if now.Sub(w.start) >= l.window {
			delete(l.windows, k)
		}
	}
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

// maxForwardedForEntries bounds how much of an X-Forwarded-For header is
// examined. The header is caller-supplied and unbounded in length, so the parse
// itself must not be something a caller can make expensive.
const maxForwardedForEntries = 8

// clientIP returns the address the limit is keyed on.
//
// RemoteAddr first, because it is the only value here the caller cannot choose.
// The Lambda adapter sets it from requestContext.http.sourceIp
// (aws-lambda-go-api-proxy, core/requestv2.go), which is the peer address API
// Gateway observed.
//
// X-Forwarded-For is NOT overwritten by API Gateway — the gateway *appends* the
// source IP to whatever the client sent. Keying on the left-most entry therefore
// keyed on a value the caller writes, and rotating it minted a fresh window for
// every request. Only the right-most entry is gateway-written, so that is the
// one consulted, and only as a fallback for a deployment that terminates
// somewhere other than the Lambda adapter and leaves RemoteAddr unusable.
func clientIP(r *http.Request) string {
	if host := addrHost(r.RemoteAddr); host != "" {
		return host
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if entry := rightmostForwarded(fwd); entry != "" {
			return entry
		}
	}
	return r.RemoteAddr
}

// addrHost extracts the host from a RemoteAddr, which may be host:port from a
// net/http listener or a bare address from the Lambda adapter.
func addrHost(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	if net.ParseIP(remoteAddr) != nil {
		return remoteAddr
	}
	return ""
}

// rightmostForwarded returns the last non-empty X-Forwarded-For entry, scanning
// from the right and giving up after maxForwardedForEntries so the work is
// bounded however long the header is.
func rightmostForwarded(fwd string) string {
	for i := 0; i < maxForwardedForEntries; i++ {
		comma := strings.LastIndexByte(fwd, ',')
		if entry := strings.TrimSpace(fwd[comma+1:]); entry != "" {
			return entry
		}
		if comma < 0 {
			return ""
		}
		fwd = fwd[:comma]
	}
	return ""
}
