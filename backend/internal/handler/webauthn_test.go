package handler_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/vppillai/chintan/backend/internal/service"
)

func verifyBody() map[string]any {
	return map[string]any{
		"challenge_id":  "ch_1",
		"credential":    map[string]any{"id": "abc"},
		"refresh_token": "rt",
	}
}

func TestBiometricRoutesAreUnavailableWhenNotConfigured(t *testing.T) {
	h := newHarness(t, withoutWebAuthn())
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/auth/webauthn/login/options", nil},
		{http.MethodPost, "/v1/auth/webauthn/login", verifyBody()},
		{http.MethodPost, "/v1/auth/webauthn/register/options", nil},
		{http.MethodPost, "/v1/auth/webauthn/register", verifyBody()},
	} {
		w := h.do(t, tc.method, tc.path, "user1", tc.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", tc.method, tc.path, w.Code)
		}
		problemOf(t, w)
	}
}

// The two login routes are the only unauthenticated data routes on the API, so
// they are the cheapest lever an anonymous caller has on this account's bill.
// The biometric spec asked for this limit and it was never built.
func TestUnauthenticatedLoginRoutesAreRateLimitedPerIP(t *testing.T) {
	h := newHarness(t)

	// Callers are distinguished by the address they connect from, which the
	// Lambda adapter writes into RemoteAddr from requestContext.http.sourceIp.
	// This test used to distinguish them by X-Forwarded-For — the header API
	// Gateway *appends* to rather than replaces, so it is caller-supplied and
	// rotating it minted a fresh window per request. Both callers below present
	// the same forged header, so only the connecting address can separate them.
	const forged = "203.0.113.9"

	limited := false
	for i := 0; i < 30; i++ {
		h.remoteAddr = "198.51.100.7:41234"
		w := h.do(t, http.MethodPost, "/v1/auth/webauthn/login/options", "", nil,
			[2]string{"X-Forwarded-For", forged})
		if w.Code == http.StatusTooManyRequests {
			limited = true
			if w.Header().Get("Retry-After") == "" {
				t.Error("a 429 with no Retry-After leaves the client guessing")
			}
			problemOf(t, w)
			break
		}
	}
	if !limited {
		t.Fatal("the unauthenticated login route never rate limited")
	}

	// The limit is per address, so one flooding caller does not lock everybody
	// else out of biometric unlock.
	h.remoteAddr = "198.51.100.8:41234"
	w := h.do(t, http.MethodPost, "/v1/auth/webauthn/login/options", "", nil,
		[2]string{"X-Forwarded-For", forged})
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("a second address was refused because of the first")
	}
}

func TestBiometricLoginFailureIsAlways401(t *testing.T) {
	h := newHarness(t)

	// Every distinguishable cause has to produce one answer, or the endpoint is
	// an oracle for which credentials exist.
	for _, cause := range []error{
		service.ErrWebAuthnVerification,
		service.ErrWebAuthnChallengeNotFound,
		service.ErrWebAuthnSubMismatch,
	} {
		h.webauthn.finishLogErr = cause
		w := h.do(t, http.MethodPost, "/v1/auth/webauthn/login", "", verifyBody(),
			[2]string{"X-Forwarded-For", "192.0.2.77"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%v: status = %d, want 401", cause, w.Code)
		}
		p := problemOf(t, w)
		if p["detail"] != "biometric verification failed" {
			t.Errorf("%v: detail = %v; every cause must read the same", cause, p["detail"])
		}
	}
}

// The one biometric failure that must NOT read like the others. It is reached
// only after the assertion has verified, so it tells an attacker nothing — and
// without it the user meets an identical, permanent 401 with no way to act on
// it, because the vault was sealed by the retired KMS key and is now gone.
func TestAVaultNeedingReEnrolmentSaysSoRatherThanFailingOpaquely(t *testing.T) {
	h := newHarness(t)
	h.webauthn.finishLogErr = fmt.Errorf("%w: not a CVK1 blob", service.ErrWebAuthnReEnrolRequired)

	w := h.do(t, http.MethodPost, "/v1/auth/webauthn/login", "", verifyBody())
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	p := problemOf(t, w)
	if p["detail"] != "biometric unlock must be set up again on this device" {
		t.Fatalf("detail = %v; the user is given nothing to act on", p["detail"])
	}
}

func TestBiometricEnrollmentAndStatus(t *testing.T) {
	h := newHarness(t)

	t.Run("options", func(t *testing.T) {
		w := h.do(t, http.MethodPost, "/v1/auth/webauthn/register/options", "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var out struct {
			ChallengeID string `json:"challenge_id"`
		}
		decodeInto(t, w, &out)
		if out.ChallengeID == "" {
			t.Error("no challenge id")
		}
	})

	t.Run("enrollment is 204", func(t *testing.T) {
		w := h.do(t, http.MethodPost, "/v1/auth/webauthn/register", "user1", verifyBody())
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("a rejected authenticator is 400", func(t *testing.T) {
		h.webauthn.finishRegErr = service.ErrWebAuthnVerification
		w := h.do(t, http.MethodPost, "/v1/auth/webauthn/register", "user1", verifyBody())
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		h.webauthn.finishRegErr = nil
	})

	t.Run("status", func(t *testing.T) {
		h.webauthn.enrolled = true
		w := h.do(t, http.MethodGet, "/v1/auth/webauthn/status", "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var out map[string]bool
		decodeInto(t, w, &out)
		if !out["enrolled"] {
			t.Error("enrolled = false after enrollment")
		}
	})

	t.Run("disable is 204", func(t *testing.T) {
		w := h.do(t, http.MethodDelete, "/v1/auth/webauthn", "user1", nil)
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("management routes require auth", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/v1/auth/webauthn/status"},
			{http.MethodDelete, "/v1/auth/webauthn"},
			{http.MethodPost, "/v1/auth/webauthn/register/options"},
		} {
			w := h.do(t, tc.method, tc.path, "", nil)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", tc.method, tc.path, w.Code)
			}
		}
	})
}
