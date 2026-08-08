package service

import "github.com/vppillai/chintan/backend/internal/model"

// The five statuses the asynchronous pipeline added now live in internal/model
// alongside the rest of the CaptureStatus constants. These are aliases, kept
// because the worker and its tests spell them with a service. prefix and the
// rename would be churn with no reader benefit.
const (
	StatusTranscribing = model.StatusTranscribing
	StatusRouting      = model.StatusRouting
	StatusCleaning     = model.StatusCleaning
	StatusAppending    = model.StatusAppending
	StatusSpendCapped  = model.StatusSpendCapped
)

// CaptureIsTerminal reports whether a capture has reached a state the pipeline
// will not leave on its own.
func CaptureIsTerminal(s model.CaptureStatus) bool {
	switch s {
	case model.StatusAppended, model.StatusNoContent, model.StatusFailed, model.StatusSpendCapped:
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
		model.StatusTranscribing, model.StatusRouting, model.StatusCleaning, model.StatusAppending:
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
