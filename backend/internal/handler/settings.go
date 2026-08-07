package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// SettingsHandler handles settings requests
type SettingsHandler struct {
	settingsService *service.SettingsService
}

// NewSettingsHandler creates a new settings handler
func NewSettingsHandler(settingsService *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{
		settingsService: settingsService,
	}
}

// ServeHTTP handles settings requests
func (h *SettingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}

	switch r.Method {
	case "GET":
		h.getSettings(w, r, userID)
	case "PUT":
		h.putSettings(w, r, userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *SettingsHandler) getSettings(w http.ResponseWriter, r *http.Request, userID string) {
	settings, err := h.settingsService.GetSettings(r.Context(), userID)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (h *SettingsHandler) putSettings(w http.ResponseWriter, r *http.Request, userID string) {
	var settings model.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		httperr.BadRequest(w, "invalid JSON")
		return
	}

	err := h.settingsService.UpdateSettings(r.Context(), userID, settings)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}