package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The inbox: three routes an external recorder, watch or app reaches with a
// device key (Authorization: Bearer ck_…) instead of a Cognito session. The
// gateway admits them without its JWT authorizer; deviceAuthenticated is the
// whole check. Nothing on these routes returns note content — a capture row
// and, on /captures, a presigned PUT — so a leaked key can add to a person's
// notes until it is revoked and never read them. docs/design/inbox.md has
// the threat model.

// MaxInboxTextRequestBytes bounds POST /v1/inbox/text's body: twenty thousand
// runes can be eighty thousand bytes, plus the envelope.
const MaxInboxTextRequestBytes = 128 << 10

// Headers a one-shot audio POST carries beside its body, for a client that
// can set headers but cannot build JSON.
const (
	HeaderInboxNoteID     = "X-Chintan-Note-Id"
	HeaderInboxLanguage   = "X-Chintan-Language"
	HeaderInboxDurationMS = "X-Chintan-Duration-Ms"
)

// inboxCaptureRequest is the OpenAPI InboxCaptureCreate schema: CaptureCreate
// plus a language for this recording.
type inboxCaptureRequest struct {
	ContentType string `json:"content_type"`
	NoteID      string `json:"note_id"`
	DurationMS  int64  `json:"duration_ms"`
	SizeBytes   int64  `json:"size_bytes"`
	Language    string `json:"language"`
}

// inboxTextRequest is the OpenAPI InboxText schema.
type inboxTextRequest struct {
	Text   string `json:"text"`
	NoteID string `json:"note_id"`
}

// InboxAccepted is the OpenAPI InboxAccepted schema, the 202 of the two
// one-shot routes: the capture the pipeline is now running.
type InboxAccepted struct {
	Capture Capture `json:"capture"`
}

type deviceContextKey struct{}

// deviceAuthenticated resolves the device key and puts the device's tenant on
// the context as the request's identity, so everything downstream — the
// request counter, idempotent replay, the capture service — works as it does
// for the app. The key reaches Authenticate and nothing else: not the
// context, not a log line.
func (rt *router) deviceAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.Devices == nil {
			httperr.ServiceUnavailable(w, r, "the inbox is not configured on this instance")
			return
		}
		raw, ok := middleware.BearerToken(r.Header.Get("Authorization"))
		if !ok {
			fail(w, r, service.ErrDeviceKeyUnknown)
			return
		}
		device, err := rt.Devices.Authenticate(r.Context(), raw)
		if err != nil {
			fail(w, r, err)
			return
		}
		ctx := middleware.WithUserID(r.Context(), device.TenantID)
		ctx = context.WithValue(ctx, deviceContextKey{}, device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// inboxIdentity is the tenant and device deviceAuthenticated put on the
// context; false means the route was reached without it, which the router
// does not allow.
func inboxIdentity(r *http.Request) (string, model.Device, bool) {
	userID, ok := middleware.GetUserID(r.Context())
	device, isDevice := r.Context().Value(deviceContextKey{}).(model.Device)
	return userID, device, ok && isDevice
}

// inboxCapture is POST /v1/captures for a device: the row and the presigned
// PUTs, for a client that can do both requests (an iOS Shortcut can).
func (rt *router) inboxCapture(w http.ResponseWriter, r *http.Request) {
	userID, device, ok := inboxIdentity(r)
	if !ok {
		fail(w, r, service.ErrDeviceKeyUnknown)
		return
	}
	var req inboxCaptureRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	if req.ContentType == "" {
		httperr.BadRequest(w, r, "content_type is required")
		return
	}
	if req.SizeBytes < 0 || req.DurationMS < 0 {
		httperr.BadRequest(w, r, "size_bytes and duration_ms must not be negative")
		return
	}
	if rt.refusedByCap(w, r) {
		return
	}
	created, err := rt.Captures.BeginCapture(r.Context(), userID, service.CaptureRequest{
		NoteID:      req.NoteID,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
		DurationMS:  req.DurationMS,
		Language:    req.Language,
		Source:      model.DeviceSource(device.ID),
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCaptureCreated(w, created)
}

// inboxAudio takes the recording itself as the body and writes it to the
// bucket, for a client that can only POST a file. 202 with the capture; the
// bucket's notification runs the pipeline as for any upload.
func (rt *router) inboxAudio(w http.ResponseWriter, r *http.Request) {
	userID, device, ok := inboxIdentity(r)
	if !ok {
		fail(w, r, service.ErrDeviceKeyUnknown)
		return
	}
	contentType := r.Header.Get("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		httperr.BadRequest(w, r, "Content-Type is required: the recording's audio type")
		return
	}
	var durationMS int64
	if raw := strings.TrimSpace(r.Header.Get(HeaderInboxDurationMS)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			httperr.BadRequest(w, r, HeaderInboxDurationMS+" must be a whole number of milliseconds")
			return
		}
		durationMS = n
	}
	body, err := readBody(w, r, service.MaxInboxAudioBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			httperr.PayloadTooLarge(w, r, fmt.Sprintf("the recording exceeds %d bytes", service.MaxInboxAudioBytes))
			return
		}
		httperr.BadRequest(w, r, "the request body could not be read")
		return
	}
	if len(body) == 0 {
		httperr.BadRequest(w, r, "the request body is empty; send the recording as the body")
		return
	}
	if rt.refusedByCap(w, r) {
		return
	}
	capture, err := rt.Captures.IngestAudio(r.Context(), userID, service.CaptureRequest{
		NoteID:      strings.TrimSpace(r.Header.Get(HeaderInboxNoteID)),
		ContentType: contentType,
		DurationMS:  durationMS,
		Language:    r.Header.Get(HeaderInboxLanguage),
		Source:      model.DeviceSource(device.ID),
	}, body)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, InboxAccepted{Capture: captureOf(capture)})
}

// inboxText takes text that needs no transcription. 202 with the capture,
// already at transcribed; the worker routes, cleans and appends it.
func (rt *router) inboxText(w http.ResponseWriter, r *http.Request) {
	userID, device, ok := inboxIdentity(r)
	if !ok {
		fail(w, r, service.ErrDeviceKeyUnknown)
		return
	}
	var req inboxTextRequest
	if !decodeJSON(w, r, MaxInboxTextRequestBytes, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		httperr.BadRequest(w, r, "text is required")
		return
	}
	if n := len([]rune(text)); n > service.MaxInboxTextRunes {
		httperr.BadRequest(w, r, fmt.Sprintf("text is %d characters; the limit is %d", n, service.MaxInboxTextRunes))
		return
	}
	if rt.refusedByCap(w, r) {
		return
	}
	capture, err := rt.Captures.IngestText(r.Context(), userID, service.CaptureRequest{
		NoteID: req.NoteID,
		Source: model.DeviceSource(device.ID),
	}, text)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, InboxAccepted{Capture: captureOf(capture)})
}
