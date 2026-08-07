package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

// CapturesHandler handles capture-related HTTP requests
type CapturesHandler struct {
	captureService *service.CaptureService
}

// NewCapturesHandler creates a new captures handler
func NewCapturesHandler(captureService *service.CaptureService) *CapturesHandler {
	return &CapturesHandler{
		captureService: captureService,
	}
}

// CreateCaptureRequest represents the request body for creating a capture.
// NoteID is optional: when omitted, the destination note is decided from what
// the speaker said once the audio has been transcribed.
type CreateCaptureRequest struct {
	NoteID      string `json:"note_id"`
	ContentType string `json:"content_type"`
}

// SetCaptureTargetRequest chooses the destination for a capture awaiting one.
type SetCaptureTargetRequest struct {
	NoteID       string `json:"note_id"`
	NewNoteTitle string `json:"new_note_title"`
}

// CreateCaptureResponse represents the response for creating a capture
type CreateCaptureResponse struct {
	CaptureID string `json:"capture_id"`
	UploadURL string `json:"upload_url"`
}

// DownloadResponse represents a download URL response
type DownloadResponse struct {
	URL string `json:"url"`
}

func (h *CapturesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, userID)
	case http.MethodGet:
		h.handleGet(w, r, userID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *CapturesHandler) handlePost(w http.ResponseWriter, r *http.Request, userID string) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/captures")

	if path == "" || path == "/" {
		// POST /v1/captures - create capture
		h.createCapture(w, r, userID)
		return
	}

	// POST /v1/captures/{id}/complete or /v1/captures/{id}/retry
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	captureID := parts[0]
	action := parts[1]

	switch action {
	case "complete":
		h.completeCapture(w, r, userID, captureID)
	case "retry":
		// Retry is the same as complete for now (idempotent)
		h.completeCapture(w, r, userID, captureID)
	case "target":
		h.setCaptureTarget(w, r, userID, captureID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *CapturesHandler) setCaptureTarget(w http.ResponseWriter, r *http.Request, userID, captureID string) {
	var req SetCaptureTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid request body")
		return
	}

	capture, err := h.captureService.SetCaptureTarget(r.Context(), userID, captureID, req.NoteID, req.NewNoteTitle)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "not found"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(err.Error(), "already targets"):
			httperr.WriteJSON(w, err, http.StatusConflict)
		case strings.Contains(err.Error(), "is required"):
			httperr.BadRequest(w, err.Error())
		default:
			httperr.InternalServerError(w, err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(capture)
}

func (h *CapturesHandler) handleGet(w http.ResponseWriter, r *http.Request, userID string) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/captures")
	path = strings.Trim(path, "/")

	// GET /v1/captures?note_id=...
	if path == "" {
		noteID := r.URL.Query().Get("note_id")
		if noteID == "" {
			httperr.BadRequest(w, "note_id query parameter is required")
			return
		}
		captures, err := h.captureService.ListCapturesForNote(r.Context(), userID, noteID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				httperr.WriteJSON(w, err, http.StatusNotFound)
				return
			}
			httperr.InternalServerError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(captures)
		return
	}

	parts := strings.Split(path, "/")

	// GET /v1/captures/{id}
	if len(parts) == 1 {
		capture, err := h.captureService.GetCapture(r.Context(), userID, parts[0])
		if err != nil {
			httperr.WriteJSON(w, err, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(capture)
		return
	}

	// GET /v1/captures/{id}/download?kind=audio|raw|clean
	if len(parts) != 2 || parts[1] != "download" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	captureID := parts[0]
	kind := r.URL.Query().Get("kind")

	if kind == "" {
		httperr.BadRequest(w, "missing kind parameter")
		return
	}

	if kind != "audio" && kind != "raw" && kind != "clean" {
		httperr.BadRequest(w, "invalid kind, must be audio, raw, or clean")
		return
	}

	h.getDownload(w, r, userID, captureID, kind)
}

func (h *CapturesHandler) createCapture(w http.ResponseWriter, r *http.Request, userID string) {
	var req CreateCaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid request body")
		return
	}

	if req.ContentType == "" {
		httperr.BadRequest(w, "content_type is required")
		return
	}

	capture, uploadURL, err := h.captureService.CreateCapture(r.Context(), userID, req.NoteID, req.ContentType)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		httperr.InternalServerError(w, err)
		return
	}

	resp := CreateCaptureResponse{
		CaptureID: capture.ID,
		UploadURL: uploadURL,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *CapturesHandler) completeCapture(w http.ResponseWriter, r *http.Request, userID, captureID string) {
	capture, err := h.captureService.CompleteCapture(r.Context(), userID, captureID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		httperr.InternalServerError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(capture)
}

func (h *CapturesHandler) getDownload(w http.ResponseWriter, r *http.Request, userID, captureID, kind string) {
	url, err := h.captureService.GetDownloadURL(r.Context(), userID, captureID, kind)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(err.Error(), "no "+kind+" file available") {
			httperr.BadRequest(w, fmt.Sprintf("no %s file available", kind))
			return
		}
		httperr.InternalServerError(w, err)
		return
	}

	resp := DownloadResponse{URL: url}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
