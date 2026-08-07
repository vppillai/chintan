package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthExtractsSubFromBearer(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"sub": "user-123"})
	token := "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	var got string
	h := Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := GetUserID(r.Context())
		if !ok {
			t.Fatal("expected userID in context")
		}
		got = id
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got != "user-123" {
		t.Fatalf("userID=%q", got)
	}
}

func TestAuthRejectsMissingCredentials(t *testing.T) {
	h := Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
