package handler

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// pushNotConfiguredDetail is GET /v1/push/key's 404: the instance has no
// VAPID key pair in SSM, so nothing can be subscribed to. The app's
// Notifications card reads this status and explains the owner's step
// (docs/design/push.md).
const pushNotConfiguredDetail = "notifications are not configured on this instance"

// PushKey is the OpenAPI PushKey schema: the VAPID public key a browser
// subscribes with.
type PushKey struct {
	PublicKey string `json:"public_key"`
}

// PushSubscription is the OpenAPI PushSubscription schema: what the list
// shows. The endpoint's host says which push service holds it; the endpoint
// itself and the keys never leave the row, since anyone holding them could
// send to that browser.
type PushSubscription struct {
	ID            string  `json:"id"`
	EndpointHost  string  `json:"endpoint_host"`
	Label         string  `json:"label"`
	CreatedAt     string  `json:"created_at"`
	LastSuccessAt *string `json:"last_success_at"`
	Failures      int64   `json:"failures"`
}

// pushSubscribeRequest is the OpenAPI PushSubscribe schema: the browser's
// PushSubscription.toJSON() — endpoint, expirationTime and keys, spelled as
// the browser spells them — plus a label for the list.
type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	// ExpirationTime is null in every browser that ships push and is
	// accepted so decodeJSON's unknown-field refusal does not reject the
	// browser's own object; nothing reads it.
	ExpirationTime json.RawMessage `json:"expirationTime"`
	Keys           struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Label string `json:"label"`
}

func pushSubscriptionOf(s model.PushSubscription) PushSubscription {
	out := PushSubscription{ID: s.ID, Label: s.Label, CreatedAt: s.CreatedAt, Failures: s.Failures}
	if u, err := url.Parse(s.Endpoint); err == nil {
		out.EndpointHost = u.Host
	}
	if s.LastSuccessAt != "" {
		v := s.LastSuccessAt
		out.LastSuccessAt = &v
	}
	return out
}

func (rt *router) getPushKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.GetUserID(r.Context()); !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.PushPublicKey == "" {
		httperr.NotFound(w, r, pushNotConfiguredDetail)
		return
	}
	writeJSON(w, http.StatusOK, PushKey{PublicKey: rt.PushPublicKey})
}

func (rt *router) listPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	subs, err := rt.Push.ListSubscriptions(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]PushSubscription, 0, len(subs))
	for _, s := range subs {
		items = append(items, pushSubscriptionOf(s))
	}
	writeJSON(w, http.StatusOK, page(items, ""))
}

func (rt *router) createPushSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	var req pushSubscribeRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	sub, err := rt.Push.Subscribe(r.Context(), userID, service.PushSubscribeRequest{
		Endpoint: req.Endpoint, P256DH: req.Keys.P256DH, Auth: req.Keys.Auth, Label: req.Label,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pushSubscriptionOf(sub))
}

func (rt *router) deletePushSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if err := rt.Push.Unsubscribe(r.Context(), userID, r.PathValue("subscriptionId")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
