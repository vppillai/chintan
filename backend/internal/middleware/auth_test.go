package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vppillai/chintan/backend/internal/auth"
)

type stubVerifier struct {
	id      auth.Identity
	err     error
	sawRaw  string
	callCnt int
}

func (s *stubVerifier) Verify(_ context.Context, raw string) (auth.Identity, error) {
	s.callCnt++
	s.sawRaw = raw
	if s.err != nil {
		return auth.Identity{}, s.err
	}
	return s.id, nil
}

func handlerCapturing(seen *auth.Identity, ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		*seen, _ = auth.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
}

// The v1 build trusted X-User-ID over the verified token, so any authenticated
// caller could act as any other user. This asserts the header is inert.
func TestAuthIgnoresUserIDHeader(t *testing.T) {
	v := &stubVerifier{id: auth.Identity{UserID: "real-owner", TenantID: "real-owner"}}
	var seen auth.Identity
	var ran bool

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("X-User-ID", "someone-else")
	w := httptest.NewRecorder()
	Auth(v)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)

	if !ran {
		t.Fatalf("handler did not run, status=%d", w.Code)
	}
	if seen.UserID != "real-owner" || seen.TenantID != "real-owner" {
		t.Fatalf("header overrode the verified identity: %+v", seen)
	}
}

// Without a token, the header must not be usable as a fallback either.
func TestAuthRejectsUserIDHeaderAlone(t *testing.T) {
	v := &stubVerifier{id: auth.Identity{UserID: "unused"}}
	var seen auth.Identity
	var ran bool

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("X-User-ID", "attacker")
	w := httptest.NewRecorder()
	Auth(v)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)

	if ran {
		t.Fatal("handler ran without a bearer token")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
	if v.callCnt != 0 {
		t.Fatal("verifier should not be consulted without a bearer token")
	}
}

func TestAuthAcceptsVerifiedToken(t *testing.T) {
	v := &stubVerifier{id: auth.Identity{UserID: "user-123", TenantID: "user-123"}}
	var seen auth.Identity
	var ran bool

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "Bearer the-token")
	w := httptest.NewRecorder()
	Auth(v)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if seen.UserID != "user-123" {
		t.Fatalf("identity=%+v", seen)
	}
	if v.sawRaw != "the-token" {
		t.Fatalf("verifier received %q", v.sawRaw)
	}
}

func TestAuthRejects(t *testing.T) {
	cases := []struct {
		name   string
		header string
		verr   error
	}{
		{"no authorization header", "", nil},
		{"empty bearer", "Bearer ", nil},
		{"wrong scheme", "Basic abcdef", nil},
		{"bare token, no scheme", "just-a-token", nil},
		{"verifier rejects", "Bearer bad", errors.New("boom")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &stubVerifier{id: auth.Identity{UserID: "should-not-be-used"}, err: tc.verr}
			var seen auth.Identity
			ran := false

			req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			Auth(v)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)

			if ran {
				t.Fatal("handler must not run")
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}

// Bearer is case-insensitive per RFC 7235.
func TestAuthAcceptsLowercaseScheme(t *testing.T) {
	v := &stubVerifier{id: auth.Identity{UserID: "u"}}
	var seen auth.Identity
	ran := false
	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "bearer tok")
	w := httptest.NewRecorder()
	Auth(v)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)
	if !ran {
		t.Fatalf("status=%d", w.Code)
	}
}

// A misconfigured deployment must reject requests, not admit them.
func TestAuthFailsClosedWithNilVerifier(t *testing.T) {
	var seen auth.Identity
	ran := false
	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	Auth(nil)(handlerCapturing(&seen, &ran)).ServeHTTP(w, req)

	if ran {
		t.Fatal("handler ran with no verifier configured")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestGetUserIDReportsAbsence(t *testing.T) {
	if _, ok := GetUserID(context.Background()); ok {
		t.Fatal("expected no user id")
	}
	ctx := WithUserID(context.Background(), "u1")
	got, ok := GetUserID(ctx)
	if !ok || got != "u1" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}
