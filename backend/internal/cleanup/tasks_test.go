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
	ok := func(raw, want string) {
		t.Helper()
		got, err := cleanup.NoteOutput(model.NoteCleanTasks, raw)
		if err != nil || got != want {
			t.Errorf("NoteOutput(tasks, %q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	refused := func(name, raw string) {
		t.Helper()
		if got, err := cleanup.NoteOutput(model.NoteCleanTasks, raw); !errors.Is(err, cleanup.ErrNotATaskList) {
			t.Errorf("%s: NoteOutput(tasks, %q) = %q, %v; want ErrNotATaskList", name, raw, got, err)
		}
	}

	ok("- [ ] call the roofer\n- [x] passport", "- [ ] call the roofer\n- [x] passport")
	ok("\n  - [ ] call the roofer  \n\n- [x] passport\n", "- [ ] call the roofer\n- [x] passport")
	ok(llm.FenceMarker+"\n- [ ] call the roofer\n"+llm.FenceMarker, "- [ ] call the roofer")
	ok("- [ ] a [x] inside the text is fine", "- [ ] a [x] inside the text is fine")

	refused("prose", "Call the roofer, then buy sealant.")
	refused("a heading before the list", "# Tasks\n- [ ] call the roofer")
	refused("a plain bullet", "- call the roofer")
	refused("an item with no text", "- [ ] ")
	refused("a capital X", "- [X] passport")
	refused("a numbered list", "1. call the roofer")

	// The item cap: five hundred is stored, one more is refused.
	items := make([]string, 0, cleanup.MaxChecklistItems+1)
	for i := 0; i < cleanup.MaxChecklistItems; i++ {
		items = append(items, "- [ ] task")
	}
	ok(strings.Join(items, "\n"), strings.Join(items, "\n"))
	refused("501 items", strings.Join(append(items, "- [ ] one too many"), "\n"))

	// Nothing at all is still the empty verdict, not a task-list one.
	if _, err := cleanup.NoteOutput(model.NoteCleanTasks, "\n\n"); !errors.Is(err, cleanup.ErrEmptyNoteOutput) {
		t.Errorf("NoteOutput(tasks, blank) = %v, want ErrEmptyNoteOutput", err)
	}
	// The other modes are not held to the list format.
	if got, err := cleanup.NoteOutput(model.NoteCleanStructured, "# Roof\n\nprose"); err != nil || got != "# Roof\n\nprose" {
		t.Errorf("structured output was reshaped: %q, %v", got, err)
	}
}
