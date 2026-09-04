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
// The response is the stored record, never the request body echoed back.
// Echoing hides every coercion: a theme the server did not recognise would come
// back looking accepted. Returning what was stored is the only way a client can
// tell what actually happened.
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
