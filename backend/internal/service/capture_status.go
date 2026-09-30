package service

import (
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

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
// will not leave on its own. It is model.IsTerminalStatus under the name the
// worker and its tests use.
func CaptureIsTerminal(s model.CaptureStatus) bool { return model.IsTerminalStatus(s) }

// CaptureStuckAfter is how long a capture may go without the worker writing
// its row before the API treats the worker as gone and lets a retry start a
// new run. It is the worker function's Timeout (900 s in
// infrastructure/template.yaml): a worker that is alive writes the row at
// every stage and no stage outlives the function, so a row older than this
// has nobody working on it. A retry before this would start a second delivery
// beside a live one — two transcriptions billed, the append kept exactly-once
// by the claim — which is what the API refused only by luck before (review
// 2026-09-05, S12). A capture in `appending` is bounded by the append claim's
// lease instead (CaptureStuck): a retry inside the lease cannot take the claim
// and only dead-letters (S11).
const CaptureStuckAfter = 15 * time.Minute

// CaptureStuck reports whether an in-flight capture has been left long enough
// that no worker can still be working on it, so a retry is a fresh start and
// not a second delivery.
func CaptureStuck(c model.CaptureIndex, now time.Time) bool {
	if c.Status == model.StatusAppending && c.AppendClaimedAt > 0 &&
		now.Sub(time.Unix(c.AppendClaimedAt, 0)) < repository.AppendClaimLease {
		return false
	}
	last := c.LastProgressAt
	if last == "" {
		last = c.CreatedAt
	}
	at, err := model.ParseTime(last)
	if err != nil {
		// A row with no readable time is not one to keep refusing on.
		return true
	}
	return now.Sub(at) >= CaptureStuckAfter
}

// uploadLandMargin is how long past its presigned PUT's expiry an upload is
// still given to land: a PUT that starts a second before the link expires is
// still accepted, and a long recording on a slow phone takes minutes to send.
const uploadLandMargin = 5 * time.Minute

// uploadMayStillLand reports whether an `uploaded` capture is young enough
// that the PUT issued with it could still arrive: its link is good for
// uploadTTL from the row's creation. Until then an absent object is an upload
// in progress, not one that never finished, and the row must neither be
// deleted — the device's PUT would land, the device would prune its copy, and
// the worker would remove the object as an orphan, losing the recording
// without a word — nor declared never uploaded. A row with no readable
// creation time is treated as old, as CaptureStuck does.
func uploadMayStillLand(c model.CaptureIndex, now time.Time) bool {
	created, err := model.ParseTime(c.CreatedAt)
	if err != nil {
		return false
	}
	return now.Sub(created) < uploadTTL+uploadLandMargin
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

// SanitizeTitle bounds a dictated title to one line. Exported for the worker,
// which honours titles the router took from speech.
func SanitizeTitle(title string) string { return sanitizeNoteTitle(title) }
