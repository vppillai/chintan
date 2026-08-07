package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
)

// NotesHandler handles notes requests
type NotesHandler struct {
	notesService *service.NotesService
}

// NewNotesHandler creates a new notes handler
func NewNotesHandler(notesService *service.NotesService) *NotesHandler {
	return &NotesHandler{
		notesService: notesService,
	}
}

// ServeHTTP handles notes requests
func (h *NotesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, "authentication required")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/notes")
	
	switch {
	case path == "" || path == "/":
		// /v1/notes
		h.handleNotesList(w, r, userID)
	case path == "/match":
		// /v1/notes/match
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleNotesMatch(w, r, userID)
	case strings.HasPrefix(path, "/") && len(path) > 1:
		// /v1/notes/{id}
		noteID := strings.TrimPrefix(path, "/")
		h.handleNoteDetail(w, r, userID, noteID)
	default:
		http.NotFound(w, r)
	}
}

func (h *NotesHandler) handleNotesList(w http.ResponseWriter, r *http.Request, userID string) {
	switch r.Method {
	case "GET":
		h.listNotes(w, r, userID)
	case "POST":
		h.createNote(w, r, userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *NotesHandler) handleNoteDetail(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	switch r.Method {
	case "GET":
		h.getNote(w, r, userID, noteID)
	case "PATCH":
		h.updateNote(w, r, userID, noteID)
	case "DELETE":
		h.deleteNote(w, r, userID, noteID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *NotesHandler) listNotes(w http.ResponseWriter, r *http.Request, userID string) {
	notes, err := h.notesService.ListNotes(r.Context(), userID)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(notes)
}

func (h *NotesHandler) createNote(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		Title   string   `json:"title"`
		Aliases []string `json:"aliases"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid JSON")
		return
	}

	if req.Title == "" {
		httperr.BadRequest(w, "title is required")
		return
	}

	note, err := h.notesService.CreateNote(r.Context(), userID, req.Title, req.Aliases)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(note)
}

func (h *NotesHandler) getNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	note, err := h.notesService.GetNote(r.Context(), userID, noteID)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(note)
}

func (h *NotesHandler) updateNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	var req struct {
		Title   *string   `json:"title"`
		Aliases *[]string `json:"aliases"`
		Body    *string   `json:"body"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid JSON")
		return
	}

	updates := service.NoteUpdates{
		Title:   req.Title,
		Aliases: req.Aliases,
		Body:    req.Body,
	}

	note, err := h.notesService.UpdateNote(r.Context(), userID, noteID, updates)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(note)
}

func (h *NotesHandler) deleteNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	err := h.notesService.DeleteNote(r.Context(), userID, noteID)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *NotesHandler) handleNotesMatch(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		Query string `json:"query"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.BadRequest(w, "invalid JSON")
		return
	}

	if req.Query == "" {
		httperr.BadRequest(w, "query is required")
		return
	}

	result, err := h.notesService.MatchNotes(r.Context(), userID, req.Query)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}