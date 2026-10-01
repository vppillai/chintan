package routing

import (
	"strings"
	"unicode"
)

// The bounds of the deterministic routing rules, in one place.
//
// Fourteen rules rescue or refuse what the router answers (prompts.md
// §Routing; the table is TestRoutingBoundsAreRegistered). Every number one
// of them reads lives here, with why it is that number and the rule that
// reads it, or, for the two rules whose numbers are the transcription's and
// the cleanup's (13: the silence scores; 14: the hint and short-dictation
// bounds), is named from here. Until 2026-10-01 these sat in three packages
// and disagreed with each other — a "name" was five words to one rule, eight
// to another, two words or eight letters to a third — and two prompt
// experiments left bounds behind whose comments cited reverted sentences
// (review 2026-10-01, BE-5, BE-12).
//
// A new bound is registered in the test's table before it ships, and a new
// rule waits for a recorded replay case (decision D8, 2026-10-01), as prompt
// text already does.
const (
	// MinNameWords and MinNameRunes are what may file a recording by opening
	// it or by being spoken as a name (rules 2 and 3: prefix_title,
	// prefix_transcript, spoken_name; pipeline.prefixRuleName): two words, or
	// one of at least eight letters. "Roof", "list" and "test" open too many
	// sentences that are not about them; a one-word name of five to seven
	// letters ("dentist", "house") waits on the owner (triage 2026-09-29,
	// decision 1).
	MinNameWords = 2
	MinNameRunes = 8

	// AppendConfidence is how sure the router must be before appending to an
	// existing note without asking (rule 4; pipeline.outcomeOf). Below it the
	// person confirms first (needs_target).
	AppendConfidence = 0.75

	// MaxInstructionWords bounds how many words the router may remove in
	// total (rule 6; RemoveSpans). A routing or naming instruction is a few
	// words ("add this to my roof repair note", "create a note titled
	// Portugal trip"); a span much longer than that is the router mistaking
	// dictation for instruction, and dictation removed from the note is lost
	// while a stray instruction word in it is trivial to fix.
	MaxInstructionWords = 24

	// MaxNameWords is the prompt's own bound on a name: its Titles rule
	// invents "a short descriptive title (one to five words)" (systemPrompt),
	// and the fixture for a sentence taken as a title pins the shape (rule 7;
	// provider.routedContent, DB6-4). A title within it is a name the speaker
	// may have said in full, so a span grown over it removes instruction; a
	// title past it is the dictation the model mistook for a name, and
	// growing over it removes the note.
	MaxNameWords = 5

	// MaxInstructionOnlyWords is the longest transcript that is plausibly
	// nothing but a spoken app instruction, and MaxSpokenTitleWords the
	// longest title that still reads as a name rather than a sentence the
	// router mistook for one (rule 8; provider.routedContent). Past either,
	// spans that cover every word look like lost dictation and the whole
	// transcript is kept. Eight here and five above because they bound
	// different mistakes: a spoken title can run to eight words ("Things to
	// talk with Milos about the Portugal trip") without the dictation having
	// been swallowed into it, while a growth over the title is undone as soon
	// as the title stops reading as a name.
	MaxInstructionOnlyWords = 20
	MaxSpokenTitleWords     = 8

	// MaxCandidates bounds the note list handed to the router, and the list
	// the pre-create re-check reads again (rules 1 to 3 and 10;
	// pipeline.decideTarget, pipeline.route): the most recently touched two
	// hundred. The store lists notes in that order over the whole partition
	// (repository.MaxNotesDrained), so the first page of the list IS the
	// window; there is no separate pool to drain and cut. It was fifty until
	// 2026-09-27, which left the owner's seven least-touched notes
	// unreachable by voice; with candidates rendered as numbered lines rather
	// than ids, two hundred cost about what fifty did (round-5 prompts lens,
	// PR-D2). Beyond a few hundred notes the right tool is a lexical
	// prefilter like Ask's ranker, not a bigger window.
	MaxCandidates = 200

	// FallbackTitleWords and FallbackTitleRunes name a note the router could
	// not title (rule 12; pipeline.fallbackNoteTitle): the first words of what
	// was said, six or forty characters, whichever comes first, so the row
	// reads as the thought it holds and still fits a list line.
	FallbackTitleWords = 6
	FallbackTitleRunes = 40

	// MaxTitleRunes is the one bound on a note title, wherever it comes from:
	// the router's dictated title (SanitizeTitle, in provider.parseRouteDecision),
	// a title the API accepts (handler.MaxTitleRunes, the OpenAPI maxLength)
	// and the title the store writes (service). One number, so a title the
	// API accepts is stored whole and a dictated title is cut no shorter than
	// a typed one; until 2026-10-01 the router cut at 120 and the other two
	// at 200, and route() ran both sanitisers on one title. The routing
	// prompt does not depend on it: it bounds every rendered candidate field
	// itself (maxFieldLen).
	MaxTitleRunes = 200
)

// SanitizeTitle bounds a title to one line of at most MaxTitleRunes: control
// characters become spaces, whitespace runs collapse to one, and the rest is
// cut. One function for every place a title enters — dictation, the API, the
// store — since a title is later rendered back into prompts and list lines.
func SanitizeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > MaxTitleRunes {
		title = strings.TrimSpace(string(runes[:MaxTitleRunes]))
	}
	return title
}
