package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
)

// The replay path for Tidy up list on one recorded answer: a four-level
// list the model returned flat, filed under the key of today's tasks prompt,
// served by LLM_REPLAY through the provider's CleanNote and then
// cleanup.SplitOutput — the two calls the worker's clean-note task makes —
// so the stored view has every level back, the dictated sentence split at
// the top where it stood, and the done line still done.
func TestTasksReplayRestoresTheLevelsAFlatAnswerLost(t *testing.T) {
	const body = "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Paper ones\n    - [ ] Cups\n  - [ ] Candles\n- [ ] Milk\n- [ ] buy eggs and call the plumber"
	const want = "- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n      - [x] Paper ones\n    - [ ] Cups\n  - [ ] Candles\n- [ ] Milk\n- [ ] Eggs\n- [ ] Call the plumber"
	system, user, err := cleanup.TasksPrompt(body, "Party", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/replay/synthetic-tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, provider.RecordingKey("MiniMax-M3", system, user)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_REPLAY", dir)
	c, err := provider.NewOpenAICleanup("replay", "", "MiniMax-M3", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.CleanNote(context.Background(), model.NoteCleanTasks, body, "", "Party")
	if err != nil {
		t.Fatal(err)
	}
	if out.Usage.InputTokens != 1100 {
		t.Errorf("usage = %+v, want the recorded usage", out.Usage)
	}
	got, dropped, err := cleanup.SplitOutput(out.Text, body)
	if err != nil || dropped != 0 || got != want {
		t.Errorf("SplitOutput = %q dropped=%d err=%v\nwant %q", got, dropped, err, want)
	}
}
