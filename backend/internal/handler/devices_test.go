package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
)

// createDevice issues a key through the API and returns the 201 body.
func (h *harness) createDevice(t *testing.T, userID, name string) handler.DeviceCreated {
	t.Helper()
	w := h.do(t, http.MethodPost, "/v1/devices", userID, map[string]any{"name": name})
	if w.Code != http.StatusCreated {
		t.Fatalf("create device: status = %d body = %s", w.Code, w.Body.String())
	}
	var out handler.DeviceCreated
	decodeInto(t, w, &out)
	return out
}

// The key is in the 201 and nowhere else the API answers.
func TestDeviceKeyIsShownOnceAndNeverListed(t *testing.T) {
	h := newHarness(t)
	created := h.createDevice(t, "user1", "Kitchen watch")
	if !strings.HasPrefix(created.Key, "ck_"+created.ID+"_") {
		t.Fatalf("key %q does not carry the device id %q", created.Key, created.ID)
	}
	if created.LastUsedAt != nil || created.LastUsedFrom != nil || created.UsageMonth != nil || created.ExpiresAt != nil {
		t.Fatalf("a fresh device has been used, or expires: %+v", created)
	}

	w := h.do(t, http.MethodGet, "/v1/devices", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, created.Key) || strings.Contains(body, "key") {
		t.Fatalf("the device list carries key material: %s", body)
	}
	var page handler.Page[handler.Device]
	decodeInto(t, w, &page)
	if len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Items[0].Name != "Kitchen watch" {
		t.Fatalf("list = %+v", page.Items)
	}

	// Another tenant sees nothing and cannot revoke it.
	if w := h.do(t, http.MethodGet, "/v1/devices", "user2", nil); !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("user2 sees %s", w.Body.String())
	}
	if w := h.do(t, http.MethodDelete, "/v1/devices/"+created.ID, "user2", nil); w.Code != http.StatusNotFound {
		t.Fatalf("foreign revoke: %d", w.Code)
	}

	if w := h.do(t, http.MethodDelete, "/v1/devices/"+created.ID, "user1", nil); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}
	w = h.do(t, http.MethodGet, "/v1/devices", "user1", nil)
	decodeInto(t, w, &page)
	if len(page.Items) != 0 {
		t.Fatalf("a revoked device is still listed: %+v", page.Items)
	}
}

// The list says what each key sent this month — requests and their bytes,
// from the counters the inbox writes with the day's — and null for a key
// that sent nothing.
func TestDeviceListShowsWhatTheKeySentThisMonth(t *testing.T) {
	h := newHarness(t)
	used := h.createDevice(t, "user1", "Watch")
	idle := h.createDevice(t, "user1", "Shortcut")
	body, err := json.Marshal(map[string]any{"text": "buy milk and eggs"})
	if err != nil {
		t.Fatal(err)
	}
	if w := h.do(t, http.MethodPost, "/v1/inbox/text", "", body, [2]string{"Authorization", "Bearer " + used.Key}); w.Code != http.StatusAccepted {
		t.Fatalf("inbox: status = %d body = %s", w.Code, w.Body.String())
	}

	w := h.do(t, http.MethodGet, "/v1/devices", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var page handler.Page[handler.Device]
	decodeInto(t, w, &page)
	byID := map[string]handler.Device{}
	for _, d := range page.Items {
		byID[d.ID] = d
	}
	if got := byID[used.ID].UsageMonth; got == nil || got.Requests != 1 || got.Bytes != int64(len(body)) || got.Month != harnessNow.Format("2006-01") {
		t.Fatalf("used device's usage_month = %+v, want 1 request of %d bytes in %s", got, len(body), harnessNow.Format("2006-01"))
	}
	if got := byID[idle.ID].UsageMonth; got != nil {
		t.Fatalf("idle device's usage_month = %+v, want null", got)
	}
	if !strings.Contains(w.Body.String(), `"usage_month":null`) {
		t.Fatalf("the idle device's usage is not null on the wire: %s", w.Body.String())
	}
	// Where from: httptest's requests come from 192.0.2.1:1234, and the list
	// says the neighbourhood, never the address (WH-B).
	if from := byID[used.ID].LastUsedFrom; from == nil || *from != "192.0.2.x" {
		t.Fatalf("used device's last_used_from = %v, want 192.0.2.x", from)
	}
	if byID[idle.ID].LastUsedFrom != nil || !strings.Contains(w.Body.String(), `"last_used_from":null`) {
		t.Fatalf("the idle device has a last_used_from: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "192.0.2.1") {
		t.Fatalf("the full address is on the wire: %s", w.Body.String())
	}
}

// expires_in_days is optional, 1–365, and becomes expires_at on the device;
// out of range is a 400 in the fixed sentence (WH-A).
func TestDeviceExpiryIsOptionalAndBounded(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/v1/devices", "user1", map[string]any{"name": "Script", "expires_in_days": 30})
	if w.Code != http.StatusCreated {
		t.Fatalf("thirty days: status = %d body = %s", w.Code, w.Body.String())
	}
	var created handler.DeviceCreated
	decodeInto(t, w, &created)
	if want := harnessNow.AddDate(0, 0, 30).UTC().Format("2006-01-02T15:04:05.000000000Z"); created.ExpiresAt == nil || *created.ExpiresAt != want {
		t.Fatalf("expires_at = %v, want %s", created.ExpiresAt, want)
	}
	for _, days := range []any{0, 366, -7} {
		w := h.do(t, http.MethodPost, "/v1/devices", "user1", map[string]any{"name": "Script", "expires_in_days": days})
		if w.Code != http.StatusBadRequest || problemOf(t, w)["detail"] != "expires_in_days must be between 1 and 365" {
			t.Fatalf("expires_in_days %v: status = %d body = %s", days, w.Code, w.Body.String())
		}
	}
	// The list carries the date, so the card can count down to it.
	w = h.do(t, http.MethodGet, "/v1/devices", "user1", nil)
	var page handler.Page[handler.Device]
	decodeInto(t, w, &page)
	if len(page.Items) != 1 || page.Items[0].ExpiresAt == nil || *page.Items[0].ExpiresAt != *created.ExpiresAt {
		t.Fatalf("list = %+v", page.Items)
	}
}
