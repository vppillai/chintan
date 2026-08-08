package handler

import (
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
)

// startExport produces a full export of the caller's data and returns its job
// record.
//
// 202 rather than 200: the contract promises a job to poll, so the endpoint can
// become genuinely asynchronous — a queue and a worker — without the client
// changing. Today the work happens inline and the job is already ready.
func (rt *router) startExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Export == nil {
		httperr.ServiceUnavailable(w, r, "export is not configured on this instance")
		return
	}

	job, err := rt.Export.Start(r.Context(), userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (rt *router) getExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Export == nil {
		httperr.ServiceUnavailable(w, r, "export is not configured on this instance")
		return
	}

	// The export id is part of an object key, so an id belonging to another
	// tenant simply addresses a key under that tenant's prefix and is absent.
	job, err := rt.Export.Get(r.Context(), userID, r.PathValue("exportId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
