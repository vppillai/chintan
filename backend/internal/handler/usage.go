package handler

import (
	"net/http"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Usage is the OpenAPI Usage schema: one tenant's provider usage for one
// month, and beside it the instance's AWS cost for the same month.
//
// The provider half is the storage row's own counters, which is the exception
// to wire.go's rule and a deliberate one: the row holds nothing but numbers the
// caller is entitled to see, and there is no S3 key or internal id on it to
// leak. The AWS half is why this is a wire type rather than an alias of
// usage.Month: it comes from a different row, on the instance partition, and
// the two are only joined here.
type Usage struct {
	Month string `json:"month"`
	usage.Totals
	Ops  map[string]usage.Totals `json:"ops"`
	Days []usage.Day             `json:"days"`
	// AWS is the instance's AWS spend for the month as last read from the
	// stack's budget by the worker's daily task, or null when nothing has been
	// recorded for the month: the stack has no budget, or the task has not run
	// since the month began. It is instance-level — the account's bill, not
	// this caller's share of it — so every tenant sees the same figure.
	AWS *usage.AWSCost `json:"aws"`
}

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
	awsCost, err := rt.Usage.AWSCost(r.Context(), month)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Usage{
		Month:  got.Month,
		Totals: got.Totals,
		Ops:    got.Ops,
		Days:   got.Days,
		AWS:    awsCost,
	})
}
