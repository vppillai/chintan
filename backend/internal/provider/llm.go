package provider

import (
	"context"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/model"
)

// TokenUsage is what a completion consumed, as the provider reported it.
//
// It carries counts and nothing else: the breaker prices a call from these, and
// a count can never leak what was said.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// Cleaned is the result of a cleanup call.
type Cleaned struct {
	Text  string
	Usage TokenUsage
}

// Answer is the result of an ask call: the model's answer, the note ids it
// cited (unfiltered — the caller keeps only the notes it packed), and whether
// the model says the notes held the answer.
type Answer struct {
	Text     string
	Sources  []string
	Grounded bool
	Usage    TokenUsage
}

// LLM interface for text cleanup/processing
type LLM interface {
	Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (Cleaned, error)
	// CleanNote rewrites a whole note body (append markers already stripped)
	// as one document in the given mode. The caller bounds the body and
	// checks the answer with cleanup.NoteOutput.
	CleanNote(ctx context.Context, mode model.NoteCleanMode, body string) (Cleaned, error)
	// Ask answers a question from the packed notes in q (backlog D5). The
	// caller bounds the notes and the question; the adapter renders the
	// prompt, makes one completion and decodes it with ask.ParseAnswer.
	Ask(ctx context.Context, q ask.Prompt) (Answer, error)
}
