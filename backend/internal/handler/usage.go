package handler

import (
	"net/http"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Usage is the OpenAPI Usage schema: one tenant's provider usage for one
// month. It is the storage type serialised directly, which is the exception to
// wire.go's rule and a deliberate one: the row holds nothing but counters the
// caller is entitled to see, and there is no S3 key or internal id on it to
// leak. If that ever stops being true, this gets its own wire type.
type Usage = usage.Month

// getUsage answers the caller's own usage for one month. It is what a "You"
// screen shows and what a future admin listing aggregates; it deliberately
// carries no per-capture detail — the month, its per-op split and one line per
// day is small enough to fetch on every visit and is all a person can act on.
func (rt *router) getUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Usage == nil {
		httperr.ServiceUnavailable(w, r, "usage accounting is not configured on this instance")
		return
	}

	month := r.URL.Query().Get("month")
	if month == "" {
		// UTC, the same calendar the worker bills to. A user west of Greenwich
		// asking on the evening of the 31st is shown the month the calls were
		// counted in, which is the one the numbers add up in.
		month = time.Now().UTC().Format("2006-01")
	}
	if !usage.ValidMonth(month) {
		httperr.BadRequest(w, r, "month must be yyyy-mm")
		return
	}

	got, err := rt.Usage.Month(r.Context(), userID, month)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
