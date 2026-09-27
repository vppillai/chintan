package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/vppillai/chintan/backend/internal/model"
)

// A real send against a stand-in push service: the request is encrypted to
// a fresh browser key, signed with a fresh VAPID key, and the service's
// status comes back as the verdict.
func TestSendSignsWithVAPIDAndReportsTheServicesStatus(t *testing.T) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("browser key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth secret: %v", err)
	}

	var got *http.Request
	var body int64
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body = r.ContentLength
		w.WriteHeader(http.StatusCreated)
	}))
	defer service.Close()

	sender, err := New(public, private, "https://chintan.example")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The service is plain http here; the API refuses such an endpoint, the
	// sender does not care.
	status, err := sender.Send(context.Background(), model.PushSubscription{
		Endpoint: service.URL + "/send/abc",
		P256DH:   base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}, []byte(`{"type":"appended"}`))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(got.Header.Get("Authorization"), "vapid t=") {
		t.Fatalf("Authorization = %q, want a VAPID token", got.Header.Get("Authorization"))
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("TTL") != "3600" || got.Header.Get("Urgency") != "high" {
		t.Fatalf("headers = %v", got.Header)
	}
	if body <= int64(len(`{"type":"appended"}`)) {
		t.Fatalf("body of %d bytes is not an encrypted payload", body)
	}

	if _, err := New(public, "", "https://chintan.example"); err == nil {
		t.Fatal("a sender without a private key was built")
	}
}
