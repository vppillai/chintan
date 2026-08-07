package handler

import (
	"net/http"
	"os"

	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

// NewRouter creates a new HTTP router with all handlers configured.
// webauthn may be nil; biometric routes then return 503.
func NewRouter(notesService *service.NotesService, settingsService *service.SettingsService, captureService *service.CaptureService, webauthn *service.WebAuthnService, allowedOrigin string) http.Handler {
	if allowedOrigin == "" {
		allowedOrigin = os.Getenv("ALLOWED_ORIGIN")
	}

	mux := http.NewServeMux()

	notesHandler := NewNotesHandler(notesService)
	settingsHandler := NewSettingsHandler(settingsService)
	capturesHandler := NewCapturesHandler(captureService)
	webauthnHandler := NewWebAuthnHandler(webauthn)

	mux.HandleFunc("/v1/health", HealthHandler)

	// Public biometric login (API Gateway also marks these Auth NONE)
	mux.Handle("/v1/auth/webauthn/login/options", webauthnHandler)
	mux.Handle("/v1/auth/webauthn/login", webauthnHandler)

	// Authenticated biometric management
	mux.Handle("/v1/auth/webauthn/register/options", middleware.Auth(webauthnHandler))
	mux.Handle("/v1/auth/webauthn/register", middleware.Auth(webauthnHandler))
	mux.Handle("/v1/auth/webauthn/status", middleware.Auth(webauthnHandler))
	mux.Handle("/v1/auth/webauthn", middleware.Auth(webauthnHandler))

	mux.Handle("/v1/settings", middleware.Auth(settingsHandler))
	mux.Handle("/v1/notes/", middleware.Auth(notesHandler))
	mux.Handle("/v1/notes", middleware.Auth(notesHandler))
	mux.Handle("/v1/captures/", middleware.Auth(capturesHandler))
	mux.Handle("/v1/captures", middleware.Auth(capturesHandler))

	return middleware.CORS(allowedOrigin)(mux)
}
