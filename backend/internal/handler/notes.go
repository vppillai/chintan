package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// noteCreateRequest is the OpenAPI NoteCreate schema.
type noteCreateRequest struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Aliases []string `json:"aliases"`
	Tags    []string `json:"tags"`
}

// noteUpdateRequest is the OpenAPI NoteUpdate schema.
//
// Version is a value, not a pointer, and it is required: optimistic concurrency
// that a client can opt out of by omitting a field is optimistic concurrency
// that does not exist.
type noteUpdateRequest struct {
	Version  *int64    `json:"version"`
	Title    *string   `json:"title"`
	Body     *string   `json:"body"`
	Aliases  *[]string `json:"aliases"`
	Tags     *[]string `json:"tags"`
	Verbatim *bool     `json:"verbatim"`
}

func (rt *router) listNotes(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	opts, err := listOptions(r)
	if answerValidation(w, r, err) {
		return
	}

	state := r.URL.Query().Get("state")
	var got repository.Page[model.NoteIndex]
	switch state {
	case "", "active":
		got, err = rt.Notes.ListNotes(r.Context(), userID, opts)
	case "archived":
		got, err = rt.Notes.ListArchivedNotes(r.Context(), userID, opts)
	default:
		httperr.BadRequest(w, r, "state must be active or archived")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}

	items := got.Items
	// The tag filter is applied after the page, exactly as a DynamoDB
	// FilterExpression would be: a page can legitimately come back short with a
	// cursor still set, and the client keeps paging.
	if tag := r.URL.Query().Get("tag"); tag != "" {
		if len([]rune(tag)) > MaxTagRunes {
			httperr.BadRequest(w, r, "tag is longer than this API stores")
			return
		}
		filtered := items[:0:0]
		for _, n := range items {
			for _, t := range n.Tags {
				if t == tag {
					filtered = append(filtered, n)
					break
				}
			}
		}
		items = filtered
	}

	writeJSON(w, http.StatusOK, page(notesOf(items), got.Cursor))
}

func (rt *router) createNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req noteCreateRequest
	if !decodeJSON(w, r, MaxNoteRequestBytes, &req) {
		return
	}
	if answerValidation(w, r, validateNoteFields(&req.Title, &req.Body, &req.Aliases, &req.Tags)) {
		return
	}

	note, err := rt.Notes.CreateNoteWithTags(r.Context(), userID, req.Title, req.Aliases, req.Tags)
	if err != nil {
		fail(w, r, err)
		return
	}

	// A body supplied at creation is written through the same path an edit
	// takes, so there is one place that maintains the snippet and the meta
	// mirror.
	if req.Body != "" {
		version := note.Version
		updated, err := rt.Notes.UpdateNote(r.Context(), userID, note.ID, service.NoteUpdates{
			Body:            &req.Body,
			ExpectedVersion: &version,
		})
		if err != nil {
			fail(w, r, err)
			return
		}
		note = updated
	}

	writeJSON(w, http.StatusCreated, noteOf(note))
}

func (rt *router) getNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	noteID := r.PathValue("noteId")

	detail, err := rt.Notes.GetNoteDetail(r.Context(), userID, noteID)
	if err != nil {
		fail(w, r, err)
		return
	}

	out := NoteDetail{Note: noteOf(detail.NoteIndex), Body: detail.Body, Captures: []Capture{}}
	if rt.Captures != nil {
		captures, err := rt.Captures.ListCapturesForNote(r.Context(), userID, noteID, repository.ListOptions{})
		if err != nil {
			fail(w, r, err)
			return
		}
		out.Captures = capturesOf(captures.Items)
	}
	writeJSON(w, http.StatusOK, out)
}

func (rt *router) updateNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	noteID := r.PathValue("noteId")

	var req noteUpdateRequest
	if !decodeJSON(w, r, MaxNoteRequestBytes, &req) {
		return
	}
	if req.Version == nil {
		httperr.BadRequest(w, r, "version is required; read the note first and send the version you saw")
		return
	}
	if answerValidation(w, r, validateNoteFields(req.Title, req.Body, req.Aliases, req.Tags)) {
		return
	}

	updated, err := rt.Notes.UpdateNote(r.Context(), userID, noteID, service.NoteUpdates{
		Title:           req.Title,
		Body:            req.Body,
		Aliases:         req.Aliases,
		Tags:            req.Tags,
		Verbatim:        req.Verbatim,
		ExpectedVersion: req.Version,
	})
	if err != nil {
		// A conflict carries the version the client should reconcile against,
		// so the editor does not have to re-read to find out what it lost to.
		if errors.Is(err, repository.ErrVersionConflict) {
			failConflictAt(w, r, err, updated.Version)
			return
		}
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noteOf(updated))
}

func (rt *router) archiveNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if _, err := rt.Notes.ArchiveNote(r.Context(), userID, r.PathValue("noteId")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *router) restoreNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	note, err := rt.Notes.RestoreNote(r.Context(), userID, r.PathValue("noteId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noteOf(note))
}

// purgeNote deletes a note and every artifact it owns, irreversibly.
//
// A partial cascade returns 500 and leaves the note index in place so the
// delete can be retried. v1 logged each cascade failure, ignored it, and
// deleted the index anyway — reporting "purged" over audio that survived in S3
// with nothing left pointing at it.
func (rt *router) purgeNote(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if err := rt.Notes.PermanentlyDeleteNote(r.Context(), userID, r.PathValue("noteId")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// purgeNotes permanently deletes a batch of archived notes.
//
// A batch endpoint exists for purge and for nothing else. Archive and restore
// are reversible and cheap, so a client does those as N ordinary calls; a purge
// cascades to every capture's audio, raw transcript, routed transcript, cleaned
// text, segments and peaks, and driving a hundred of those from a phone means a
// hundred round trips that all have to survive the connection, with a partial
// failure leaving objects orphaned and nothing tracking which.
//
// It answers 200 with a result per note even when some of them failed. There is
// no transaction spanning DynamoDB and S3, so a single verdict for the batch
// would be a claim the server cannot make.
func (rt *router) purgeNotes(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req NotePurgeRequest
	if !decodeJSON(w, r, MaxNoteRequestBytes, &req) {
		return
	}

	results, err := rt.Notes.PurgeNotes(r.Context(), userID, req.NoteIDs)
	switch {
	case errors.Is(err, service.ErrPurgeBatchEmpty):
		httperr.BadRequest(w, r, "note_ids must name at least one note")
		return
	case errors.Is(err, service.ErrPurgeBatchTooLarge):
		httperr.BadRequest(w, r, fmt.Sprintf("note_ids may name at most %d notes in one request", service.MaxPurgeBatch))
		return
	case err != nil:
		fail(w, r, err)
		return
	}

	out := make([]NotePurgeResult, 0, len(results))
	for _, res := range results {
		out = append(out, NotePurgeResult{NoteID: res.NoteID, Status: res.Status, Detail: res.Detail})
	}
	writeJSON(w, http.StatusOK, NotePurgeResponse{Results: out})
}

func (rt *router) matchNotes(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}

	var req struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, MaxSmallRequestBytes, &req) {
		return
	}
	if req.Query == "" {
		httperr.BadRequest(w, r, "query is required")
		return
	}
	if n := len([]rune(req.Query)); n > MaxMatchQuery {
		httperr.BadRequest(w, r, "query is longer than this API accepts")
		return
	}

	result, err := rt.Notes.MatchNotes(r.Context(), userID, req.Query)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matchResponseOf(result))
}

// matchResponse is the OpenAPI MatchResponse schema.
type matchResponse struct {
	Confidence   float64          `json:"confidence"`
	AutoSelected bool             `json:"auto_selected"`
	Candidates   []matchCandidate `json:"candidates"`
	// AutoSelectID is retained alongside auto_selected because the routing
	// worker reads it. Dropping it would be a silent break for a caller the
	// OpenAPI does not describe.
	AutoSelectID *string `json:"auto_select_id,omitempty"`
}

type matchCandidate struct {
	NoteID string  `json:"note_id"`
	Title  string  `json:"title"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

func matchResponseOf(result service.MatchResult) matchResponse {
	out := matchResponse{
		Candidates:   make([]matchCandidate, 0, len(result.Candidates)),
		AutoSelected: result.AutoSelectID != nil,
		AutoSelectID: result.AutoSelectID,
	}
	for _, c := range result.Candidates {
		out.Candidates = append(out.Candidates, matchCandidate{
			NoteID: c.NoteID,
			Title:  c.Title,
			Score:  c.Score,
		})
	}
	if len(out.Candidates) > 0 {
		out.Confidence = clamp01(out.Candidates[0].Score)
	}
	return out
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// validateNoteFields applies the field caps. Every argument is optional, so one
// function serves both create and update.
func validateNoteFields(title, body *string, aliases, tags *[]string) error {
	if title != nil {
		if err := checkTitle(*title); err != nil {
			return err
		}
	}
	if body != nil {
		if err := checkBody(*body); err != nil {
			return err
		}
	}
	if aliases != nil {
		if err := checkStrings("aliases", *aliases, MaxAliases, MaxAliasRunes); err != nil {
			return err
		}
	}
	if tags != nil {
		if err := checkStrings("tags", *tags, MaxTags, MaxTagRunes); err != nil {
			return err
		}
	}
	return nil
}
