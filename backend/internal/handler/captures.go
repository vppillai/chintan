package handler

import (
	"net/http"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// captureCreateRequest is the OpenAPI CaptureCreate schema.
type captureCreateRequest struct {
	ContentType string `json:"content_type"`
	NoteID      string `json:"note_id"`
	DurationMS  int64  `json:"duration_ms"`
	SizeBytes   int64  `json:"size_bytes"`
}

// captureTargetRequest is the OpenAPI CaptureTarget schema.
type captureTargetRequest struct {
	NoteID       string `json:"note_id"`
	NewNoteTitle string `json:"new_note_title"`
}

// captureMoveRequest is the OpenAPI CaptureMove schema.
type captureMoveRequest struct {
	NoteID string `json:"note_id"`
}

// downloadResponse is the presigned GET a client plays or reads from.
type downloadResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

func (rt *router) listCaptures(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	opts, err := listOptions(r)
	if answerValidation(w, r, err) {
		return
	}

	filter, valid := service.ParseCaptureFilter(r.URL.Query().Get("status"))
	if !valid {
		httperr.BadRequest(w, r, "status must be one of pending, failed, needs_target, all")
		return
	}

	// note_id is not in the contract but the note detail view uses it, and a
	// note's captures are a genuinely cheaper query than the tenant's.
	if noteID := r.URL.Query().Get("note_id"); noteID != "" {
		got, err := rt.Captures.ListCapturesForNote(r.Context(), userID, noteID, opts)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, page(capturesOf(got.Items), got.Cursor))
		return
	}

	got, err := rt.Captures.ListCaptures(r.Context(), userID, filter, opts)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page(capturesOf(got.Items), got.Cursor))
}

// beginCapture writes the capture row and returns presigned PUTs. It contacts no
// provider and reads no object: the upload event drives the pipeline
// asynchronously in the worker, because API Gateway caps an integration at 30
// seconds and a speech-to-text plus LLM run does not fit inside that.
func (rt *router) beginCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req captureCreateRequest
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

	// The budget is checked before the URL is issued. Without this a capped
	// tenant uploads a recording, watches the capture sit at spend_capped, and
	// is told nothing about why.
	if rt.Spend != nil {
		capped, err := rt.Spend.Capped(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		if capped {
			fail(w, r, service.ErrSpendCapped)
			return
		}
	}

	created, err := rt.Captures.BeginCapture(r.Context(), userID, service.CaptureRequest{
		NoteID:      req.NoteID,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
		DurationMS:  req.DurationMS,
	})
	if err != nil {
		fail(w, r, err)
		return
	}

	// The upload headers reach the client verbatim. x-amz-tagging is inside the
	// signature, so dropping or rewriting it turns every upload into a 403 —
	// and signing it is the only thing that stops an uploader omitting the
	// retention tag and escaping the lifecycle rule.
	out := CaptureCreated{
		Capture: captureOf(created.Capture),
		Upload:  uploadOf(created.Audio),
	}
	if created.Peaks.URL != "" {
		peaks := uploadOf(created.Peaks)
		out.PeaksUpload = &peaks
	}
	writeJSON(w, http.StatusCreated, out)
}

func (rt *router) getCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	capture, err := rt.Captures.GetCapture(r.Context(), userID, r.PathValue("captureId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, captureOf(*capture))
}

func (rt *router) setCaptureTarget(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req captureTargetRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	if req.NoteID != "" && req.NewNoteTitle != "" {
		httperr.BadRequest(w, r, "supply either note_id or new_note_title, not both")
		return
	}
	if req.NewNoteTitle != "" {
		if err := checkTitle(req.NewNoteTitle); answerValidation(w, r, err) {
			return
		}
	}

	capture, err := rt.Captures.SetCaptureTarget(r.Context(), userID, r.PathValue("captureId"), req.NoteID, req.NewNoteTitle)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, captureOf(*capture))
}

// retryCapture hands a failed capture back to the worker and returns 202.
//
// It does no work inline. v1 ran the whole pipeline on the request path here,
// which is exactly how a gateway timeout turned into duplicated note content:
// the Lambda kept going, the client retried, and the append ran twice.
func (rt *router) retryCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	if rt.Spend != nil {
		capped, err := rt.Spend.Capped(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		if capped {
			fail(w, r, service.ErrSpendCapped)
			return
		}
	}

	capture, err := rt.Captures.RetryCapture(r.Context(), userID, r.PathValue("captureId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, captureOf(*capture))
}

// downloadKinds is the fixed set. segments is the timestamped raw transcript
// that drives tap-to-seek; peaks is the precomputed amplitude envelope, so
// drawing a waveform does not mean downloading the whole recording.
var downloadKinds = map[string]bool{
	"audio": true, "raw": true, "clean": true, "segments": true, "peaks": true,
}

func (rt *router) downloadCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		httperr.BadRequest(w, r, "kind is required")
		return
	}
	if !downloadKinds[kind] {
		httperr.BadRequest(w, r, "kind must be one of audio, raw, clean, segments, peaks")
		return
	}

	url, err := rt.Captures.GetDownloadURL(r.Context(), userID, r.PathValue("captureId"), kind)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, downloadResponse{
		URL:       url,
		ExpiresAt: model.FormatTime(time.Now().Add(service.DownloadTTL)),
	})
}

// deleteCapture removes one recording and the paragraph it dictated.
//
// The service cuts the paragraph, refreshes the note index, unlinks the
// capture's objects and deletes its row, in that order, so a failure part-way
// leaves the capture visible and the delete retryable. A second call is 404.
func (rt *router) deleteCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if err := rt.Captures.DeleteCapture(r.Context(), userID, r.PathValue("captureId")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// moveCapture relocates one recording, and the paragraph it dictated, to
// another note. The paragraph lands among the target's recordings in
// chronological order, not at the end. 200 with the re-pointed capture; 204
// when it was already there.
func (rt *router) moveCapture(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	var req captureMoveRequest
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	if req.NoteID == "" {
		httperr.BadRequest(w, r, "note_id is required")
		return
	}

	capture, moved, err := rt.Captures.MoveCapture(r.Context(), userID, r.PathValue("captureId"), req.NoteID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !moved {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, captureOf(*capture))
}
