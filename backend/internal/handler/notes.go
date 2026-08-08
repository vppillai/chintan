package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
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
		h.handleNotesList(w, r, userID)
	case path == "/match":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleNotesMatch(w, r, userID)
	case strings.HasPrefix(path, "/") && len(path) > 1:
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && parts[1] == "restore" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			h.restoreNote(w, r, userID, parts[0])
			return
		}
		if len(parts) == 1 {
			h.handleNoteDetail(w, r, userID, parts[0])
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *NotesHandler) handleNotesList(w http.ResponseWriter, r *http.Request, userID string) {
	switch r.Method {
	case http.MethodGet:
		h.listNotes(w, r, userID)
	case http.MethodPost:
		h.createNote(w, r, userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *NotesHandler) handleNoteDetail(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	switch r.Method {
	case http.MethodGet:
		h.getNote(w, r, userID, noteID)
	case http.MethodPatch:
		h.updateNote(w, r, userID, noteID)
	case http.MethodDelete:
		h.deleteNote(w, r, userID, noteID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *NotesHandler) listNotes(w http.ResponseWriter, r *http.Request, userID string) {
	opts := listOptionsFrom(r)

	var (
		page repository.Page[model.NoteIndex]
		err  error
	)
	if r.URL.Query().Get("status") == "archived" {
		page, err = h.notesService.ListArchivedNotes(r.Context(), userID, opts)
	} else {
		page, err = h.notesService.ListNotes(r.Context(), userID, opts)
	}
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	setNextCursor(w, page.Cursor)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(page.Items)
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
		if errors.Is(err, service.ErrEmptyNoteTitle) {
			httperr.BadRequest(w, "title is required")
			return
		}
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(note)
}

func (h *NotesHandler) getNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	note, err := h.notesService.GetNoteDetail(r.Context(), userID, noteID)
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
		if errors.Is(err, service.ErrNoteArchived) {
			httperr.WriteJSON(w, err, http.StatusConflict)
			return
		}
		if errors.Is(err, service.ErrEmptyNoteTitle) {
			httperr.BadRequest(w, "title is required")
			return
		}
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(note)
}

func (h *NotesHandler) deleteNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	var err error
	if r.URL.Query().Get("permanent") == "true" {
		err = h.notesService.PermanentlyDeleteNote(r.Context(), userID, noteID)
		if errors.Is(err, service.ErrNoteNotArchived) {
			httperr.BadRequest(w, err.Error())
			return
		}
	} else {
		err = h.notesService.DeleteNote(r.Context(), userID, noteID)
	}
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *NotesHandler) restoreNote(w http.ResponseWriter, r *http.Request, userID, noteID string) {
	note, err := h.notesService.RestoreNote(r.Context(), userID, noteID)
	if err != nil {
		httperr.WriteJSON(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(note)
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
