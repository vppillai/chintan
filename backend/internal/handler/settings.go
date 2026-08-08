package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
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
	writeJSON(w, http.StatusOK, settingsOf(rt.withDefaults(stored)))
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

	var req model.Settings
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}

	validated, err := service.ValidateSettings(req)
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
	writeJSON(w, http.StatusOK, settingsOf(rt.withDefaults(stored)))
}

// withDefaults fills the fields a pre-v2 record does not carry and substitutes
// the instance-wide spend cap for a tenant that has not set its own, so the UI
// can show the budget that will actually be enforced.
func (rt *router) withDefaults(s model.Settings) model.Settings {
	s = service.NormalizeSettings(s)
	if s.DailySpendCapMicros == 0 {
		s.DailySpendCapMicros = rt.DefaultSpendCapMicros
	}
	return s
}
