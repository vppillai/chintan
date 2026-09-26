package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// The inbox: three routes an external recorder, watch or app reaches with a
// device key (Authorization: Bearer ck_…, the same value bare, or
// X-Device-Key: ck_…) instead of a Cognito session. The gateway admits them
// without its JWT authorizer; deviceAuthenticated is the whole check.
// Nothing on these routes returns note content — a capture row and, on
// /captures, a presigned PUT — so a leaked key can add to a person's notes
// until it is revoked and never read them. docs/design/inbox.md has the
// threat model.

// MaxInboxTextRequestBytes bounds POST /v1/inbox/text's body: twenty thousand
// runes can be eighty thousand bytes, plus the envelope.
const MaxInboxTextRequestBytes = 128 << 10

// Headers a one-shot POST carries beside its body, for a client that can
// set headers but cannot build JSON. X-Chintan-Note-Id and
// X-Chintan-Recorded-At are read on both one-shot routes; the other two
// only mean something for a recording.
const (
	HeaderInboxNoteID      = "X-Chintan-Note-Id"
	HeaderInboxLanguage    = "X-Chintan-Language"
	HeaderInboxDurationMS  = "X-Chintan-Duration-Ms"
	HeaderInboxRecordedAt  = "X-Chintan-Recorded-At"
	recordedAtMaxAge       = 7 * 24 * time.Hour
	recordedAtMaxSkew      = 5 * time.Minute
	recordedAtMaxPartBytes = 64
)

// HeaderDeviceKey carries the device key for a client whose Authorization
// header is spoken for.
const HeaderDeviceKey = "X-Device-Key"

// msgAudioTypeRequired is the one-shot audio route's 400 for a recording
// with no declared type, raw body and form part alike.
const msgAudioTypeRequired = "Content-Type is required: the recording's audio type"

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
		// No key at all takes the same road as a malformed one, so the
		// refusal is counted the same way. ContentLength is -1 when the
		// body's length is not known up front; the month's byte counter
		// then goes without this request.
		device, err := rt.Devices.Authenticate(r.Context(), deviceKeyOf(r), max(r.ContentLength, 0))
		if err != nil {
			countRefusal(r.Context(), err)
			fail(w, r, err)
			return
		}
		ctx := middleware.WithUserID(r.Context(), device.TenantID)
		ctx = context.WithValue(ctx, deviceContextKey{}, device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// deviceKeyOf is the key a request presents, in any of three spellings of the
// one credential: X-Device-Key: ck_…, Authorization: Bearer ck_…, or the
// same value bare in Authorization. A webhook that takes header key/value
// pairs — the Pebble Index ring's is one — has nowhere to learn the Bearer
// scheme, and a client whose Authorization header is spoken for has the
// second header, which is why it is read first. Whichever spelling, what
// follows is the same check and the same fixed 401, so nothing here widens
// the threat model: the key is still the only thing that opens the inbox.
// "" when the request presents none.
func deviceKeyOf(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get(HeaderDeviceKey)); raw != "" {
		return raw
	}
	auth := r.Header.Get("Authorization")
	if raw, ok := middleware.BearerToken(auth); ok {
		return raw
	}
	if raw := strings.TrimSpace(auth); strings.HasPrefix(raw, "ck_") {
		return raw
	}
	return ""
}

// countRefusal makes a refused key visible without any of the key: one
// InboxKeyRefused per refusal, by reason, with the dimensionless rollup the
// InboxKeyRefusedAlarm reads, and a WARN naming the device id and the reason
// once the id parsed. A malformed key logs nothing, so a probe cannot write
// bytes of its choosing into the log; the 401 on the wire is unchanged.
func countRefusal(ctx context.Context, err error) {
	var ref *service.DeviceRefusal
	if !errors.As(err, &ref) {
		return
	}
	obs.CountWithRollup(ctx, "InboxKeyRefused", map[string]string{"Reason": ref.Reason})
	if ref.DeviceID != "" {
		obs.Log(ctx).Warn("device key refused",
			slog.String("device_id", ref.DeviceID),
			slog.String("reason", ref.Reason))
	}
}

// parseRecordedAt reads a sender's claim of when it recorded — milliseconds
// since the epoch (10 to 13 digits) or RFC 3339 — into the row's own layout.
// It is telemetry, not input: a value that does not parse, or that is more
// than seven days before now or five minutes after it (a device clock that
// is wrong), is dropped and the request goes on. Never a 400.
func parseRecordedAt(raw string, now time.Time) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	var at time.Time
	if n := len(raw); n >= 10 && n <= 13 && strings.Trim(raw, "0123456789") == "" {
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", false
		}
		at = time.UnixMilli(ms)
	} else {
		var err error
		if at, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return "", false
		}
	}
	if at.Before(now.Add(-recordedAtMaxAge)) || at.After(now.Add(recordedAtMaxSkew)) {
		return "", false
	}
	return model.FormatTime(at), true
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

// inboxAudio takes the recording as the body — raw, or as the audio part of
// a multipart/form-data POST — and writes it to the bucket, for a client
// that can only POST a file. 202 with the capture; the bucket's notification
// runs the pipeline as for any upload.
func (rt *router) inboxAudio(w http.ResponseWriter, r *http.Request) {
	userID, device, ok := inboxIdentity(r)
	if !ok {
		fail(w, r, service.ErrDeviceKeyUnknown)
		return
	}
	contentType := r.Header.Get("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		httperr.BadRequest(w, r, msgAudioTypeRequired)
		return
	}
	req := service.CaptureRequest{
		NoteID:      strings.TrimSpace(r.Header.Get(HeaderInboxNoteID)),
		ContentType: contentType,
		Language:    r.Header.Get(HeaderInboxLanguage),
		Source:      model.DeviceSource(device.ID),
	}
	req.RecordedAt, _ = parseRecordedAt(r.Header.Get(HeaderInboxRecordedAt), time.Now())
	if raw := strings.TrimSpace(r.Header.Get(HeaderInboxDurationMS)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			httperr.BadRequest(w, r, HeaderInboxDurationMS+" must be a whole number of milliseconds")
			return
		}
		req.DurationMS = n
	}
	if mediaType, _, _ := mime.ParseMediaType(contentType); mediaType == "multipart/form-data" {
		rt.inboxAudioForm(w, r, userID, req)
		return
	}
	body, err := readBody(w, r, service.MaxInboxAudioBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			recordingTooLarge(w, r)
			return
		}
		httperr.BadRequest(w, r, "the request body could not be read")
		return
	}
	if len(body) == 0 {
		httperr.BadRequest(w, r, "the request body is empty; send the recording as the body")
		return
	}
	rt.acceptInboxAudio(w, r, userID, req, body)
}

// inboxAudioForm is the form shape of the same route, for a webhook that
// posts a form rather than a file. The Pebble Index ring's sends parts named
// audio (audio/mp4), transcription (its own, when it managed one), recordedAt
// and client. The audio part is ingested exactly as a raw body is, under the
// part's own Content-Type — required, as the raw body's is: RFC 7578 makes
// an untyped part text/plain, and the ring sends audio/mp4 explicitly. With
// no audio part but a transcription, the
// transcription is filed as /v1/inbox/text files text, so a ring set to send
// only its transcription still makes a note. With both, the audio wins and
// the ring's transcription is dropped: the pipeline transcribes with the
// note's language, which the ring's does not know. client names nothing this
// route acts on. recordedAt (milliseconds since the epoch) is the ring's
// clock at the moment it recorded and goes to the row's timing record, as
// the X-Chintan-Recorded-At header does; created_at stays the server's.
//
// The whole form is read under MaxInboxAudioBytes, the raw body's cap: the
// gateway limit that cap exists for applies to the body as the gateway sees
// it, envelope and all, and nothing here buffers more than the cap.
func (rt *router) inboxAudioForm(w http.ResponseWriter, r *http.Request, userID string, req service.CaptureRequest) {
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxInboxAudioBytes)
	form, err := r.MultipartReader()
	if err != nil {
		httperr.BadRequest(w, r, "the request body could not be read")
		return
	}
	var audio []byte
	var transcription string
	for {
		part, err := form.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			unreadableForm(w, r, err)
			return
		}
		switch part.FormName() {
		case "audio":
			// The part's own type, not the request's, which is the form's.
			if req.ContentType = part.Header.Get("Content-Type"); req.ContentType == "" {
				httperr.BadRequest(w, r, msgAudioTypeRequired)
				return
			}
			if audio, err = io.ReadAll(part); err != nil {
				unreadableForm(w, r, err)
				return
			}
		case "transcription":
			// Bounded at the JSON route's body cap; anything cut off there
			// was past the rune limit several times over and acceptInboxText
			// refuses it with the same sentence /v1/inbox/text would.
			raw, err := io.ReadAll(io.LimitReader(part, MaxInboxTextRequestBytes))
			if err != nil {
				unreadableForm(w, r, err)
				return
			}
			// Invalid UTF-8 becomes U+FFFD, as the JSON route's decoder
			// makes it, so both roads file the same text.
			transcription = strings.ToValidUTF8(string(raw), "\uFFFD")
		case "recordedAt":
			// Epoch milliseconds; a longer part is not a timestamp.
			raw, err := io.ReadAll(io.LimitReader(part, recordedAtMaxPartBytes))
			if err != nil {
				unreadableForm(w, r, err)
				return
			}
			if at, ok := parseRecordedAt(string(raw), time.Now()); ok {
				req.RecordedAt = at
			}
		}
		// Any other part is skipped: the next NextPart discards what is left
		// of this one.
	}
	switch {
	case len(audio) > 0:
		rt.acceptInboxAudio(w, r, userID, req, audio)
	case strings.TrimSpace(transcription) != "":
		rt.acceptInboxText(w, r, userID, service.CaptureRequest{NoteID: req.NoteID, Source: req.Source, RecordedAt: req.RecordedAt}, transcription)
	default:
		httperr.BadRequest(w, r, "the form has no audio or transcription part")
	}
}

// unreadableForm answers for a form that could not be read through: the
// raw body's 413 when the cap stopped it, 400 otherwise.
func unreadableForm(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		recordingTooLarge(w, r)
		return
	}
	httperr.BadRequest(w, r, "the request body could not be read")
}

// recordingTooLarge is the one 413 of the one-shot audio route, for a raw
// body and a form alike.
func recordingTooLarge(w http.ResponseWriter, r *http.Request) {
	httperr.PayloadTooLarge(w, r, fmt.Sprintf("the recording exceeds %d bytes", service.MaxInboxAudioBytes))
}

// acceptInboxAudio is the tail a raw body and a form's audio part share: the
// spend cap, the row, the object with its retention tags, the 202.
func (rt *router) acceptInboxAudio(w http.ResponseWriter, r *http.Request, userID string, req service.CaptureRequest, body []byte) {
	if rt.refusedByCap(w, r) {
		return
	}
	capture, err := rt.Captures.IngestAudio(r.Context(), userID, req, body)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, InboxAccepted{Capture: captureOf(capture)})
}

// acceptInboxText is the tail /v1/inbox/text and a form's transcription
// share: the target note, the bounds, the spend cap, the row, the text
// object, the worker hand-off, the 202. The note comes from the body's
// note_id when it has one, else from X-Chintan-Note-Id — the header is for
// a client that can set headers but cannot build the JSON, so the body,
// which a sender had to construct on purpose, wins when both are sent.
// The service's tenant-scoped GetNote turns another tenant's id, or none,
// into the same 404 the audio route gives.
func (rt *router) acceptInboxText(w http.ResponseWriter, r *http.Request, userID string, req service.CaptureRequest, text string) {
	if req.NoteID == "" {
		req.NoteID = strings.TrimSpace(r.Header.Get(HeaderInboxNoteID))
	}
	text = strings.TrimSpace(text)
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
	capture, err := rt.Captures.IngestText(r.Context(), userID, req, text)
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
	recordedAt, _ := parseRecordedAt(r.Header.Get(HeaderInboxRecordedAt), time.Now())
	rt.acceptInboxText(w, r, userID, service.CaptureRequest{
		NoteID:     req.NoteID,
		Source:     model.DeviceSource(device.ID),
		RecordedAt: recordedAt,
	}, req.Text)
}
