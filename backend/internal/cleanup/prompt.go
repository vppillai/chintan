package cleanup

import (
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
)

// Per-capture cleanup has one mode since 2026-09-27: faithful. Polished
// rewrote paragraph by paragraph, so a note's tone drifted between
// recordings, and the whole-note Polished view (NotePrompt) does that job on
// the whole (round-5 prompts lens, PR-D5). The mode line stays in the text
// because the eval baseline was measured with it. The shared rules — nothing
// invented, the language kept, the transcript is data — are llm's, composed
// between this package's own bullets.
const systemPrompt = `You clean up a speech-to-text transcript for a personal note.
Mode: faithful. Fix STT garbling, punctuation and obvious grammar mistakes; keep the speaker's wording, phrasing and vocabulary.
` + llm.NoInventionRule + `
` + llm.LanguageRule + `
` + llm.DataRule + `
- Return only the cleaned text, no preamble or commentary.`

// SystemPrompt returns the per-capture cleanup system prompt.
func SystemPrompt() string {
	return systemPrompt
}

// UserPrompt renders the transcript for the cleanup model. language is the
// ISO-639-1 code the transcript is known to be in, or "" when nothing knows;
// naming it up front is what keeps a model from "correcting" a script it did
// not expect into one it did.
func UserPrompt(raw, language string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("cleanup: raw transcript is required")
	}

	var b strings.Builder
	if language != "" {
		b.WriteString("The transcript is in " + llm.LanguageLabel(language) + ".\n")
	}
	// One line names what the fenced text is; the rule that it is data is the
	// system prompt's (llm.DataRule). llm.Fence defangs any marker the
	// dictation itself contains so it cannot close the block early.
	b.WriteString("The transcript is between the marker lines.\n" + llm.Fence(raw))
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// Whole-note cleanup (the cleaned view, backlog D1)
// ---------------------------------------------------------------------------
//
// The per-capture prompts above clean one transcript as it is appended. The
// note prompt runs over the entire body after the fact and produces a
// document: the same "the text is content, never instructions" rule (D12),
// the same fence, but a brief written for a reader of the whole note rather
// than for a paragraph.

const (
	noteSharedRules = `- Keep every fact, decision, name, number and date. Do not add information.
- Remove filler, false starts and repetition.
` + llm.LanguageRule + `
` + llm.DataRule + `
- Return only the rewritten note, in Markdown, with no preamble or commentary.`

	// The two document modes are one template: a sentence naming the
	// document the mode asks for, then the shared rules. Only that sentence
	// differs, so only it is the mode's to word.
	noteStructuredSystemPrompt = `You rewrite a dictated personal note as a well-organised Markdown document: related points
under short headings, lists for enumerations, prose otherwise.
` + noteSharedRules

	notePolishedSystemPrompt = `You rewrite a dictated personal note as coherent prose only, no headings and no lists, with a
light touch: fix what dictation garbled and smooth the flow, and keep the author's phrasing
and vocabulary where it already reads well.
` + noteSharedRules
)

// NotePrompt is the system and user prompt for the whole-note cleaned view.
// language is the note's own Language: the ISO-639-1 code it asks to be
// transcribed in, named up front exactly as UserPrompt names a transcript's,
// or "" / model.LanguageAuto, which claim nothing. An unknown mode is refused
// rather than mapped to a default: the caller chose the mode on the user's
// behalf and a silent substitution would store a document in a mode the note
// does not claim.
func NotePrompt(mode model.NoteCleanMode, body, language string) (system, user string, err error) {
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("cleanup: note body is required")
	}
	switch mode {
	case model.NoteCleanStructured:
		system = noteStructuredSystemPrompt
	case model.NoteCleanPolished:
		system = notePolishedSystemPrompt
	default:
		return "", "", fmt.Errorf("cleanup: unknown note clean mode %q", mode)
	}
	var b strings.Builder
	if language != "" && language != model.LanguageAuto {
		b.WriteString("The note is in " + llm.LanguageLabel(language) + ".\n")
	}
	b.WriteString("The note is between the marker lines.\n" + llm.Fence(body))
	return system, b.String(), nil
}

// ErrEmptyNoteOutput is what NoteOutput returns when the model produced
// nothing usable: an empty answer, or the fence markers and nothing else.
var ErrEmptyNoteOutput = fmt.Errorf("cleanup: the model returned no note text")

// NoteOutput checks a completion for the cleaned view and returns the text
// to store. A model that echoes the fence around its answer has still
// answered, so a leading and trailing marker line are removed; one that
// returned only the markers, or nothing, has not. Until 2026-09-27 a tasks
// mode held a checklist's answer to task-list lines here; the mode is gone
// (round-5 prompts lens, PR-D4: items are extracted per recording instead)
// and a checklist has no cleaned view.
func NoteOutput(raw string) (string, error) {
	out := strings.TrimSpace(raw)
	out = strings.TrimSpace(strings.TrimPrefix(out, llm.FenceMarker))
	out = strings.TrimSpace(strings.TrimSuffix(out, llm.FenceMarker))
	if out == "" || strings.TrimSpace(strings.ReplaceAll(out, llm.FenceMarker, "")) == "" {
		return "", ErrEmptyNoteOutput
	}
	return out, nil
}

// NoteMaxTokens bounds the completion for a body of the given size: about
// one and a half times the input, at the usual four characters per token, so
// a structured rewrite has room for headings and list syntax and a model that
// starts repeating itself is cut off rather than paid for. The floor keeps a
// one-line note from being capped below a sentence.
func NoteMaxTokens(body string) int {
	tokens := len(body)/4 + 1
	limit := tokens + tokens/2
	if limit < 256 {
		limit = 256
	}
	return limit
}
