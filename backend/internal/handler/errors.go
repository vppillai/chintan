package handler

import (
	"errors"
	"net/http"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// fail maps a domain error to a status code.
//
// This is the only place the mapping exists, and it never inspects error text.
// A strings.Contains(err.Error(), "not found") check means rewording an error
// message silently changes an HTTP status code — and a wrapped error mentioning
// a missing S3 object turns an unrelated failure into a 404.
//
// Every arm matches a typed sentinel with errors.Is, so wrapping is safe and
// the compiler notices when a sentinel is removed. Anything unrecognised is a
// 500 whose text is logged and never serialised.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	// ---- not found -------------------------------------------------------
	// Another tenant's identifier lands here too: the store scopes every read
	// to the caller's partition, so a foreign id is simply absent. It must not
	// become a 403, which would confirm the id exists.
	case errors.Is(err, repository.ErrNotFound):
		httperr.NotFound(w, r, "no such resource")

	// ---- conflicts -------------------------------------------------------
	case errors.Is(err, repository.ErrVersionConflict):
		httperr.Conflict(w, r, "the resource changed since you read it; re-read and retry", nil)
	case errors.Is(err, repository.ErrIdempotencyInFlight):
		httperr.Conflict(w, r, "an identical request is still in flight", nil)
	case errors.Is(err, repository.ErrIdempotencyKeyReused):
		httperr.Conflict(w, r, "this Idempotency-Key was used for a different request", nil)
	case errors.Is(err, service.ErrNoteArchived):
		httperr.Conflict(w, r, "the note is archived; restore it first", nil)
	case errors.Is(err, service.ErrCaptureTerminal):
		httperr.Conflict(w, r, "the capture has already finished", nil)
	case errors.Is(err, service.ErrCaptureAlreadyTargeted):
		httperr.Conflict(w, r, "the capture already has a destination note", nil)
	case errors.Is(err, service.ErrCaptureInFlight):
		httperr.Conflict(w, r, "the capture is still being processed; wait for it to finish or fail", nil)
	case errors.Is(err, service.ErrCaptureUnfiled):
		httperr.Conflict(w, r, "the capture has no note yet; choose one with /target instead of moving it", nil)

	// ---- client mistakes -------------------------------------------------
	case errors.Is(err, service.ErrNoteNotArchived):
		httperr.BadRequest(w, r, "archive the note before deleting it permanently")
	case errors.Is(err, service.ErrEmptyNoteTitle):
		httperr.BadRequest(w, r, "title is required")
	case errors.Is(err, service.ErrCaptureTargetRequired):
		httperr.BadRequest(w, r, "supply either note_id or new_note_title")
	case errors.Is(err, service.ErrUnsupportedContentType):
		httperr.BadRequest(w, r, "content_type must be one of audio/webm, audio/mp4, audio/ogg, audio/wav")
	case errors.Is(err, service.ErrDownloadKindUnknown):
		httperr.BadRequest(w, r, "kind must be one of audio, raw, clean, segments, peaks")
	case errors.Is(err, service.ErrInvalidCursor):
		httperr.BadRequest(w, r, "the cursor is not one this API issued")
	case errors.Is(err, service.ErrEmptySearchQuery):
		httperr.BadRequest(w, r, "q is required")
	case errors.Is(err, service.ErrInvalidCleanupMode),
		errors.Is(err, service.ErrInvalidRetentionDays),
		errors.Is(err, service.ErrInvalidTheme),
		errors.Is(err, service.ErrInvalidLanguage):
		httperr.BadRequest(w, r, err.Error())

	// ---- limits ----------------------------------------------------------
	case errors.Is(err, service.ErrCaptureTooLarge):
		httperr.PayloadTooLarge(w, r, "the recording is larger than this API accepts")
	case errors.Is(err, service.ErrSpendCapped):
		// Distinct from a generic failure so the UI can explain a budget
		// decision instead of inviting a retry that will fail the same way.
		//
		// The distinction lives in `type`, not in the title. This 429 and the
		// gateway's throttling 429 both say "Too Many Requests", and the
		// client's rule — back off on a rate limit, stop on a budget — cannot
		// be derived from prose.
		capped := httperr.New(http.StatusTooManyRequests, "the daily provider spend cap has been reached")
		capped.Type = httperr.TypeSpendCapped
		httperr.Write(w, r, capped)

	// ---- not configured on this instance ---------------------------------
	case errors.Is(err, service.ErrCaptureWorkerUnavailable):
		httperr.ServiceUnavailable(w, r, "the capture pipeline is not configured on this instance")
	case errors.Is(err, service.ErrNoteCreationUnavailable):
		httperr.ServiceUnavailable(w, r, "creating a note from a capture is not configured on this instance")

	// ---- rolled back, repeat the request ----------------------------------
	case errors.Is(err, service.ErrMoveIncomplete):
		// The source note was restored and the capture still points at it, so
		// the same request can simply be sent again. The type URI is what
		// tells the client that, since a 503 can also mean "not configured".
		httperr.Retryable(w, r, "the recording could not be moved and nothing was changed; try again")

	// ---- ours ------------------------------------------------------------
	case errors.Is(err, service.ErrPurgeIncomplete):
		// The note index survives on purpose, so the delete can be retried
		// rather than reporting success over orphaned audio.
		httperr.InternalServerError(w, r, err)

	default:
		httperr.InternalServerError(w, r, err)
	}
}

// failConflictAt is fail's companion for the one case that carries state: an
// optimistic-concurrency loss, where the client needs the current version to
// reconcile without a second round trip.
func failConflictAt(w http.ResponseWriter, r *http.Request, err error, currentVersion int64) {
	if errors.Is(err, repository.ErrVersionConflict) {
		v := currentVersion
		httperr.Conflict(w, r, "the resource changed since you read it; re-read and retry", &v)
		return
	}
	fail(w, r, err)
}
