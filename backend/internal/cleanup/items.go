package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/llm"
)

// ---------------------------------------------------------------------------
// Checklist items (the per-recording extraction, backlog C7)
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

// The rules the two owner captures need, the six acceptance examples, the
// shared rules; 480 tokens against the 643 of the 2026-09-26 text, the same
// rules and examples (round-5 prompts lens, PR-4). The one rule that was
// unstated before is now explicit: a remove/tick/change request is returned
// as spoken, which is why that example is lowercase while the capitalisation
// rule applies to items.
const itemsSystemPrompt = `You turn one dictated recording into items for the checklist named in the message.

- An item is a short noun phrase in the speaker's own words, quantity kept ("two lemons", "500 g rice"), first letter capitalised, no full stop.
- Leave out the words spoken to the app or that only say the thing is wanted: "add", "put", "into it", "to my list", the list's own name, "I need", "we're out of", "buy", "get", "pick up". A task with an action of its own ("call the plumber", "post the parcel by Friday") keeps its verb.
- Split "X and Y" and "X, Y and Z" into one item each unless the words plainly name one thing ("salt and pepper" is two; "fish and chips" is one). In doubt, split.
- A recording that only tells the app what to do ("create a shopping list") yields no items.
- A request to remove, tick off or change an item is not an item to add: return the whole request as one item, as spoken.
- Fix obvious speech-to-text garbling; change nothing else. Never invent an item and never make an item of the list's name.
` + llm.LanguageRule + `
` + llm.DataRule + `

Reply with ONLY {"items":["…","…"]}; {"items":[]} when there is nothing to add. No fence, no commentary.

Examples (the list is "Shopping list"), recording then reply:
- "create a shopping list and add chickpeas and green gram into it" → {"items":["Chickpeas","Green gram"]}
- "Add umbrella to shopping list" → {"items":["Umbrella"]}
- "put milk, eggs and two loaves of bread on the shopping list" → {"items":["Milk","Eggs","Two loaves of bread"]}
- "shopping list: batteries, dish soap" → {"items":["Batteries","Dish soap"]}
- "create a shopping list" → {"items":[]}
- "remove milk from the list" → {"items":["remove milk from the list"]}`

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
	title := strings.Join(strings.Fields(strings.ReplaceAll(listTitle, llm.FenceMarker, "-----")), " ")
	if title == "" {
		title = "(untitled)"
	}

	var b strings.Builder
	b.WriteString("The list is titled: " + title + "\n")
	if language != "" {
		b.WriteString("The recording is in " + llm.LanguageLabel(language) + ".\n")
	}
	b.WriteString("The recording is between the marker lines.\n" + llm.Fence(transcript))
	return itemsSystemPrompt, b.String(), nil
}

// MaxChecklistItemRunes bounds one appended item. A checklist item is a line,
// and the extraction is asked for short noun phrases; a line this long is a
// reply that returned the recording as one item, which is still one item
// and not a paragraph. Two thousand runes is a few minutes of speech.
const MaxChecklistItemRunes = 2000

// MaxItemsPerRecording bounds one reply. A person names a handful of things
// in one breath, a few dozen at the very most; a reply with more is a model
// generating rather than extracting and is refused whole.
const MaxItemsPerRecording = 100

// ErrNotAnItemList is what ParseItems returns for a reply that is not a
// list of items: no JSON object, no "items" array, or more items than
// MaxItemsPerRecording. The pipeline falls back to the recording as one
// item, so dictation is never lost to a bad reply.
var ErrNotAnItemList = errors.New("cleanup: the model did not return a list of items")

// ParseItems reads the model's reply for ItemsPrompt and returns the items
// to append: whitespace collapsed, empty ones dropped, each cut at
// MaxChecklistItemRunes. An empty list is a valid answer: the recording was
// nothing but an instruction.
//
// Nothing here checks an item against the transcript or the title. A
// subsequence check would refuse the garbling fix the prompt asks for, and a
// rule dropping an item equal to the title would silently lose "add
// batteries" to a list titled Batteries; an item the prompt should not have
// produced is visible in the list and one tap away from gone, a dropped one
// is lost. The prompt is the guard, and provider.TestLiveEval/items is the
// check on the prompt.
func ParseItems(raw string) ([]string, error) {
	obj, err := llm.ExtractJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnItemList, err)
	}
	var reply struct {
		Items *[]string `json:"items"`
	}
	if err := json.Unmarshal([]byte(obj), &reply); err != nil || reply.Items == nil {
		return nil, ErrNotAnItemList
	}
	if len(*reply.Items) > MaxItemsPerRecording {
		return nil, fmt.Errorf("%w: %d items, limit %d", ErrNotAnItemList, len(*reply.Items), MaxItemsPerRecording)
	}
	items := make([]string, 0, len(*reply.Items))
	for _, item := range *reply.Items {
		item = strings.Join(strings.Fields(item), " ")
		if runes := []rune(item); len(runes) > MaxChecklistItemRunes {
			item = strings.TrimSpace(string(runes[:MaxChecklistItemRunes]))
		}
		if item == "" {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// ItemsMaxTokens bounds the completion for ItemsPrompt: the items are words
// of the transcript inside a little JSON, so three times the input's tokens
// (at the usual four characters per token) covers a reply that returns every
// word with quoting around each item, and a model that starts generating is
// cut off rather than paid for. The floor keeps a five-word recording from
// being capped below one JSON object.
func ItemsMaxTokens(transcript string) int {
	limit := 3 * (len(transcript)/4 + 1)
	if limit < 512 {
		limit = 512
	}
	return limit
}
