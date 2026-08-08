package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// limited drives one request through the limiter and reports the status.
func limited(t *testing.T, h http.HandlerFunc, remoteAddr string, headers ...[2]string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/webauthn/login/options", nil)
	r.RemoteAddr = remoteAddr
	for _, kv := range headers {
		r.Header.Set(kv[0], kv[1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w.Code
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// One caller must not be able to mint a fresh window per request by rotating a
// header it writes itself.
//
// The limiter used to key on the LEFT-most X-Forwarded-For entry, justified by a
// comment claiming API Gateway "overwrites" the header. It does not — it appends
// the source IP to whatever the client sent — so the left-most entry is entirely
// caller-supplied and 20 rotated values bought 20 fresh windows. Each of those
// requests reaches a DynamoDB Query and a GetItem, which is the unauthenticated
// database-cost lever this limiter exists to close.
func TestRotatingForwardedForDoesNotMintFreshWindows(t *testing.T) {
	l := newIPLimiter(loginAttemptsPerWindow, loginWindow)
	h := l.rateLimit(okHandler)

	refused := 0
	for i := 0; i < 20; i++ {
		code := limited(t, h, "198.51.100.7:41234",
			[2]string{"X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i)})
		if code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatalf("20 requests from one address were all allowed by rotating X-Forwarded-For; the %d/%s limit never fired",
			loginAttemptsPerWindow, loginWindow)
	}
	if want := 20 - loginAttemptsPerWindow; refused != want {
		t.Errorf("refused %d of 20, want %d: the limit is keyed on something other than the connecting address", refused, want)
	}
}

// The limit is per address, so one flooding caller must not lock everybody else
// out of biometric unlock — including when they all present the same
// X-Forwarded-For.
func TestTwoAddressesGetIndependentWindows(t *testing.T) {
	l := newIPLimiter(loginAttemptsPerWindow, loginWindow)
	h := l.rateLimit(okHandler)
	shared := [2]string{"X-Forwarded-For", "203.0.113.9"}

	flooded := false
	for i := 0; i < 30; i++ {
		if limited(t, h, "198.51.100.7:41234", shared) == http.StatusTooManyRequests {
			flooded = true
			break
		}
	}
	if !flooded {
		t.Fatal("the flooding address was never refused")
	}
	if code := limited(t, h, "198.51.100.8:41234", shared); code == http.StatusTooManyRequests {
		t.Fatal("a second address was refused because of the first")
	}
}

// A long forged header must not become a parsing lever either, and the entry
// that is consulted has to be the one the gateway appended (right-most), not the
// one the caller prepended.
func TestForwardedForIsOnlyConsultedFromTheRightAndOnlyWithoutARemoteAddr(t *testing.T) {
	forged := strings.Repeat("203.0.113.1, ", 500)

	// With a usable RemoteAddr, X-Forwarded-For does not participate at all.
	if got := clientIP(request(t, "198.51.100.7:41234", forged+"192.0.2.9")); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want the connecting address 198.51.100.7", got)
	}

	// Without one, the right-most entry — the only one a gateway writes — wins.
	if got := clientIP(request(t, "", "192.0.2.1, 198.51.100.2, 203.0.113.3")); got != "203.0.113.3" {
		t.Errorf("clientIP = %q, want the right-most entry 203.0.113.3", got)
	}

	// The Lambda adapter writes requestContext.http.sourceIp, which carries no
	// port, so a bare address still has to be recognised as the peer.
	if got := clientIP(request(t, "198.51.100.7", "203.0.113.9")); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want the bare RemoteAddr 198.51.100.7", got)
	}

	// A header made of nothing but separators resolves to no forwarded entry
	// rather than an empty key shared by every caller.
	if got := clientIP(request(t, "", strings.Repeat(",", 5000))); got != "" {
		t.Errorf("clientIP = %q, want no key from a separator-only header", got)
	}
}

func request(t *testing.T, remoteAddr, forwarded string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/webauthn/login/options", nil)
	r.RemoteAddr = remoteAddr
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return r
}

// Filling the table must never forgive a window that is already over the limit.
//
// The eviction path used to reset the whole map once it held limiterCapacity
// live windows, so ~4096 attacker-chosen keys wiped every counter in flight —
// including the attacker's own. That turns the memory bound into a way to buy
// unlimited attempts.
func TestFillingTheTableDoesNotForgiveAnOffendersWindow(t *testing.T) {
	l := newIPLimiter(loginAttemptsPerWindow, loginWindow)
	now := time.Now()
	l.now = func() time.Time { return now }

	const offender = "203.0.113.4"
	for i := 0; i <= loginAttemptsPerWindow; i++ {
		l.allow(offender)
	}
	if ok, _ := l.allow(offender); ok {
		t.Fatal("the offender was not over the limit to begin with")
	}

	// Spray far more distinct keys than the table holds, all inside the window
	// so none of them is evictable.
	for i := 0; i < limiterCapacity*2; i++ {
		l.allow(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	if len(l.windows) > limiterCapacity {
		t.Errorf("limiter holds %d windows, over the %d cap", len(l.windows), limiterCapacity)
	}
	if ok, retry := l.allow(offender); ok {
		t.Fatalf("the offender was forgiven by the eviction path (retry=%d); ~%d chosen keys buy an unlimited window",
			retry, limiterCapacity)
	}
}

// Expired windows are still reclaimed, or the table fills with dead entries and
// every new caller is refused forever.
func TestExpiredWindowsAreReclaimed(t *testing.T) {
	l := newIPLimiter(loginAttemptsPerWindow, loginWindow)
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < limiterCapacity; i++ {
		l.allow(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	now = now.Add(loginWindow + time.Second)
	if ok, _ := l.allow("203.0.113.44"); !ok {
		t.Fatal("a new caller was refused although every tracked window had expired")
	}
	if len(l.windows) > limiterCapacity {
		t.Errorf("limiter holds %d windows, over the %d cap", len(l.windows), limiterCapacity)
	}
}

// A caller whose window has run out gets a fresh one, rather than being held
// over the limit forever by the table entry it left behind.
func TestAWindowResetsWhenItRunsOut(t *testing.T) {
	l := newIPLimiter(loginAttemptsPerWindow, loginWindow)
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i <= loginAttemptsPerWindow; i++ {
		l.allow("203.0.113.4")
	}
	if ok, retry := l.allow("203.0.113.4"); ok || retry < 1 {
		t.Fatalf("allow = (%v, %d), want a refusal with a positive Retry-After", ok, retry)
	}
	now = now.Add(loginWindow)
	if ok, _ := l.allow("203.0.113.4"); !ok {
		t.Fatal("the window never reset")
	}
}
