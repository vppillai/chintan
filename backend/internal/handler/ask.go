package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// MaxAskRequestBytes bounds POST /v1/ask. A question and six earlier turns
// of a conversation fit comfortably; a client that carries longer answers
// forward is expected to cut them, and is told so with a 413.
const MaxAskRequestBytes = 16 << 10

// askRequest is the OpenAPI AskRequest schema.
type askRequest struct {
	Question string    `json:"question"`
	History  []askTurn `json:"history"`
}

// askTurn is one earlier exchange of the conversation (OpenAPI AskTurn).
type askTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Ask is the OpenAPI Ask schema: a question and, once the worker has run, its
// answer. Every member is present on every response so the client polling
// for the answer never has to guess whether a field is coming.
type Ask struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Question string `json:"question"`
	// Answer is null until answered. Plain text, possibly with simple
	// Markdown (paragraphs, "- " lists, **bold**); never headings.
	Answer *string `json:"answer"`
	// Grounded is true when the answer was drawn from notes and false when
	// the honest answer is "that is not in your notes".
	Grounded bool `json:"grounded"`
	// Sources are the notes that were both given to the model and cited by
	// it, in relevance order. Never null.
	Sources []AskSource `json:"sources"`
	// Error is the fixed sentence when Status is failed, else null. Never a
	// provider's words.
	Error *string `json:"error"`
	// NotesConsidered is how many notes were in the retrieval window.
	NotesConsidered int     `json:"notes_considered"`
	CreatedAt       string  `json:"created_at"`
	AnsweredAt      *string `json:"answered_at"`
}

// AskSource is one cited note.
type AskSource struct {
	NoteID string `json:"note_id"`
	Title  string `json:"title"`
}

func askOf(a model.Ask) Ask {
	out := Ask{
		ID:              a.ID,
		Status:          string(a.Status),
		Question:        a.Question,
		Grounded:        a.Grounded,
		Sources:         make([]AskSource, 0, len(a.Sources)),
		NotesConsidered: a.NotesConsidered,
		CreatedAt:       a.CreatedAt,
	}
	for _, s := range a.Sources {
		out.Sources = append(out.Sources, AskSource{NoteID: s.NoteID, Title: s.Title})
	}
	if a.Status == model.AskAnswered {
		v := a.Answer
		out.Answer = &v
	}
	if a.Error != "" {
		v := a.Error
		out.Error = &v
	}
	if a.AnsweredAt != "" {
		v := a.AnsweredAt
		out.AnsweredAt = &v
	}
	return out
}

// beginAsk validates the question, writes the pending row, hands it to the
// worker and answers 202. Nothing runs inline, for the reason cleanNote gives.
// The spend gate answers first, so a capped instance is told rather than left
// polling a row that will only ever say "daily provider spend cap reached".
func (rt *router) beginAsk(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Ask == nil {
		httperr.ServiceUnavailable(w, r, "ask is not configured on this instance")
		return
	}

	var req askRequest
	if !decodeJSON(w, r, MaxAskRequestBytes, &req) {
		return
	}
	history := make([]model.AskTurn, 0, len(req.History))
	for _, turn := range req.History {
		history = append(history, model.AskTurn{Question: turn.Question, Answer: turn.Answer})
	}
	// Validated here as well as in the service so the 400 carries the field
	// problem and the service's own check stays the one that is load-bearing.
	if _, _, err := service.ValidateAsk(req.Question, history); err != nil {
		httperr.BadRequest(w, r, err.Error())
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

	a, err := rt.Ask.Begin(r.Context(), userID, req.Question, history)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, askOf(a))
}

// getAsk reads one question back. Another tenant's id is simply absent.
func (rt *router) getAsk(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Ask == nil {
		httperr.ServiceUnavailable(w, r, "ask is not configured on this instance")
		return
	}
	a, err := rt.Ask.Get(r.Context(), userID, r.PathValue("askId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, askOf(a))
}
