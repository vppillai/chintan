package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/llm"
)

// ---------------------------------------------------------------------------
// Checklist items (the per-recording extraction, backlog C7; the tree, R6-CL-1)
// ---------------------------------------------------------------------------
//
// A recording filed into a checklist used to become one item holding the
// cleaned transcript, and which words that was depended on the router's span
// removal: "Add umbrella to shopping list" became the item "list", and
// "create a shopping list and add chickpeas and green gram into it" became
// "Add chickpeas and green gram into it." (owner feedback 2026-09-26). Nothing
// between the router and the append knew what a checklist item is — the
// router deletes words, the cleanup prompt cleans prose, the append renders
// one line. This prompt is the one component that does know: it reads the
// raw transcript with the list's name beside it and answers with the items
// to add, and with none when the recording only told the app what to do.
//
// Since 2026-09-29 the items are a one-level tree. "eggs from Walmart and
// meat from Costco" used to become `Eggs from Walmart` and `Meat from
// Costco`, or five flat items for a longer breath: no prompt knew what a
// group was and no parser could carry one (round-6 checklist lens, prod
// battery 10 of 15). A place, a person, an occasion or a category the things
// are named under is now the parent and the things are its children, which
// is the shape the owner types by hand already.

// Item is one checklist item as the model returns it and as the append
// writes it: its text, whether it is done (the Split up's; the per-recording
// extraction never sets it) and its sub-items. One level: a child has no
// children (owner decision CL-D1), and ParseItems clamps a deeper reply to
// that.
type Item struct {
	Text     string `json:"text"`
	Done     bool   `json:"done,omitempty"`
	Children []Item `json:"children,omitempty"`
}

// UnmarshalJSON accepts an item as an object or as a bare string, so a model
// that answers the pre-2026-09-29 shape `{"items":["Milk","Eggs"]}` degrades
// to flat items, never to an unusable reply.
func (it *Item) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &it.Text)
	}
	type plain Item
	return json.Unmarshal(b, (*plain)(it))
}

// checklistItemRules is what an item is, worded once for the two prompts that
// make them: the per-recording extraction (ItemsPrompt) and the whole-note
// Split up (TasksPrompt). The owner's two sentences of 2026-09-29 are the
// examples in the second and fourth rules: a sentence spoken to the app is the
// things it named, and a place the things are named under is their parent.
const checklistItemRules = `- An item is one thing the person wants on the list, as a short entry in their own words: quantity kept ("two lemons", "500 g rice"), first letter capitalised, no full stop. A task with an action of its own ("call the plumber", "post the parcel by Friday") keeps its verb.
- Leave out every word that is about the list rather than on it: "add", "put", "into it", "to my list", "buy", "get", "pick up", "I need", "we're out of", and the list's own name — also when the recording opens with that name to file it ("Shopping list eggs from Walmart"). "Add milk, eggs and protein powder to the shopping list" is three items, Milk, Eggs and Protein powder: never one item holding the sentence, never "Add milk".
- One thing per item: "X and Y" and "X, Y and Z" are one item each, unless the words plainly name one thing ("salt and pepper" is two; "fish and chips" is one). In doubt, split.
- Group as the person grouped. A place, a person, an occasion or a category the things are named under ("eggs from Walmart and meat from Costco", "for the party plates and cups") is a parent item with those things as its children: Walmart › Eggs, Costco › Meat. One level only: a child has no children. A thing named under no group is a top-level item. Never invent a group, and never make a group of the list's own name.
- A request to remove, tick off or change an item is not an item to add: return the whole request as one top-level item, as spoken.
- Fix obvious speech-to-text garbling and drop fillers ("um", "okay so"); change nothing else. Never invent an item and never lose one.
` + llm.LanguageRule + `
` + llm.DataRule

// itemsSystemPrompt: one recording → the items to add, grouped as spoken.
// Six examples, the owner's two sentences first.
const itemsSystemPrompt = `You turn one dictated recording into items for the checklist named in the message. The recording was spoken to add things to that list; return the things to add, grouped as the person grouped them.

` + checklistItemRules + `

Reply with ONLY {"items":[{"text":"…","children":[{"text":"…"}]},…]}: "children" holds the things named under a parent, and is [] or left out when there are none. {"items":[]} when there is nothing to add. No fence, no commentary.

Examples (the list is "Shopping list"), recording then reply:
- "Add milk, eggs and protein powder to the shopping list" → {"items":[{"text":"Milk"},{"text":"Eggs"},{"text":"Protein powder"}]}
- "add buying eggs from Walmart and meat from Costco in the shopping list" → {"items":[{"text":"Walmart","children":[{"text":"Eggs"}]},{"text":"Costco","children":[{"text":"Meat"}]}]}
- "Shopping list eggs from Walmart" → {"items":[{"text":"Walmart","children":[{"text":"Eggs"}]}]}
- "shopping list: batteries, dish soap" → {"items":[{"text":"Batteries"},{"text":"Dish soap"}]}
- "create a shopping list" → {"items":[]}
- "remove milk from the list" → {"items":[{"text":"remove milk from the list"}]}`

// ItemsPrompt is the system and user prompt that turns one recording into
// checklist items for the list titled listTitle. language is the ISO-639-1
// code the transcript is known to be in, or "" when nothing knows, named up
// front as UserPrompt names it. The title is the one fact about the
// destination the model needs — it is what tells "add umbrella to shopping
// list" from dictation that happens to mention a list — and it is the only
// other text in the prompt, so it is kept to a line and cannot open or close
// the fence.
func ItemsPrompt(transcript, listTitle, language string) (system, user string, err error) {
	if strings.TrimSpace(transcript) == "" {
		return "", "", fmt.Errorf("cleanup: raw transcript is required")
	}
	var b strings.Builder
	b.WriteString("The list is titled: " + titleLine(listTitle) + "\n")
	if language != "" {
		b.WriteString("The recording is in " + llm.LanguageLabel(language) + ".\n")
	}
	b.WriteString("The recording is between the marker lines.\n" + llm.Fence(transcript))
	return itemsSystemPrompt, b.String(), nil
}

// titleLine is a list's title as a prompt names it: one line, a fence marker
// typed into it defanged, "(untitled)" when there is nothing to name.
func titleLine(title string) string {
	title = strings.Join(strings.Fields(strings.ReplaceAll(title, llm.FenceMarker, "-----")), " ")
	if title == "" {
		return "(untitled)"
	}
	return title
}

// MaxChecklistItemRunes bounds one appended item. A checklist item is a line,
// and the extraction is asked for short entries; a line this long is a reply
// that returned the recording as one item, which is still one item and not a
// paragraph. Two thousand runes is a few minutes of speech.
const MaxChecklistItemRunes = 2000

// MaxItemsPerRecording bounds one reply, sub-items counted. A person names a
// handful of things in one breath, a few dozen at the very most; a reply with
// more is a model generating rather than extracting and is refused whole.
const MaxItemsPerRecording = 100

// ErrNotAnItemList is what ParseItems returns for a reply that is not a
// list of items: no JSON object, no "items" array, or more items than
// MaxItemsPerRecording. The pipeline falls back to the recording as one
// item, so dictation is never lost to a bad reply.
var ErrNotAnItemList = errors.New("cleanup: the model did not return a list of items")

// ParseItems reads the model's reply for ItemsPrompt and returns the items
// to append as a one-level tree: each item an object {text, children?,
// done?} or a bare string; a grandchild clamped to a child of the top-level
// item; text whitespace-collapsed and cut at MaxChecklistItemRunes; an item
// with no text dropped, its children lifted to the top level. More than
// MaxItemsPerRecording items counting children is ErrNotAnItemList. An
// empty list is a valid answer: the recording was nothing but an
// instruction.
//
// Nothing here checks an item against the transcript or the title. A
// subsequence check would refuse the garbling fix the prompt asks for, and a
// rule dropping an item equal to the title would silently lose "add
// batteries" to a list titled Batteries; an item the prompt should not have
// produced is visible in the list and one tap away from gone, a dropped one
// is lost. The prompt is the guard, and provider.TestLiveEval/items is the
// check on the prompt.
func ParseItems(raw string) ([]Item, error) {
	return parseItems(raw, MaxItemsPerRecording)
}

func parseItems(raw string, limit int) ([]Item, error) {
	obj, err := llm.ExtractJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnItemList, err)
	}
	var reply struct {
		Items *[]Item `json:"items"`
	}
	if err := json.Unmarshal([]byte(obj), &reply); err != nil || reply.Items == nil {
		return nil, ErrNotAnItemList
	}
	if n := countItems(*reply.Items); n > limit {
		return nil, fmt.Errorf("%w: %d items, limit %d", ErrNotAnItemList, n, limit)
	}
	items := make([]Item, 0, len(*reply.Items))
	for _, it := range *reply.Items {
		// One level: a child's own children become children of the top-level
		// item, after it, the way the frontend parser clamps a two-level
		// jump (checklist.ts, MAX_DEPTH).
		var children []Item
		for _, c := range it.Children {
			if text := itemText(c.Text); text != "" {
				children = append(children, Item{Text: text, Done: c.Done})
			}
			for _, g := range flatten(c.Children) {
				if text := itemText(g.Text); text != "" {
					children = append(children, Item{Text: text, Done: g.Done})
				}
			}
		}
		if text := itemText(it.Text); text != "" {
			items = append(items, Item{Text: text, Done: it.Done, Children: children})
			continue
		}
		items = append(items, children...)
	}
	return items, nil
}

// countItems is the number of items in a reply at any depth.
func countItems(items []Item) int {
	n := 0
	for _, it := range items {
		n += 1 + countItems(it.Children)
	}
	return n
}

// flatten lists items at any depth, each without its children.
func flatten(items []Item) []Item {
	var out []Item
	for _, it := range items {
		out = append(out, Item{Text: it.Text, Done: it.Done})
		out = append(out, flatten(it.Children)...)
	}
	return out
}

// itemText is an item's text as it is stored: whitespace runs collapsed,
// cut at MaxChecklistItemRunes, "" for no words.
func itemText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > MaxChecklistItemRunes {
		s = strings.TrimSpace(string(runes[:MaxChecklistItemRunes]))
	}
	return s
}

// RenderItems writes items as the clean artefact holds them: one line per
// item, a child's line prefixed by two spaces, no box. The append renders
// the box (pipeline checklistItems) and the readers of the artefact that
// compare items by their words (previousItems, replaceChecklistItems) fold
// the indent away. Text is collapsed and an item with no words is skipped,
// so a fake's untidy reply renders the way ParseItems would have cleaned it.
func RenderItems(items []Item) string {
	var lines []string
	for _, it := range items {
		if text := itemText(it.Text); text != "" {
			lines = append(lines, text)
			for _, c := range it.Children {
				if ct := itemText(c.Text); ct != "" {
					lines = append(lines, "  "+ct)
				}
			}
			continue
		}
		for _, c := range it.Children {
			if ct := itemText(c.Text); ct != "" {
				lines = append(lines, ct)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// ItemsFromLines is the inverse of RenderItems: a line that starts with two
// spaces is a child of the last top-level line, blank lines are skipped, a
// child with no parent yet is top level. It reads a checklist body's lines
// too — the task-list box after the indent is read as Done and left out of
// the text — so the append and the Split up's checks see the body as items.
func ItemsFromLines(text string) []Item {
	var items []Item
	for _, line := range strings.Split(text, "\n") {
		child := strings.HasPrefix(line, "  ")
		trimmed := strings.TrimLeft(line, " \t")
		done := false
		switch {
		case strings.HasPrefix(trimmed, "- [ ] "):
			trimmed = trimmed[len("- [ ] "):]
		case strings.HasPrefix(trimmed, "- [x] "), strings.HasPrefix(trimmed, "- [X] "):
			trimmed, done = trimmed[len("- [x] "):], true
		}
		t := itemText(trimmed)
		if t == "" {
			continue
		}
		it := Item{Text: t, Done: done}
		if child && len(items) > 0 {
			last := &items[len(items)-1]
			last.Children = append(last.Children, it)
			continue
		}
		items = append(items, it)
	}
	return items
}

// ItemsMaxTokens bounds the completion for ItemsPrompt: the items are words
// of the transcript inside a JSON object per item, so four times the
// input's tokens (at the usual four characters per token) covers a reply
// that returns every word with an object around each, and a model that
// starts generating is cut off rather than paid for. The floor keeps a
// five-word recording from being capped below a few objects.
func ItemsMaxTokens(transcript string) int {
	limit := 4 * (len(transcript)/4 + 1)
	if limit < 768 {
		limit = 768
	}
	return limit
}
