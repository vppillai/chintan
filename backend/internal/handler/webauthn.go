package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// WebAuthnAPI is the biometric surface the router needs.
//
// It is an interface rather than *service.WebAuthnService for one reason worth
// the indirection: it makes the biometric routes testable without a real
// authenticator ceremony, so the conformance test can prove every status the
// contract declares is actually reachable.
type WebAuthnAPI interface {
	Status(ctx context.Context, userID string) (bool, error)
	Disable(ctx context.Context, userID string) error
	BeginRegistration(ctx context.Context, userID string) (*model.WebAuthnOptionsResponse, error)
	FinishRegistration(ctx context.Context, userID string, req *model.WebAuthnVerifyRequest) error
	BeginLogin(ctx context.Context) (*model.WebAuthnOptionsResponse, error)
	FinishLogin(ctx context.Context, req *model.WebAuthnVerifyRequest) (*model.CognitoTokenSet, error)
}

var _ WebAuthnAPI = (*service.WebAuthnService)(nil)

// available reports whether biometrics are configured, answering 503 if not.
//
// An instance without a KMS key for the token vault genuinely cannot do this,
// and saying so is better than a 500 that looks like a fault.
func (rt *router) available(w http.ResponseWriter, r *http.Request) bool {
	if rt.WebAuthn == nil {
		p := httperr.New(http.StatusServiceUnavailable, "biometric unlock is not configured on this instance")
		p.Type = httperr.TypeBiometricUnavailable
		httperr.Write(w, r, p)
		return false
	}
	return true
}

func (rt *router) webauthnStatus(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	enrolled, err := rt.WebAuthn.Status(r.Context(), userID)
	if err != nil {
		httperr.InternalServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enrolled": enrolled})
}

func (rt *router) webauthnDisable(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if err := rt.WebAuthn.Disable(r.Context(), userID); err != nil {
		httperr.InternalServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *router) webauthnRegisterOptions(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	resp, err := rt.WebAuthn.BeginRegistration(r.Context(), userID)
	if err != nil {
		httperr.InternalServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (rt *router) webauthnRegister(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req model.WebAuthnVerifyRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	if err := rt.WebAuthn.FinishRegistration(r.Context(), userID, &req); err != nil {
		switch {
		case errors.Is(err, service.ErrWebAuthnMissingRefresh):
			httperr.BadRequest(w, r, "refresh_token is required")
		case errors.Is(err, service.ErrWebAuthnChallengeNotFound):
			httperr.BadRequest(w, r, "the enrollment challenge expired; start again")
		case errors.Is(err, service.ErrWebAuthnVerification), errors.Is(err, service.ErrWebAuthnSubMismatch):
			httperr.BadRequest(w, r, "the authenticator could not be verified")
		default:
			httperr.InternalServerError(w, r, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *router) webauthnLoginOptions(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}
	resp, err := rt.WebAuthn.BeginLogin(r.Context())
	if err != nil {
		if errors.Is(err, service.ErrWebAuthnNotEnrolled) {
			// Not enrolled is not a fault, and 503 was the wrong way to say so.
			// 503 means "this instance cannot do biometrics at all", which is a
			// different thing that needs different handling; it also reads as
			// transient, so a client retries an answer that cannot change until
			// somebody enrols. And because signing out revokes the credential by
			// design, it made a permanent loop: sign in, be offered biometric
			// unlock, fail, forever.
			//
			// 404 is the honest code — there is no credential here to log in
			// with — and it is not a probing oracle. This route is
			// unauthenticated and BeginLogin is a discoverable-credential login
			// that names no account, so the answer is identical for every
			// caller of this instance whatever accounts exist.
			p := httperr.New(http.StatusNotFound, "no biometric credential is enrolled here")
			p.Type = httperr.TypeBiometricNotEnrolled
			httperr.Write(w, r, p)
			return
		}
		httperr.InternalServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (rt *router) webauthnLogin(w http.ResponseWriter, r *http.Request) {
	if !rt.available(w, r) {
		return
	}

	var req model.WebAuthnVerifyRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	tokens, err := rt.WebAuthn.FinishLogin(r.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWebAuthnReEnrolRequired):
			// Deliberately distinguishable, and it does not make this endpoint
			// an oracle: reaching here means the assertion already verified, so
			// only someone who holds the credential is told anything. The
			// alternative is a permanent, identical 401 with nothing the user
			// can do — the vault was sealed by the retired KMS key and has just
			// been discarded, so enrolling again is the entire fix.
			obs.Log(r.Context()).Info("biometric login refused", slog.String("reason", "vault needs re-enrolment"))
			// Typed, because the client has to offer re-enrolment here and a
			// plain sign-in everywhere else 401 is returned. It was telling the
			// two apart by matching prose against the detail below.
			p := httperr.New(http.StatusUnauthorized, "biometric unlock must be set up again on this device")
			p.Type = httperr.TypeBiometricReEnrolmentRequired
			httperr.Write(w, r, p)
		case errors.Is(err, service.ErrWebAuthnChallengeNotFound),
			errors.Is(err, service.ErrWebAuthnVerification),
			errors.Is(err, service.ErrWebAuthnSubMismatch):
			// One answer for every biometric failure. A client must not be able
			// to tell "expired challenge" from "wrong finger" from "no such
			// credential" — and a 401 here means biometrics failed, which
			// clients must not confuse with an expired Cognito session.
			obs.Log(r.Context()).Info("biometric login refused", slog.String("reason", "verification"))
			httperr.Unauthorized(w, r, "biometric verification failed")
		default:
			httperr.InternalServerError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}
