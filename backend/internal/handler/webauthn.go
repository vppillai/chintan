package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// WebAuthnHandler serves biometric enroll/unlock endpoints.
type WebAuthnHandler struct {
	svc *service.WebAuthnService
}

func NewWebAuthnHandler(svc *service.WebAuthnService) *WebAuthnHandler {
	return &WebAuthnHandler{svc: svc}
}

func (h *WebAuthnHandler) unavailable(w http.ResponseWriter) bool {
	if h == nil || h.svc == nil {
		httperr.WriteJSON(w, errors.New("WebAuthn is not available"), http.StatusServiceUnavailable)
		return true
	}
	return false
}

func (h *WebAuthnHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/auth/webauthn")
	path = strings.Trim(path, "/")

	switch {
	case r.Method == http.MethodGet && path == "status":
		h.status(w, r)
	case r.Method == http.MethodDelete && path == "":
		h.disable(w, r)
	case r.Method == http.MethodPost && path == "register/options":
		h.registerOptions(w, r)
	case r.Method == http.MethodPost && path == "register":
		h.register(w, r)
	case r.Method == http.MethodPost && path == "login/options":
		h.loginOptions(w, r)
	case r.Method == http.MethodPost && path == "login":
		h.login(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *WebAuthnHandler) status(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}
	enrolled, err := h.svc.Status(r.Context(), userID)
	if err != nil {
		httperr.InternalServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"enrolled": enrolled})
}

func (h *WebAuthnHandler) disable(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}
	if err := h.svc.Disable(r.Context(), userID); err != nil {
		httperr.InternalServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WebAuthnHandler) registerOptions(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}
	resp, err := h.svc.BeginRegistration(r.Context(), userID)
	if err != nil {
		log.Printf("webauthn.register.options: %v", err)
		httperr.InternalServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *WebAuthnHandler) register(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}
	var req model.WebAuthnVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid request body")
		return
	}
	if err := h.svc.FinishRegistration(r.Context(), userID, &req); err != nil {
		switch {
		case errors.Is(err, service.ErrWebAuthnMissingRefresh):
			httperr.BadRequest(w, "refresh_token is required")
		case errors.Is(err, service.ErrWebAuthnChallengeNotFound):
			httperr.BadRequest(w, "registration challenge expired, please retry")
		case errors.Is(err, service.ErrWebAuthnVerification), errors.Is(err, service.ErrWebAuthnSubMismatch):
			httperr.BadRequest(w, "could not verify authenticator")
		default:
			log.Printf("webauthn.register: %v", err)
			httperr.InternalServerError(w, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "Biometric unlock enabled"})
}

func (h *WebAuthnHandler) loginOptions(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.BeginLogin(r.Context())
	if err != nil {
		if errors.Is(err, service.ErrWebAuthnNotEnrolled) {
			httperr.BadRequest(w, "biometric unlock is not set up")
			return
		}
		log.Printf("webauthn.login.options: %v", err)
		httperr.InternalServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *WebAuthnHandler) login(w http.ResponseWriter, r *http.Request) {
	var req model.WebAuthnVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid request body")
		return
	}
	tokens, err := h.svc.FinishLogin(r.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWebAuthnChallengeNotFound):
			httperr.BadRequest(w, "login challenge expired, please retry")
		case errors.Is(err, service.ErrWebAuthnVerification), errors.Is(err, service.ErrWebAuthnSubMismatch):
			// 401 means biometric failed — clients must not treat as Cognito session expiry.
			httperr.Unauthorized(w, "biometric verification failed")
		default:
			log.Printf("webauthn.login: %v", err)
			httperr.InternalServerError(w, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tokens)
}
