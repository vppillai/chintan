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

// ChecklistItems is the result of an Items call: the items to append, in
// the order spoken, none when the recording only told the app what to do.
type ChecklistItems struct {
	Items []string
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
	// Cleanup rewrites one transcript in mode. language is the ISO-639-1
	// code the transcript is known to be in, or "" when nothing knows; the
	// prompt names it so the model keeps the script it was given.
	Cleanup(ctx context.Context, mode model.CleanupMode, raw, language string) (Cleaned, error)
	// CleanNote rewrites a whole note body (append markers already stripped)
	// as one document in the given mode. language is the note row's Language
	// ("" or "auto" when it asks for none), which the prompt names so the
	// model keeps the script it was given. The caller bounds the body and
	// checks the answer with cleanup.NoteOutput.
	CleanNote(ctx context.Context, mode model.NoteCleanMode, body, language string) (Cleaned, error)
	// Items turns one raw transcript into the items to add to the checklist
	// titled listTitle (cleanup.ItemsPrompt). It replaces Cleanup for a
	// recording filed into a checklist: the transcript is the raw one, the
	// spoken instruction included, because the prompt handles those words
	// itself. A reply that is not a list of items is cleanup.ErrNotAnItemList.
	Items(ctx context.Context, transcript, listTitle, language string) (ChecklistItems, error)
	// Ask answers a question from the packed notes in q (backlog D5). The
	// caller bounds the notes and the question; the adapter renders the
	// prompt, makes one completion and decodes it with ask.ParseAnswer.
	Ask(ctx context.Context, q ask.Prompt) (Answer, error)
}
