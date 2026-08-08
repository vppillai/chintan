package handler

import (
	"net/http"
	"os"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

// NewRouter wires the HTTP surface.
//
// webauthn may be nil; biometric routes then return 503. verifier may be nil
// only in tests that inject an identity directly — middleware.Auth fails closed
// without it.
func NewRouter(
	notesService *service.NotesService,
	settingsService *service.SettingsService,
	captureService *service.CaptureService,
	webauthn *service.WebAuthnService,
	allowedOrigin string,
	verifier auth.Verifier,
) http.Handler {
	if allowedOrigin == "" {
		allowedOrigin = os.Getenv("ALLOWED_ORIGIN")
	}

	authenticated := middleware.Auth(verifier)

	mux := http.NewServeMux()

	notesHandler := NewNotesHandler(notesService)
	settingsHandler := NewSettingsHandler(settingsService)
	capturesHandler := NewCapturesHandler(captureService)
	webauthnHandler := NewWebAuthnHandler(webauthn)

	mux.HandleFunc("/v1/health", HealthHandler)

	// Public biometric login. These are the only unauthenticated data routes,
	// and API Gateway marks them AuthorizationType: NONE to match.
	mux.Handle("/v1/auth/webauthn/login/options", webauthnHandler)
	mux.Handle("/v1/auth/webauthn/login", webauthnHandler)

	// Authenticated biometric management
	mux.Handle("/v1/auth/webauthn/register/options", authenticated(webauthnHandler))
	mux.Handle("/v1/auth/webauthn/register", authenticated(webauthnHandler))
	mux.Handle("/v1/auth/webauthn/status", authenticated(webauthnHandler))
	mux.Handle("/v1/auth/webauthn", authenticated(webauthnHandler))

	mux.Handle("/v1/settings", authenticated(settingsHandler))
	mux.Handle("/v1/notes/", authenticated(notesHandler))
	mux.Handle("/v1/notes", authenticated(notesHandler))
	mux.Handle("/v1/captures/", authenticated(capturesHandler))
	mux.Handle("/v1/captures", authenticated(capturesHandler))

	return middleware.CORS(allowedOrigin)(mux)
}
