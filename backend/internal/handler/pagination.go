package handler

import (
	"net/http"
	"strconv"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// listOptionsFrom reads cursor pagination parameters off the query string.
//
// The response body shape is unchanged — a bare JSON array — and the cursor for
// the next page travels in X-Next-Cursor. Phase 6 replaces the envelope; this
// exists so that no list can silently truncate in the meantime.
func listOptionsFrom(r *http.Request) repository.ListOptions {
	opts := repository.ListOptions{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 32); err == nil && n > 0 {
			opts.Limit = int32(n)
		}
	}
	return opts
}

// setNextCursor advertises the next page, if there is one.
func setNextCursor(w http.ResponseWriter, cursor string) {
	if cursor != "" {
		w.Header().Set("X-Next-Cursor", cursor)
	}
}
