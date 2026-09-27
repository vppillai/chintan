package provider

import (
	"context"

	"github.com/vppillai/chintan/backend/internal/routing"
)

// RouteAction is the destination decision for a dictated capture.
type RouteAction string

const (
	// RouteAppend sends the capture into an existing note.
	RouteAppend RouteAction = "append"
	// RouteNew creates a new note for the capture.
	RouteNew RouteAction = "new"
)

// RouteDecision is where a transcript should go, plus the transcript with any
// spoken routing instruction removed.
type RouteDecision struct {
	Action     RouteAction `json:"action"`
	NoteID     string      `json:"note_id"`
	Title      string      `json:"title"`
	Confidence float64     `json:"confidence"`
	Content    string      `json:"content"`
	// Checklist is the reply's `kind` for a RouteNew decision: true when the
	// speaker named a list or dictated items to tick off, so the note the
	// pipeline creates is a checklist and this same run extracts items
	// instead of cleaning prose. It is always false for an append — an
	// existing note keeps the kind it has (owner feedback 2026-09-27).
	Checklist bool `json:"checklist"`

	// Usage is what the routing call consumed. It is carried on the decision so
	// the breaker can reconcile its reservation against the real cost without a
	// second round trip or a second interface.
	Usage TokenUsage `json:"-"`
}

// Router decides which note a transcript belongs to. language is the code the
// transcript is known to be in, or "" when nothing knows.
type Router interface {
	Route(ctx context.Context, transcript string, candidates []routing.Candidate, language string) (RouteDecision, error)
}
