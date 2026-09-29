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

// noteTasksSystemPrompt is the checklist's mode (Split up): the list as it
// stands → the list it was meant to be. It composes checklistItemRules, so
// what an item is and how the person's groups are read are worded once for
// this prompt and the per-recording extraction; its own three rules are what
// a whole list needs on top — every line's meaning kept, existing groups kept
// and joined, done stays done. The worked example is the owner's live case
// of 2026-09-29 beside an existing Costco.
const noteTasksSystemPrompt = `You tidy a checklist into the list it was meant to be. The text between the marker lines is the list as it stands: one item per line, "- [ ] " open, "- [x] " done, a sub-item indented two spaces under its parent.

` + checklistItemRules + `
- Every line's meaning is kept: nothing dropped, nothing added. A line that is already one thing stays word for word. A line that holds several things becomes one item each; a line that is a sentence spoken to the app ("Add milk to the shopping list") becomes the things it named.
- Keep the groups the list has, and put an item under an existing group when its own words say it belongs there ("chicken from Costco" under Costco). Two lines that name the same thing are one item.
- Done stays done: a line marked "- [x]" is an item with "done": true, its words kept; an open line is never marked done. Two lines naming the same thing merge into an open item if either was open.

Reply with ONLY {"items":[{"text":"…","done":false,"children":[{"text":"…","done":false}]},…]}, in the list's order, "done" and "children" left out when false or empty. No fence, no commentary.

Example, list then reply:
- [ ] Add milk, eggs and protein powder to the shopping list
- [x] Bread
- [ ] Costco
  - [ ] Meat
- [ ] chicken from costco and rice from the indian store
→ {"items":[{"text":"Milk"},{"text":"Eggs"},{"text":"Protein powder"},{"text":"Bread","done":true},{"text":"Costco","children":[{"text":"Meat"},{"text":"Chicken"}]},{"text":"Indian store","children":[{"text":"Rice"}]}]}`

// NotePrompt is the system and user prompt for the whole-note cleaned view
// in one of the two document modes. language is the note's own Language:
// the ISO-639-1 code it asks to be transcribed in, named up front exactly as
// UserPrompt names a transcript's, or "" / model.LanguageAuto, which claim
// nothing. Any other mode is refused rather than mapped to a default — tasks
// included, which has TasksPrompt because it needs the list's title: the
// caller chose the mode on the user's behalf and a silent substitution would
// store a document in a mode the note does not claim.
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
	return system, noteUserPrompt(body, language), nil
}

// TasksPrompt is the system and user prompt for the checklist's Split up
// (model.NoteCleanTasks). The user prompt names the list's title first,
// sanitised as ItemsPrompt names it, because the rule that the list's own
// name is not an item needs a name to work with: the ring speaks the title
// before every line ("Business ideas by Priyanka Seated pool for dogs").
func TasksPrompt(body, title, language string) (system, user string, err error) {
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("cleanup: note body is required")
	}
	return noteTasksSystemPrompt, "The list is titled: " + titleLine(title) + "\n" + noteUserPrompt(body, language), nil
}

// noteUserPrompt is the whole-note user prompt: the note's language when
// the row names one, then the fenced body.
func noteUserPrompt(body, language string) string {
	var b strings.Builder
	if language != "" && language != model.LanguageAuto {
		b.WriteString("The note is in " + llm.LanguageLabel(language) + ".\n")
	}
	b.WriteString("The note is between the marker lines.\n" + llm.Fence(body))
	return b.String()
}

// ErrEmptyNoteOutput is what NoteOutput and SplitOutput return when the
// model produced nothing usable: an empty answer, the fence markers and
// nothing else, or a Split up whose every item was dropped.
var ErrEmptyNoteOutput = fmt.Errorf("cleanup: the model returned no note text")

// NoteOutput checks a completion for the cleaned view and returns the text
// to store. A model that echoes the fence around its answer has still
// answered, so a leading and trailing marker line are removed; one that
// returned only the markers, or nothing, has not.
func NoteOutput(raw string) (string, error) {
	out := strings.TrimSpace(raw)
	out = strings.TrimSpace(strings.TrimPrefix(out, llm.FenceMarker))
	out = strings.TrimSpace(strings.TrimSuffix(out, llm.FenceMarker))
	if out == "" || strings.TrimSpace(strings.ReplaceAll(out, llm.FenceMarker, "")) == "" {
		return "", ErrEmptyNoteOutput
	}
	return out, nil
}

// MaxChecklistItems bounds a Split up, sub-items counted. Five hundred is
// far above any list a person keeps by voice and far below the 200 KB the
// row can hold, so a model that starts generating items rather than tidying
// them is refused as unusable rather than stored.
const MaxChecklistItems = 500

// ErrNotATaskList is what SplitOutput returns for an answer that is not the
// list tidied: not a list of items at all, more items than
// MaxChecklistItems, or one that lost a tick or opened a done item. The
// worker records it as "nothing usable", the same as an empty answer,
// because a checklist view that is prose is no view at all, and one that
// lost a tick is worse than the view it would replace.
var ErrNotATaskList = fmt.Errorf("cleanup: the model did not return a task list")

// SplitOutput checks a completion for TasksPrompt against the body it
// tidied and returns the checklist body to store — `- [ ] ` / `- [x] `
// lines, a sub-item indented two spaces — and how many items were dropped.
// Adoption writes this answer over the body (CleanedPanel "Use this list",
// the first act in Split up), so the prompt's promises are checked rather
// than trusted:
//
//   - an item whose words are not the body's words, in order
//     (llm.VerifySubsequence; group names like "Walmart" or "Party" are
//     body words), is the model's, not the person's: it is dropped and
//     counted, and a dropped parent's children are lifted to the top level.
//     "- [x] Make a list." was the model inventing an antecedent for "it"
//     (owner feedback 2026-09-26). Dropping rather than refusing keeps the
//     split the model got right;
//   - every done body line must be accounted for by a done answer item whose
//     words are the line's, or a sub-sequence of them (a done line the model
//     tidied), else the whole answer is refused: a lost tick is worse than
//     the previous view;
//   - an open answer item with a done body line's words is refused too,
//     unless the body also had an open line with those words — two lines
//     naming one thing merge, and open wins. A childless open answer item
//     whose words are a sub-sequence of a done line's and of no open line's
//     is the same thing: "- [x] Milk and eggs" split into an open Milk and a
//     done Eggs lost Milk's tick. A parent is exempt, because a group's name
//     over done lines ("Walmart" over "- [x] Eggs from Walmart") is not a
//     tick, and the prompt never asks for a parent's done;
//   - a done answer item with no done body line's words (equal, or the item
//     a sub-sequence of the line) is refused: the tick is the person's, and
//     a model that adds one hides an open item under Done. So is a done
//     answer item with an open body line's words when no open answer item
//     has them: the model closed the open one of a pair, and open wins.
//
// The pre-2026-09-29 rule that done lines come back verbatim and in order is
// gone: it is what forced "Add milk to the shopping list" to survive a
// split. Nothing left after the drops is ErrEmptyNoteOutput.
func SplitOutput(raw, body string) (text string, dropped int, err error) {
	out, err := NoteOutput(raw)
	if err != nil {
		return "", 0, err
	}
	items, err := parseItems(out, MaxChecklistItems)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrNotATaskList, err)
	}

	var kept []Item
	keep := func(it Item) bool {
		if llm.VerifySubsequence(it.Text, body) {
			return true
		}
		dropped++
		return false
	}
	for _, it := range items {
		var children []Item
		for _, c := range it.Children {
			if keep(c) {
				children = append(children, Item{Text: c.Text, Done: c.Done})
			}
		}
		if keep(it) {
			kept = append(kept, Item{Text: it.Text, Done: it.Done, Children: children})
			continue
		}
		kept = append(kept, children...)
	}

	// Tick safety, over the body's lines and the kept answer.
	bodyDone, bodyOpen := map[string]bool{}, map[string]bool{}
	for _, it := range flatten(ItemsFromLines(body)) {
		if it.Done {
			bodyDone[llm.FoldWords(it.Text)] = true
		} else {
			bodyOpen[llm.FoldWords(it.Text)] = true
		}
	}
	answer := flatten(kept)
	answerOpen := map[string]bool{}
	for _, it := range answer {
		if !it.Done {
			answerOpen[llm.FoldWords(it.Text)] = true
		}
	}
	for line := range bodyDone {
		if bodyOpen[line] {
			// Two lines naming one thing, one of them open: they merge and
			// open wins, so the done one need not come back done.
			continue
		}
		accounted := false
		for _, it := range answer {
			if it.Done && (llm.FoldWords(it.Text) == line || llm.VerifySubsequence(it.Text, line)) {
				accounted = true
				break
			}
		}
		if !accounted {
			return "", dropped, fmt.Errorf("%w: a done item was lost", ErrNotATaskList)
		}
	}
	check := func(it Item, parent bool) error {
		w := llm.FoldWords(it.Text)
		switch {
		case !it.Done && bodyDone[w] && !bodyOpen[w],
			!it.Done && !parent && subsequenceOfAny(it.Text, bodyDone) && !subsequenceOfAny(it.Text, bodyOpen):
			return fmt.Errorf("%w: a done item was reopened", ErrNotATaskList)
		case it.Done && bodyOpen[w] && !answerOpen[w]:
			return fmt.Errorf("%w: an open item was closed", ErrNotATaskList)
		case it.Done && !bodyDone[w] && !subsequenceOfAny(it.Text, bodyDone):
			return fmt.Errorf("%w: a done item was invented", ErrNotATaskList)
		}
		return nil
	}
	for _, it := range kept {
		if err := check(it, len(it.Children) > 0); err != nil {
			return "", dropped, err
		}
		for _, c := range it.Children {
			if err := check(c, false); err != nil {
				return "", dropped, err
			}
		}
	}
	if len(kept) == 0 {
		return "", dropped, ErrEmptyNoteOutput
	}
	return RenderTaskList(kept), dropped, nil
}

// subsequenceOfAny reports whether text's words are, in order, among one of
// the lines' words.
func subsequenceOfAny(text string, lines map[string]bool) bool {
	for line := range lines {
		if llm.VerifySubsequence(text, line) {
			return true
		}
	}
	return false
}

// TasksMaxTokens bounds the completion for TasksPrompt: the items are the
// body's words inside a JSON object per item, so three times the input's
// tokens (at the usual four characters per token) covers a list returned
// whole with an object around each line. The floor keeps a one-line list
// from being capped below a few objects.
func TasksMaxTokens(body string) int {
	limit := 3 * (len(body)/4 + 1)
	if limit < 512 {
		limit = 512
	}
	return limit
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
