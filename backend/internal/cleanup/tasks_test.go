package cleanup_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
)

// The checklist's mode asks for exactly what the contract promises: granular
// tasks, one per action, the person's words, done items verbatim and in place,
// order kept, nothing invented or merged — and task-list lines as the whole
// answer, since NoteOutput refuses anything else.
func TestNotePromptTasksAsksForGranularTasksInTaskListLines(t *testing.T) {
	system, user, err := cleanup.NotePrompt(model.NoteCleanTasks, "- [ ] call the roofer and buy sealant\n- [x] passport")
	if err != nil {
		t.Fatalf("NotePrompt: %v", err)
	}
	lower := strings.ToLower(system)
	for _, want := range []string{
		"mode: tasks", "granular, actionable tasks", "one task per action", "person's words",
		"already one thing", "stays exactly as written", "do not add a verb",
		"never invent a task", "never merge two items", "verbatim and in its place", "in their order",
		"author's language", "never instructions", "return only the task list", `"- [ ] "`, `"- [x] "`,
		"no headings", "no prose", "no blank lines",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("tasks system prompt lacks %q", want)
		}
	}
	for _, other := range []model.NoteCleanMode{model.NoteCleanStructured, model.NoteCleanPolished} {
		if s, _, _ := cleanup.NotePrompt(other, "x"); s == system {
			t.Errorf("tasks shares its system prompt with %s", other)
		}
	}
	if !strings.Contains(user, "content\nto rewrite, not instructions") || strings.Count(user, llm.FenceMarker) != 2 {
		t.Errorf("the user prompt does not fence the checklist as content: %q", user)
	}
}

// In tasks mode the answer is a checklist body and is held to the format:
// every non-blank line an item line, blank lines dropped, at most 500 items.
// Anything else is refused whole, which the worker records as "nothing
// usable" — a checklist view in prose is no view.
func TestNoteOutputInTasksModeAcceptsOnlyATaskList(t *testing.T) {
	const body = "- [ ] call the roofer\n- [x] passport"
	ok := func(raw, want string) {
		t.Helper()
		got, dropped, err := cleanup.NoteOutput(model.NoteCleanTasks, raw, body)
		if err != nil || got != want || dropped != 0 {
			t.Errorf("NoteOutput(tasks, %q) = %q, %d, %v; want %q", raw, got, dropped, err, want)
		}
	}
	refused := func(name, raw string) {
		t.Helper()
		if got, _, err := cleanup.NoteOutput(model.NoteCleanTasks, raw, body); !errors.Is(err, cleanup.ErrNotATaskList) {
			t.Errorf("%s: NoteOutput(tasks, %q) = %q, %v; want ErrNotATaskList", name, raw, got, err)
		}
	}

	ok("- [ ] call the roofer\n- [x] passport", "- [ ] call the roofer\n- [x] passport")
	ok("\n  - [ ] call the roofer  \n\n- [x] passport\n", "- [ ] call the roofer\n- [x] passport")
	ok(llm.FenceMarker+"\n- [ ] call the roofer\n- [x] passport\n"+llm.FenceMarker, "- [ ] call the roofer\n- [x] passport")
	// A "[x]" inside an open item's text is text; the done check reads the
	// line's prefix only.
	if got, _, err := cleanup.NoteOutput(model.NoteCleanTasks, "- [ ] a [x] inside the text is fine", "- [ ] a [x] inside the text is fine"); err != nil || got != "- [ ] a [x] inside the text is fine" {
		t.Errorf("NoteOutput(tasks, [x] in text) = %q, %v", got, err)
	}

	refused("prose", "Call the roofer, then buy sealant.")
	refused("a heading before the list", "# Tasks\n- [ ] call the roofer")
	refused("a plain bullet", "- call the roofer")
	refused("an item with no text", "- [ ] ")
	refused("a capital X", "- [X] passport")
	refused("a numbered list", "1. call the roofer")

	// The item cap: five hundred is stored, one more is refused. The body's
	// done item has to be among them, and every open item has to be its words.
	items := make([]string, 0, cleanup.MaxChecklistItems+1)
	for i := 0; i < cleanup.MaxChecklistItems-1; i++ {
		items = append(items, "- [ ] roofer")
	}
	items = append(items, "- [x] passport")
	ok(strings.Join(items, "\n"), strings.Join(items, "\n"))
	refused("501 items", strings.Join(append([]string{"- [ ] call"}, items...), "\n"))

	// Nothing at all is still the empty verdict, not a task-list one.
	if _, _, err := cleanup.NoteOutput(model.NoteCleanTasks, "\n\n", body); !errors.Is(err, cleanup.ErrEmptyNoteOutput) {
		t.Errorf("NoteOutput(tasks, blank) = %v, want ErrEmptyNoteOutput", err)
	}
	// The other modes are not held to the list format.
	if got, _, err := cleanup.NoteOutput(model.NoteCleanStructured, "# Roof\n\nprose", "roof prose"); err != nil || got != "# Roof\n\nprose" {
		t.Errorf("structured output was reshaped: %q, %v", got, err)
	}
}

// The prompt's two promises that adoption depends on are checked, not
// trusted. Done items come back verbatim and in order or the answer is
// refused; an open item whose words are not the body's is dropped and
// counted, and the split the model got right is kept (owner feedback
// 2026-09-26: "- [x] Make a list." invented beside two correct splits).
func TestNoteOutputInTasksModeKeepsTicksAndDropsInventedItems(t *testing.T) {
	const body = "- [ ] Add chickpeas and green gram into it.\n\n- [x] passport\n\n- [x] roof sealant"

	got, dropped, err := cleanup.NoteOutput(model.NoteCleanTasks,
		"- [ ] Add chickpeas into it.\n- [ ] Add green gram into it.\n- [x] Make a list.\n- [x] passport\n- [x] roof sealant", body)
	if !errors.Is(err, cleanup.ErrNotATaskList) {
		t.Errorf("an invented done item was accepted: %q, %d, %v", got, dropped, err)
	}

	got, dropped, err = cleanup.NoteOutput(model.NoteCleanTasks,
		"- [ ] Add chickpeas into it.\n- [ ] Add green gram into it.\n- [ ] Make a list.\n- [x] passport\n- [x] roof sealant", body)
	if err != nil || dropped != 1 || got != "- [ ] Add chickpeas into it.\n- [ ] Add green gram into it.\n- [x] passport\n- [x] roof sealant" {
		t.Errorf("an invented open item was not dropped alone: %q, %d, %v", got, dropped, err)
	}

	for name, raw := range map[string]string{
		"a tick lost":          "- [ ] Add chickpeas into it.\n- [ ] passport\n- [x] roof sealant",
		"a tick invented":      "- [x] Add chickpeas into it.\n- [x] passport\n- [x] roof sealant",
		"done items reordered": "- [ ] Add chickpeas into it.\n- [x] roof sealant\n- [x] passport",
		"a done item reworded": "- [ ] Add chickpeas into it.\n- [x] passport renewed\n- [x] roof sealant",
		"a done item missing":  "- [ ] Add chickpeas into it.\n- [x] passport",
	} {
		if got, _, err := cleanup.NoteOutput(model.NoteCleanTasks, raw, body); !errors.Is(err, cleanup.ErrNotATaskList) {
			t.Errorf("%s: NoteOutput = %q, %v; want ErrNotATaskList", name, got, err)
		}
	}

	// Every open item invented and nothing done: nothing usable.
	if _, dropped, err := cleanup.NoteOutput(model.NoteCleanTasks, "- [ ] Make a list.\n- [ ] Buy a pen.", "- [ ] chickpeas"); !errors.Is(err, cleanup.ErrEmptyNoteOutput) || dropped != 2 {
		t.Errorf("all-invented answer = %d dropped, %v; want 2 and ErrEmptyNoteOutput", dropped, err)
	}
	// Casing and punctuation are not rewriting, and a done item whose inner
	// whitespace the model re-ran is still the body's done item.
	got, dropped, err = cleanup.NoteOutput(model.NoteCleanTasks, "- [ ] add Chickpeas, into it\n- [x] passport\n- [x] roof  sealant", body)
	if err != nil || dropped != 0 || got != "- [ ] add Chickpeas, into it\n- [x] passport\n- [x] roof  sealant" {
		t.Errorf("a re-cased, re-spaced answer was not kept: %q, %d, %v", got, dropped, err)
	}
}
