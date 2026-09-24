package handler_test

import (
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
	if created.LastUsedAt != nil {
		t.Fatalf("a fresh device has been used at %v", *created.LastUsedAt)
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
