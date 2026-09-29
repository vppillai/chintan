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
		"the list it was meant to be", "a sub-item indented two spaces",
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
// with a done line's words is refused unless the body also had it open; a
// done answer item with no done line's words is a tick the model added.
func TestSplitOutputRefusesALostReopenedOrInventedTick(t *testing.T) {
	const body = "- [ ] call the roofer and buy sealant\n- [x] passport.\n- [x] pay the electricity bill"
	for name, reply := range map[string]string{
		"a done item lost":       `{"items":["Call the roofer","Buy sealant",{"text":"pay the electricity bill","done":true}]}`,
		"a done item reopened":   `{"items":["Call the roofer","Buy sealant","Passport",{"text":"pay the electricity bill","done":true}]}`,
		"a done item reworded":   `{"items":["Call the roofer","Buy sealant",{"text":"passport renewed","done":true},{"text":"pay the electricity bill","done":true}]}`,
		"a done item invented":   `{"items":[{"text":"Call the roofer","done":true},"Buy sealant",{"text":"passport","done":true},{"text":"pay the electricity bill","done":true}]}`,
		"not a list":             "Call the roofer, then buy sealant.",
		"a done sub-item opened": `{"items":["Call the roofer","Buy sealant",{"text":"Errands","children":[{"text":"passport"},{"text":"pay the electricity bill","done":true}]}]}`,
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
	// when the body also has an open Milk.
	got, _, err = cleanup.SplitOutput(`{"items":["Milk","Eggs"]}`, "- [ ] Milk\n- [x] milk\n- [ ] Eggs")
	if err != nil || got != "- [ ] Milk\n- [ ] Eggs" {
		t.Errorf("open did not win over a done duplicate: %q, %v", got, err)
	}
	// A "[x]" inside an item's text is text.
	if got, _, err := cleanup.SplitOutput(`{"items":["a [x] inside the text is fine"]}`, "- [ ] a [x] inside the text is fine"); err != nil || got != "- [ ] a [x] inside the text is fine" {
		t.Errorf("SplitOutput([x] in text) = %q, %v", got, err)
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

func TestTasksMaxTokensIsThreeTimesTheInputFromAFloor(t *testing.T) {
	if got := cleanup.TasksMaxTokens("- [ ] milk"); got != 512 {
		t.Errorf("TasksMaxTokens(short) = %d, want the floor", got)
	}
	if got := cleanup.TasksMaxTokens(strings.Repeat("x", 4_000)); got != 3*1001 {
		t.Errorf("TasksMaxTokens(4 KB) = %d, want 3,003", got)
	}
}
