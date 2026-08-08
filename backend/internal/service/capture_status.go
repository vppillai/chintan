package service

import "github.com/vppillai/chintan/backend/internal/model"

// The v2 capture statuses.
//
// `uploaded`, `transcribed`, `cleaned`, `appended`, `failed`, `needs_target` and
// `no_content` already exist in internal/model. These are the ones the async
// pipeline adds: the in-progress stages the frontend's progress card polls, and
// the distinct outcome a spend cap produces.
//
// They are declared here rather than in internal/model only because this phase
// does not own that package. Phase 6 should promote them alongside the rest of
// the CaptureStatus constants; model.CaptureStatus is a string type, so moving
// them changes no stored value and no wire representation.
const (
	// StatusTranscribing means the recording is with the speech provider.
	StatusTranscribing model.CaptureStatus = "transcribing"
	// StatusRouting means the destination note is being decided.
	StatusRouting model.CaptureStatus = "routing"
	// StatusCleaning means the transcript is with the cleanup model.
	StatusCleaning model.CaptureStatus = "cleaning"
	// StatusAppending means the append claim is held and the text is going into
	// the note body.
	StatusAppending model.CaptureStatus = "appending"
	// StatusSpendCapped means the tenant's daily provider spend cap stopped the
	// call. It is deliberately distinct from `failed` so the UI can explain a
	// budget decision rather than report a fault.
	StatusSpendCapped model.CaptureStatus = "spend_capped"
)

// CaptureIsTerminal reports whether a capture has reached a state the pipeline
// will not leave on its own.
func CaptureIsTerminal(s model.CaptureStatus) bool {
	switch s {
	case model.StatusAppended, model.StatusNoContent, model.StatusFailed, StatusSpendCapped:
		return true
	default:
		return false
	}
}

// CaptureIsPending reports whether a capture is still moving through the
// pipeline. It backs GET /v1/captures?status=pending, which is what lets the
// progress card survive a reload.
func CaptureIsPending(s model.CaptureStatus) bool {
	switch s {
	case model.StatusUploaded, model.StatusTranscribed, model.StatusCleaned,
		StatusTranscribing, StatusRouting, StatusCleaning, StatusAppending:
		return true
	default:
		return false
	}
}

// Snippet re-derives a note's list snippet. Exported for the worker, which
// refreshes the index after an append.
func Snippet(body string) string { return generateSnippet(body) }

// SanitizeTitle bounds a dictated title to one line. Exported for the worker,
// which honours titles the router took from speech.
func SanitizeTitle(title string) string { return sanitizeNoteTitle(title) }
