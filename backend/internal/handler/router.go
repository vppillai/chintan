package handler

import (
	"net/http"
	"os"

	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

// NewRouter creates a new HTTP router with all handlers configured
func NewRouter(notesService *service.NotesService, settingsService *service.SettingsService, allowedOrigin string) http.Handler {
	if allowedOrigin == "" {
		allowedOrigin = os.Getenv("ALLOWED_ORIGIN")
	}

	mux := http.NewServeMux()

	// Create handlers
	notesHandler := NewNotesHandler(notesService)
	settingsHandler := NewSettingsHandler(settingsService)

	// Health endpoint (no auth required)
	mux.HandleFunc("/v1/health", HealthHandler)

	// Protected endpoints
	mux.Handle("/v1/settings", middleware.Auth(settingsHandler))
	mux.Handle("/v1/notes/", middleware.Auth(notesHandler))
	mux.Handle("/v1/notes", middleware.Auth(notesHandler))

	// Apply CORS to all routes
	return middleware.CORS(allowedOrigin)(mux)
}