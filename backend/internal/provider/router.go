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

	// Usage is what the routing call consumed. It is carried on the decision so
	// the breaker can reconcile its reservation against the real cost without a
	// second round trip or a second interface.
	Usage TokenUsage `json:"-"`
}

// Router decides which note a transcript belongs to.
type Router interface {
	Route(ctx context.Context, transcript string, candidates []routing.Candidate) (RouteDecision, error)
}
