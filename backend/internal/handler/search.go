package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
)

// searchHit is the OpenAPI SearchPage item.
type searchHit struct {
	NoteID    string   `json:"note_id"`
	Title     string   `json:"title"`
	Excerpt   string   `json:"excerpt,omitempty"`
	MatchedIn []string `json:"matched_in,omitempty"`
}

// search runs a paginated query over the caller's own partition.
//
// The query string never reaches a log line or a metric dimension. It is user
// content — often the most revealing content in the system, because a person
// searches for what they are worried about — and the access log records the
// route pattern, not the path.
func (rt *router) search(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Search == nil {
		httperr.ServiceUnavailable(w, r, "search is not configured on this instance")
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		httperr.BadRequest(w, r, "q is required")
		return
	}
	if n := len([]rune(q)); n > MaxSearchQuery {
		httperr.BadRequest(w, r, "q is longer than this API accepts")
		return
	}

	opts, err := listOptions(r)
	if answerValidation(w, r, err) {
		return
	}

	got, err := rt.Search.Search(r.Context(), userID, q, opts)
	if err != nil {
		fail(w, r, err)
		return
	}

	items := make([]searchHit, 0, len(got.Items))
	for _, hit := range got.Items {
		items = append(items, searchHit{
			NoteID:    hit.NoteID,
			Title:     hit.Title,
			Excerpt:   hit.Excerpt,
			MatchedIn: hit.MatchedIn,
		})
	}
	writeJSON(w, http.StatusOK, page(items, got.Cursor))
}

func (rt *router) listTags(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Tags == nil {
		httperr.ServiceUnavailable(w, r, "tags are not configured on this instance")
		return
	}

	tags, err := rt.Tags.List(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The tag list is the whole set by construction — it is bounded by how many
	// distinct tags one person uses — so it carries no cursor.
	writeJSON(w, http.StatusOK, page(tags, ""))
}
