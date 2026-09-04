package handler

// routes is the whole HTTP surface, in one readable table.
//
// Every pattern is a Go 1.22 method-and-wildcard pattern, so the router matches
// the method and extracts the identifier. v1 hand-parsed each path with
// strings.TrimPrefix and strings.Split inside three separate handlers, which is
// how "/v1/captures/{id}/complete" and "/v1/captures/{id}/retry" ended up
// dispatching to the same function by accident.
//
// The version prefix appears once, here.
func (rt *router) routes() {
	p := APIPrefix

	// Health. Liveness answers whether the process is up; readiness probes the
	// dependencies. v1 had one static answer serving as both, which stayed
	// green through a DynamoDB outage.
	rt.handle("GET "+p+"/health", rt.health, public())
	rt.handle("GET "+p+"/health/ready", rt.ready, public())

	// Settings.
	rt.handle("GET "+p+"/settings", rt.getSettings)
	rt.handle("PUT "+p+"/settings", rt.putSettings, idempotent())

	// Notes. The literal segments are registered before the wildcard; Go's mux
	// prefers the more specific pattern, so "/v1/notes/match" is not read as a
	// note whose id is "match".
	rt.handle("GET "+p+"/notes", rt.listNotes)
	rt.handle("POST "+p+"/notes", rt.createNote, idempotent(), body(MaxNoteRequestBytes))
	rt.handle("POST "+p+"/notes/match", rt.matchNotes)
	// Before "/notes/{noteId}" in the table for readability only: ServeMux
	// prefers the more specific literal pattern regardless of order.
	rt.handle("POST "+p+"/notes/purge", rt.purgeNotes, idempotent(), body(MaxNoteRequestBytes))
	rt.handle("GET "+p+"/notes/{noteId}", rt.getNote)
	rt.handle("PATCH "+p+"/notes/{noteId}", rt.updateNote, idempotent(), body(MaxNoteRequestBytes))
	rt.handle("DELETE "+p+"/notes/{noteId}", rt.archiveNote)
	rt.handle("POST "+p+"/notes/{noteId}/restore", rt.restoreNote, idempotent())
	rt.handle("DELETE "+p+"/notes/{noteId}/permanent", rt.purgeNote)

	// Tags and search.
	rt.handle("GET "+p+"/tags", rt.listTags)
	rt.handle("GET "+p+"/search", rt.search)

	// Usage: the caller's own provider spend, by month.
	rt.handle("GET "+p+"/usage", rt.getUsage)

	// Captures. POST /v1/captures is synchronous and fast: it writes the row and
	// returns presigned PUTs. Nothing slow happens on a request path bounded by
	// the gateway's fixed 30-second integration ceiling.
	rt.handle("GET "+p+"/captures", rt.listCaptures)
	rt.handle("POST "+p+"/captures", rt.beginCapture, idempotent())
	rt.handle("GET "+p+"/captures/{captureId}", rt.getCapture)
	rt.handle("POST "+p+"/captures/{captureId}/target", rt.setCaptureTarget, idempotent())
	rt.handle("POST "+p+"/captures/{captureId}/retry", rt.retryCapture, idempotent())
	rt.handle("GET "+p+"/captures/{captureId}/download", rt.downloadCapture)

	// Export.
	rt.handle("POST "+p+"/export", rt.startExport, idempotent())
	rt.handle("GET "+p+"/export/{exportId}", rt.getExport)
}
