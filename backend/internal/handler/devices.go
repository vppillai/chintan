package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
)

// Device is the OpenAPI Device schema: what the device list shows. Never the
// key, never its hash.
type Device struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// DeviceCreated is the OpenAPI DeviceCreated schema: the one response that
// carries the key. It is not stored anywhere after this — POST /v1/devices
// is deliberately not idempotent, so no replay record holds it either.
type DeviceCreated struct {
	Device
	Key string `json:"key"`
}

// deviceCreateRequest is the OpenAPI DeviceCreate schema.
type deviceCreateRequest struct {
	Name string `json:"name"`
}

func deviceOf(d model.Device) Device {
	out := Device{ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt}
	if d.LastUsedAt != "" {
		v := d.LastUsedAt
		out.LastUsedAt = &v
	}
	return out
}

func (rt *router) listDevices(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	devices, err := rt.Devices.ListDevices(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]Device, 0, len(devices))
	for _, d := range devices {
		items = append(items, deviceOf(d))
	}
	writeJSON(w, http.StatusOK, page(items, ""))
}

func (rt *router) createDevice(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	var req deviceCreateRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	device, key, err := rt.Devices.CreateDevice(r.Context(), userID, req.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DeviceCreated{Device: deviceOf(device), Key: key})
}

func (rt *router) revokeDevice(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if err := rt.Devices.RevokeDevice(r.Context(), userID, r.PathValue("deviceId")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
