package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

func (rt *router) getSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	stored, err := rt.Settings.GetSettings(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(service.NormalizeSettings(stored), rt.SpendCapMicros))
}

// putSettings validates, stores, and returns what was stored.
//
// v1 stored whatever it was sent and echoed the request body straight back, so
// every coercion was invisible: a theme the server did not recognise came back
// looking accepted, and a negative retention was persisted. Returning the
// stored record is the only way a client can tell what actually happened.
func (rt *router) putSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req SettingsUpdate
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}

	validated, err := service.ValidateSettings(req.settings())
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := rt.Settings.UpdateSettings(r.Context(), userID, validated); err != nil {
		fail(w, r, err)
		return
	}

	// Read back rather than trusting the write: what a later GET will report is
	// the only honest answer to "what did you store".
	stored, err := rt.Settings.GetSettings(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(service.NormalizeSettings(stored), rt.SpendCapMicros))
}
