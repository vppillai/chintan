package handler_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

// browserSubscription is the object PushSubscription.toJSON() gives the app,
// as the browser spells it (expirationTime null), plus the app's label.
func browserSubscription(n int) map[string]any {
	return map[string]any{
		"endpoint":       fmt.Sprintf("https://web.push.apple.com/send/%d", n),
		"expirationTime": nil,
		"keys": map[string]any{
			"p256dh": "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM",
			"auth":   "tBHItJI5svbpez7KI4CCXg",
		},
		"label": "Safari on iPhone",
	}
}

func TestPushKeyIsTheInstancesOr404WhenTheOwnerHasNotMadeOne(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodGet, "/v1/push/key", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("key: %d %s", w.Code, w.Body.String())
	}
	var key handler.PushKey
	decodeInto(t, w, &key)
	if key.PublicKey != harnessVAPIDPublicKey {
		t.Fatalf("public_key = %q", key.PublicKey)
	}

	w = newHarness(t, withoutPushKey()).do(t, http.MethodGet, "/v1/push/key", "user1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unconfigured key: %d", w.Code)
	}
	if got := problemOf(t, w)["detail"]; got != "notifications are not configured on this instance" {
		t.Fatalf("detail = %v", got)
	}
}

// The browser's own object is accepted as it is; the list carries the
// endpoint's host and never the endpoint or the keys; ten per tenant, the
// same endpoint again is the same row.
func TestPushSubscriptionsAreListedWithoutSecretsAndCappedAtTen(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/v1/push/subscriptions", "user1", browserSubscription(1))
	if w.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body.String())
	}
	var created handler.PushSubscription
	decodeInto(t, w, &created)
	if created.EndpointHost != "web.push.apple.com" || created.Label != "Safari on iPhone" || created.LastSuccessAt != nil {
		t.Fatalf("created = %+v", created)
	}

	w = h.do(t, http.MethodGet, "/v1/push/subscriptions", "user1", nil)
	body := w.Body.String()
	if strings.Contains(body, "/send/") || strings.Contains(body, "p256dh") || strings.Contains(body, "tBHItJI5") {
		t.Fatalf("the list carries the endpoint or a key: %s", body)
	}
	var listed handler.Page[handler.PushSubscription]
	decodeInto(t, w, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("list = %+v", listed.Items)
	}

	// Idempotent on the endpoint: a browser subscribing again is one row.
	h.do(t, http.MethodPost, "/v1/push/subscriptions", "user1", browserSubscription(1))
	for i := 2; i <= model.MaxPushSubscriptionsPerTenant; i++ {
		if w := h.do(t, http.MethodPost, "/v1/push/subscriptions", "user1", browserSubscription(i)); w.Code != http.StatusCreated {
			t.Fatalf("subscription %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := h.do(t, http.MethodPost, "/v1/push/subscriptions", "user1", browserSubscription(11)); w.Code != http.StatusConflict {
		t.Fatalf("eleventh: %d %s", w.Code, w.Body.String())
	}

	// Another tenant sees nothing and cannot remove it; the owner can, once.
	if w := h.do(t, http.MethodGet, "/v1/push/subscriptions", "user2", nil); !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("user2 sees %s", w.Body.String())
	}
	if w := h.do(t, http.MethodDelete, "/v1/push/subscriptions/"+created.ID, "user2", nil); w.Code != http.StatusNotFound {
		t.Fatalf("foreign delete: %d", w.Code)
	}
	if w := h.do(t, http.MethodDelete, "/v1/push/subscriptions/"+created.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := h.do(t, http.MethodDelete, "/v1/push/subscriptions/"+created.ID, "user1", nil); w.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", w.Code)
	}
}

func TestPushSubscribeRefusesAnInsecureEndpoint(t *testing.T) {
	h := newHarness(t)
	bad := browserSubscription(1)
	bad["endpoint"] = "http://push.example/insecure"
	w := h.do(t, http.MethodPost, "/v1/push/subscriptions", "user1", bad)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("http endpoint: %d %s", w.Code, w.Body.String())
	}
	if got := problemOf(t, w)["detail"]; got != "the subscription endpoint must be an https URL" {
		t.Fatalf("detail = %v", got)
	}
}
