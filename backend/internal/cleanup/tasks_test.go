package cleanup_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
)

// The Split up prompt composes the shared item rules and adds the three a
// whole list needs — every line's meaning kept, existing groups kept and
// joined, done stays done — asks for JSON items with "done", and its user
// prompt names the list first so the list's own name is never an item.
func TestTasksPromptComposesTheItemRulesAndNamesTheList(t *testing.T) {
	system, user, err := cleanup.TasksPrompt("- [ ] call the roofer and buy sealant\n- [x] passport", "Errands", "ml")
	if err != nil {
		t.Fatalf("TasksPrompt: %v", err)
	}
	lower := strings.ToLower(system)
	for _, want := range []string{
		"the list it was meant to be", "a sub-item indented two spaces", "two more for each level, at most four levels",
		"keep every item at the level it has, up to four levels", "under an existing group or sub-group", "never add a level the list does not have",
		"group as the person grouped", "never invent a group", `never "add milk"`,
		"every line's meaning is kept", "a sentence spoken to the app", "keep the groups the list has", "under an existing group",
		"two lines that name the same thing are one item", "done stays done", `"done": true`, "an open line is never marked done",
		"open item if either was open", `"done":false`, "in the list's order",
		"- [ ] add milk, eggs and protein powder to the shopping list", "- [ ] costco\n  - [ ] meat",
		`{"text":"costco","children":[{"text":"meat"},{"text":"chicken"}]}`, `{"text":"bread","done":true}`,
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("tasks system prompt lacks %q", want)
		}
	}
	for _, rule := range []string{llm.LanguageRule, llm.DataRule} {
		if !strings.Contains(system, rule) {
			t.Errorf("tasks system prompt lacks the shared rule %q", rule)
		}
	}
	if !strings.HasPrefix(user, "The list is titled: Errands\nThe note is in Malayalam (ml).\nThe note is between the marker lines.\n"+llm.FenceMarker+"\n") || strings.Count(user, llm.FenceMarker) != 2 {
		t.Errorf("the user prompt does not name the list, the language and fence the checklist: %q", user)
	}
	_, user, _ = cleanup.TasksPrompt("- [ ] x", "My "+llm.FenceMarker+" list", model.LanguageAuto)
	if !strings.HasPrefix(user, "The list is titled: My ----- list\nThe note is between the marker lines.\n") {
		t.Errorf("the title is not sanitised or auto claims a language: %q", user)
	}
	if _, _, err := cleanup.TasksPrompt(" \n", "Errands", ""); err == nil {
		t.Error("an empty body was accepted")
	}
	if _, _, err := cleanup.NotePrompt(model.NoteCleanTasks, "- [ ] x", ""); err == nil {
		t.Error("NotePrompt accepted tasks; the mode needs the title and has TasksPrompt")
	}
}

// The owner's live case of 2026-09-29: the sentence becomes the things it
// named. A fenced reply is read; the tree is written with the indent; done
// comes back as a tick.
func TestSplitOutputAcceptsTheSplitAndWritesTheTree(t *testing.T) {
	const body = "- [ ] Add milk, eggs and protein powder to the shopping list"
	got, dropped, err := cleanup.SplitOutput(`{"items":[{"text":"Milk"},{"text":"Eggs"},{"text":"Protein powder"}]}`, body)
	if err != nil || dropped != 0 || got != "- [ ] Milk\n- [ ] Eggs\n- [ ] Protein powder" {
		t.Errorf("SplitOutput = %q, %d, %v", got, dropped, err)
	}

	tree := "- [ ] add buying eggs from Walmart and meat from Costco in the shopping list\n- [x] Bread\n- [ ] Costco\n  - [x] Meat"
	reply := llm.FenceMarker + "\n```json\n" + `{"items":[{"text":"Walmart","children":[{"text":"Eggs"}]},{"text":"Costco","children":[{"text":"Meat","done":true}]},{"text":"Bread","done":true}]}` + "\n```\n" + llm.FenceMarker
	got, dropped, err = cleanup.SplitOutput(reply, tree)
	if err != nil || dropped != 0 || got != "- [ ] Walmart\n  - [ ] Eggs\n- [ ] Costco\n  - [x] Meat\n- [x] Bread" {
		t.Errorf("SplitOutput(tree) = %q, %d, %v", got, dropped, err)
	}

	// The old shape still splits.
	got, _, err = cleanup.SplitOutput(`{"items":["Milk","Eggs"]}`, "- [ ] milk and eggs")
	if err != nil || got != "- [ ] Milk\n- [ ] Eggs" {
		t.Errorf("SplitOutput(strings) = %q, %v", got, err)
	}
}

// An item whose words are not the body's is the model's: dropped and
// counted, a dropped parent's children lifted, the split beside it kept.
// Nothing left is the empty verdict.
func TestSplitOutputDropsInventedItemsAndLiftsADroppedParentsChildren(t *testing.T) {
	const body = "- [ ] Add chickpeas and green gram into it.\n\n- [x] passport"
	got, dropped, err := cleanup.SplitOutput(`{"items":[{"text":"Chickpeas"},{"text":"Green gram"},{"text":"Make a list"},{"text":"passport","done":true}]}`, body)
	if err != nil || dropped != 1 || got != "- [ ] Chickpeas\n- [ ] Green gram\n- [x] passport" {
		t.Errorf("an invented open item was not dropped alone: %q, %d, %v", got, dropped, err)
	}
	got, dropped, err = cleanup.SplitOutput(`{"items":[{"text":"Pantry","children":[{"text":"Chickpeas"},{"text":"Green gram"}]},{"text":"passport","done":true}]}`, body)
	if err != nil || dropped != 1 || got != "- [ ] Chickpeas\n- [ ] Green gram\n- [x] passport" {
		t.Errorf("a dropped parent did not lift its children: %q, %d, %v", got, dropped, err)
	}
	if _, dropped, err := cleanup.SplitOutput(`{"items":["Make a list","Buy a pen"]}`, "- [ ] chickpeas"); !errors.Is(err, cleanup.ErrEmptyNoteOutput) || dropped != 2 {
		t.Errorf("all-invented answer = %d dropped, %v; want 2 and ErrEmptyNoteOutput", dropped, err)
	}
}

// Tick safety: every done body line is accounted for by a done answer item
// with its words (or a tidied sub-sequence of them); an open answer item
// with a done line's words — or, childless, a part of them and of no open
// line's — is refused unless the body also had it open; a done answer item
// with no done line's words is a tick the model added, and one with an open
// line's words that no open answer item has closed that line.
func TestSplitOutputRefusesALostReopenedOrInventedTick(t *testing.T) {
	const body = "- [ ] call the roofer and buy sealant\n- [x] passport.\n- [x] pay the electricity bill"
	for name, reply := range map[string]string{
		"a done item lost":       `{"items":["Call the roofer","Buy sealant",{"text":"pay the electricity bill","done":true}]}`,
		"a done item reopened":   `{"items":["Call the roofer","Buy sealant","Passport",{"text":"pay the electricity bill","done":true}]}`,
		"a done item reworded":   `{"items":["Call the roofer","Buy sealant",{"text":"passport renewed","done":true},{"text":"pay the electricity bill","done":true}]}`,
		"a done item invented":   `{"items":[{"text":"Call the roofer","done":true},"Buy sealant",{"text":"passport","done":true},{"text":"pay the electricity bill","done":true}]}`,
		"not a list":             "Call the roofer, then buy sealant.",
		"a done sub-item opened": `{"items":["Call the roofer","Buy sealant",{"text":"Errands","children":[{"text":"passport"},{"text":"pay the electricity bill","done":true}]}]}`,
		"a done line split into an open part and a done part": `{"items":["Call the roofer","Buy sealant",{"text":"passport","done":true},"Electricity",{"text":"pay the bill","done":true}]}`,
	} {
		if got, _, err := cleanup.SplitOutput(reply, body); !errors.Is(err, cleanup.ErrNotATaskList) {
			t.Errorf("%s: SplitOutput = %q, %v; want ErrNotATaskList", name, got, err)
		}
	}
	// A done line tidied — its words a sub-sequence of the line's — is still
	// accounted for; casing and punctuation are not rewriting; a typed [X]
	// is a tick.
	got, dropped, err := cleanup.SplitOutput(`{"items":["Chickpeas",{"text":"Passport","done":true},{"text":"Electricity bill","done":true}]}`, "- [ ] chickpeas\n- [X] passport.\n- [x] pay the  electricity bill")
	if err != nil || dropped != 0 || got != "- [ ] Chickpeas\n- [x] Passport\n- [x] Electricity bill" {
		t.Errorf("a tidied done item was refused: %q, %d, %v", got, dropped, err)
	}
	// Duplicates merge and open wins: an open Milk over "- [x] milk" is fine
	// when the body also has an open Milk; one done Milk for the pair closed
	// the open one.
	got, _, err = cleanup.SplitOutput(`{"items":["Milk","Eggs"]}`, "- [ ] Milk\n- [x] milk\n- [ ] Eggs")
	if err != nil || got != "- [ ] Milk\n- [ ] Eggs" {
		t.Errorf("open did not win over a done duplicate: %q, %v", got, err)
	}
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Milk","done":true},"Eggs"]}`, "- [ ] Milk\n- [x] milk\n- [ ] Eggs"); !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("closing the open one of a pair = %q, %v; want ErrNotATaskList", got, err)
	}
	// A part of a done line is a reopened tick only when no open line has it
	// too, and a group's name over done lines is a group, not a tick.
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Milk","done":true},"Eggs"]}`, "- [x] Milk and eggs\n- [ ] eggs")
	if err != nil || got != "- [x] Milk\n- [ ] Eggs" {
		t.Errorf("a part shared with an open line was refused: %q, %v", got, err)
	}
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Walmart","children":[{"text":"Eggs","done":true},{"text":"Milk","done":true}]}]}`, "- [x] eggs from Walmart\n- [x] milk from Walmart")
	if err != nil || got != "- [ ] Walmart\n  - [x] Eggs\n  - [x] Milk" {
		t.Errorf("a group over done lines was refused: %q, %v", got, err)
	}
	// A done body line with no letter or digit is no item, so it cannot be a
	// tick the answer lost.
	if got, _, err := cleanup.SplitOutput(`{"items":["Milk","Eggs"]}`, "- [x] —\n- [ ] milk and eggs"); err != nil || got != "- [ ] Milk\n- [ ] Eggs" {
		t.Errorf("a punctuation-only done line blocked the split: %q, %v", got, err)
	}
	// A "[x]" inside an item's text is text.
	if got, _, err := cleanup.SplitOutput(`{"items":["a [x] inside the text is fine"]}`, "- [ ] a [x] inside the text is fine"); err != nil || got != "- [ ] a [x] inside the text is fine" {
		t.Errorf("SplitOutput([x] in text) = %q, %v", got, err)
	}
}

// BE-1 (review 2026-10-01): an answer that drops an open line or a prose
// line is refused whole, since Tidy up list writes it over the body. A
// line is kept when its words are in some answer item — a split, a merge
// and a regroup under a new group all are. Only an open answer item covers
// a line: a done one with its words is the line closed.
func TestSplitOutputRefusesALostOpenItemOrProseLine(t *testing.T) {
	for _, tc := range []struct {
		name, body, reply, want string
	}{
		{"the repro: three open items gone", "- [ ] Milk\n- [ ] Eggs\n- [ ] Bread\n- [ ] Rice", `{"items":[{"text":"Milk"}]}`, ""},
		{"a prose line gone", "- [ ] Milk\nRemember the coupon\n- [ ] Eggs", `{"items":["Milk","Eggs"]}`, ""},
		{"an open sub-item gone", "- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice", `{"items":[{"text":"Costco","children":["Meat"]}]}`, ""},
		{"an open line closed into a done one that holds it", "- [x] Milk\n- [ ] Milk 2 litres", `{"items":[{"text":"Milk","done":true}]}`, ""},
		{"an open line closed into a done part of it", "- [x] Eggs\n- [ ] Buy eggs", `{"items":[{"text":"Eggs","done":true}]}`, ""},
		{"two lines merged into one", "- [ ] Milk\n- [ ] Milk 2 litres\n- [ ] Eggs", `{"items":["Milk 2 litres","Eggs"]}`, "- [ ] Milk 2 litres\n- [ ] Eggs"},
		{"one line split into three", "- [ ] Add milk, eggs and protein powder to the shopping list", `{"items":["Milk","Eggs","Protein powder"]}`, "- [ ] Milk\n- [ ] Eggs\n- [ ] Protein powder"},
		{"a regroup under a new group", "- [ ] Costco\n  - [ ] Meat\n- [ ] chicken from costco and rice from the indian store", `{"items":[{"text":"Costco","children":["Meat","Chicken"]},{"text":"Indian store","children":["Rice"]}]}`, "- [ ] Costco\n  - [ ] Meat\n  - [ ] Chicken\n- [ ] Indian store\n  - [ ] Rice"},
		{"a prose line kept as an item", "- [ ] Milk\nRemember the coupon\n- [ ] Eggs", `{"items":["Milk","Remember the coupon","Eggs"]}`, "- [ ] Milk\n- [ ] Remember the coupon\n- [ ] Eggs"},
	} {
		got, _, err := cleanup.SplitOutput(tc.reply, tc.body)
		switch {
		case tc.want == "" && !errors.Is(err, cleanup.ErrNotATaskList):
			t.Errorf("%s: SplitOutput = %q, %v; want ErrNotATaskList", tc.name, got, err)
		case tc.want == "" && !strings.HasSuffix(err.Error(), ": an open item was lost"):
			t.Errorf("%s: error = %v; want the fixed sentence", tc.name, err)
		case tc.want != "" && (err != nil || got != tc.want):
			t.Errorf("%s: SplitOutput = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// The item cap counts sub-items: five hundred is stored, one more is refused.
// Nothing at all is the empty verdict, not a task-list one.
func TestSplitOutputCapsTheListAndRefusesNothing(t *testing.T) {
	items := make([]string, 0, cleanup.MaxChecklistItems+1)
	for i := 0; i < cleanup.MaxChecklistItems-1; i++ {
		items = append(items, `"roofer"`)
	}
	items = append(items, `{"text":"passport","done":true}`)
	const body = "- [ ] roofer\n- [x] passport"
	if got, _, err := cleanup.SplitOutput(`{"items":[`+strings.Join(items, ",")+`]}`, body); err != nil || strings.Count(got, "\n") != cleanup.MaxChecklistItems-1 {
		t.Errorf("500 items: %v, %d lines", err, strings.Count(got, "\n")+1)
	}
	if _, _, err := cleanup.SplitOutput(`{"items":[{"text":"roofer","children":["roofer"]},`+strings.Join(items, ",")+`]}`, body); !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("501 items counting a sub-item: %v, want ErrNotATaskList", err)
	}
	for _, raw := range []string{"", "\n\n", llm.FenceMarker + "\n\n" + llm.FenceMarker} {
		if _, _, err := cleanup.SplitOutput(raw, body); !errors.Is(err, cleanup.ErrEmptyNoteOutput) {
			t.Errorf("SplitOutput(%q) = %v, want ErrEmptyNoteOutput", raw, err)
		}
	}
}

// Three levels: the extraction keeps one (owner decision 2), the Split up
// keeps the list's, so "one level only" is the items prompt's rule and not
// the tasks prompt's.
func TestOnlyTheItemsPromptIsOneLevel(t *testing.T) {
	items, _, _ := cleanup.ItemsPrompt("milk", "Shopping list", "")
	tasks, _, _ := cleanup.TasksPrompt("- [ ] milk", "Shopping list", "")
	if !strings.Contains(items, "One level only: a child has no children.") {
		t.Error("the items prompt lost its one-level rule")
	}
	if strings.Contains(tasks, "One level only") {
		t.Error("the tasks prompt still says one level only")
	}
}

// A three-level list tidied unchanged is stored as it was; a reply four
// levels deep keeps four (PR10-11), a fifth flattened into the fourth after
// its parent; a dropped parent at the second level lifts its children to
// the second level.
func TestSplitOutputKeepsFourLevels(t *testing.T) {
	const body = "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n    - [x] Cups\n  - [ ] Candles\n- [ ] Milk"
	got, dropped, err := cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates"},{"text":"Cups","done":true}]},{"text":"Candles"}]},{"text":"Milk"}]}`, body)
	if err != nil || dropped != 0 || got != body {
		t.Errorf("an unchanged three-level list = %q, %d, %v", got, dropped, err)
	}
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates","children":[{"text":"Cups","done":true}]}]},{"text":"Candles"}]},{"text":"Milk"}]}`, body)
	if want := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Cups\n  - [ ] Candles\n- [ ] Milk"; err != nil || got != want {
		t.Errorf("a four-level reply = %q, %v; want %q", got, err, want)
	}
	deep := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Cups\n- [ ] Milk"
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates","children":[{"text":"Cups","done":true,"children":[{"text":"Milk"}]}]}]}]}]}`, deep)
	if want := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Cups\n      - [ ] Milk"; err != nil || got != want {
		t.Errorf("a five-level reply = %q, %v; want %q", got, err, want)
	}
	// An invented second-level parent is dropped and its children lifted to
	// its level (dropInvented), then handed back to their own parent, which
	// survived beside it (restoreLevels): the list comes out as it was.
	got, dropped, err = cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Walmart","children":[{"text":"Plates"},{"text":"Cups","done":true}]},{"text":"Costco"},{"text":"Candles"}]},{"text":"Milk"}]}`, body)
	if err != nil || dropped != 1 || got != body {
		t.Errorf("a dropped second-level parent = %q, %d, %v; want the body", got, dropped, err)
	}
	// The invented group taking the list's own group with it is a lost open
	// item, not a drop.
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Walmart","children":[{"text":"Plates"},{"text":"Cups","done":true}]},{"text":"Candles"}]},{"text":"Milk"}]}`, body); !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("Costco replaced by Walmart = %q, %v; want ErrNotATaskList", got, err)
	}
	// Tick safety reaches the third level.
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates"},{"text":"Cups"}]},{"text":"Candles"}]},{"text":"Milk"}]}`, body); !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("a lost tick on a third-level line = %q, %v; want ErrNotATaskList", got, err)
	}
}

// DB6-11, settled as "the parent reopens": a done group that gains an open
// item is open, because a done item has no open descendant. Both of the
// item's bodies: the model reopening the group itself is accepted, and the
// model leaving it done over an open child is stored open.
// A four-level list the model answers flat, or partly flat, every word
// kept: the guards have nothing to refuse, and the levels come back from the
// body — each item under its old parent, the done one still done, the
// dictated sentence split at the top where it stood. An item the model put
// under another group stays there, and a child the model kept with its
// parent is not moved.
func TestSplitOutputRestoresTheLevelsAFlatAnswerLost(t *testing.T) {
	body := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Paper ones\n    - [ ] Cups\n  - [ ] Candles\n- [ ] buy milk and call the plumber"
	want := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Paper ones\n    - [ ] Cups\n  - [ ] Candles\n- [ ] Milk\n- [ ] Call the plumber"
	for name, reply := range map[string]string{
		"flat":   `{"items":[{"text":"Party"},{"text":"Costco"},{"text":"Plates"},{"text":"Paper ones","done":true},{"text":"Cups"},{"text":"Candles"},{"text":"Milk"},{"text":"Call the plumber"}]}`,
		"partly": `{"items":[{"text":"Party","children":[{"text":"Costco"},{"text":"Plates","children":[{"text":"Paper ones","done":true}]},{"text":"Cups"},{"text":"Candles"}]},{"text":"Milk"},{"text":"Call the plumber"}]}`,
		"intact": `{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates","children":[{"text":"Paper ones","done":true}]},{"text":"Cups"}]},{"text":"Candles"}]},{"text":"Milk"},{"text":"Call the plumber"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, dropped, err := cleanup.SplitOutput(reply, body)
			if err != nil || dropped != 0 || got != want {
				t.Errorf("SplitOutput = %q dropped=%d err=%v\nwant %q", got, dropped, err, want)
			}
		})
	}

	// Candles under Costco is the model regrouping, which the prompt allows;
	// Cups at the top, under nothing, goes back under its parent Costco.
	moved := `{"items":[{"text":"Party","children":[{"text":"Costco","children":[{"text":"Plates","children":[{"text":"Paper ones","done":true}]},{"text":"Candles"}]}]},{"text":"Cups"},{"text":"Milk"},{"text":"Call the plumber"}]}`
	got, _, err := cleanup.SplitOutput(moved, body)
	if want := "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Paper ones\n    - [ ] Candles\n    - [ ] Cups\n- [ ] Milk\n- [ ] Call the plumber"; err != nil || got != want {
		t.Errorf("SplitOutput = %q err=%v\nwant %q", got, err, want)
	}

	// Two parents whose names share a prefix: each child goes back under its
	// own, by exact words, whichever the answer names first.
	near := "- [ ] Costco\n  - [ ] Meat\n- [ ] Costco run\n  - [ ] Gas"
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Costco run"},{"text":"Gas"},{"text":"Costco"},{"text":"Meat"}]}`, near)
	if want := "- [ ] Costco run\n  - [ ] Gas\n- [ ] Costco\n  - [ ] Meat"; err != nil || got != want {
		t.Errorf("SplitOutput = %q err=%v\nwant %q", got, err, want)
	}

	// A list with no levels is left exactly as the model answered it.
	flatBody := "- [ ] Milk\n- [x] Eggs"
	got, _, err = cleanup.SplitOutput(`{"items":[{"text":"Milk"},{"text":"Eggs","done":true}]}`, flatBody)
	if err != nil || got != flatBody {
		t.Errorf("SplitOutput = %q err=%v, want the body", got, err)
	}
}

func TestSplitOutputReopensADoneParentThatGainsAnOpenChild(t *testing.T) {
	const body = "- [x] Costco\n  - [x] Meat\n- [ ] chicken from costco"
	const want = "- [ ] Costco\n  - [x] Meat\n  - [ ] Chicken"
	for name, reply := range map[string]string{
		"the answer reopens it":    `{"items":[{"text":"Costco","children":[{"text":"Meat","done":true},{"text":"Chicken"}]}]}`,
		"the answer keeps it done": `{"items":[{"text":"Costco","done":true,"children":[{"text":"Meat","done":true},{"text":"Chicken"}]}]}`,
	} {
		if got, _, err := cleanup.SplitOutput(reply, body); err != nil || got != want {
			t.Errorf("%s: SplitOutput = %q, %v; want %q", name, got, err, want)
		}
	}
	// At any depth: a done second-level group under a done top reopens both.
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Party","done":true,"children":[{"text":"Costco","done":true,"children":[{"text":"Plates","done":true},{"text":"Cups"}]}]}]}`,
		"- [x] Party\n  - [x] Costco\n    - [x] Plates\n- [ ] cups from costco"); err != nil || got != "- [ ] Party\n  - [ ] Costco\n    - [x] Plates\n    - [ ] Cups" {
		t.Errorf("a two-level reopen = %q, %v", got, err)
	}
	// A done line the body already held an open sub-item under is stored
	// open: the list put the item there.
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Party","done":true,"children":[{"text":"Plates"}]}]}`, "- [x] Party\n  - [ ] Plates"); err != nil || got != "- [ ] Party\n  - [ ] Plates" {
		t.Errorf("a done parent over an open sub-item = %q, %v", got, err)
	}
	// A group the model invents over an open line is not the list's: Milk
	// › Eggs out of "- [x] Milk", "- [ ] Eggs" would lose Milk's tick, done
	// or open in the answer.
	for _, reply := range []string{
		`{"items":[{"text":"Milk","children":[{"text":"Eggs"}]}]}`,
		`{"items":[{"text":"Milk","done":true,"children":[{"text":"Eggs"}]}]}`,
	} {
		if got, _, err := cleanup.SplitOutput(reply, "- [x] Milk\n- [ ] Eggs"); !errors.Is(err, cleanup.ErrNotATaskList) {
			t.Errorf("an invented group over a done line = %q, %v; want ErrNotATaskList", got, err)
		}
	}
	// The exemption is exactly that: a done group reopened with no open item
	// under it is still a reopened tick.
	if got, _, err := cleanup.SplitOutput(`{"items":[{"text":"Costco","children":[{"text":"Meat","done":true}]},"Chicken from costco"]}`, body); !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("a done group reopened with nothing open under it = %q, %v; want ErrNotATaskList", got, err)
	}
}

func TestTasksMaxTokensIsThreeTimesTheInputFromAFloor(t *testing.T) {
	if got := cleanup.TasksMaxTokens("- [ ] milk"); got != 512 {
		t.Errorf("TasksMaxTokens(short) = %d, want the floor", got)
	}
	if got := cleanup.TasksMaxTokens(strings.Repeat("x", 4_000)); got != 3*1001 {
		t.Errorf("TasksMaxTokens(4 KB) = %d, want 3,003", got)
	}
}
