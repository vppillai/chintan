package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// evalRecordings is where scripts/dev/record-replay.sh puts the live eval's
// replies, seen from this package.
const evalRecordings = "../provider/testdata/eval/recordings"

// TestRoutingEvalReplay is the routing eval CI can run: every route case in
// the provider's fixtures.json, the model's reply replayed from the owner's
// last recording (provider.TestLiveEval with LLM_RECORD), through the same
// provider parsing and span growth the worker runs and then decide(), and
// the case's expectations asserted on the outcome — so a change to
// preferExistingTitle, spoken_name, the 0.75 bar or the span rules is
// measured against real replies without a key. A prompt change is a replay
// miss until it is re-recorded (docs/design/routing.md, "Replay"). A case
// marked flaky (the live eval saw it answered differently across runs) or
// known_failure (the same wrong answer every run, with the reason) has its
// outcome logged, not asserted.
func TestRoutingEvalReplay(t *testing.T) {
	if files, _ := filepath.Glob(filepath.Join(evalRecordings, "*.json")); len(files) == 0 {
		t.Skip("no eval recordings; record them on the VM with scripts/dev/record-replay.sh and commit the directory")
	}
	t.Setenv("LLM_REPLAY", evalRecordings)
	c, err := provider.NewOpenAICleanup("replay", "", os.Getenv("LLM_MODEL"), nil)
	if err != nil {
		t.Fatal(err)
	}
	fx := loadRouteEval(t)
	active, idOf := fx.notes()
	candidates := make([]routing.Candidate, 0, len(active))
	for _, n := range active {
		candidates = append(candidates, routeCandidate(n))
	}
	for i, tc := range fx.Route.Cases {
		t.Run(fmt.Sprintf("%02d", i+1), func(t *testing.T) {
			reply, err := c.Route(context.Background(), tc.Transcript, candidates, tc.Language)
			if err != nil {
				t.Fatalf("%s | %v", tc.Transcript, err)
			}
			d, matchedBy, outcome := decide(reply, tc.Transcript, active)
			t.Logf("%s | %s %s matched_by=%q %q | %q", tc.Transcript, outcome, d.NoteID, matchedBy, d.Title, d.Content)
			if tc.KnownFailure != "" {
				t.Skipf("known failure of the prompt, logged above, not asserted: %s", tc.KnownFailure)
			}
			if tc.Flaky {
				t.Skip("flaky in the live eval: the recorded reply is one of several the model gives; its outcome is logged above, not asserted")
			}
			tc.check(t, d, outcome, idOf)
		})
	}
}

// The replay path end to end on one hand-written reply: filed under the key
// of today's routing prompt, served by LLM_REPLAY, parsed and span-trimmed
// by the provider, and turned by decide() from the model's "new Roof repair"
// into an append to the listed Roof repair.
func TestRouteReplayRunsTheRecordedReplyThroughDecide(t *testing.T) {
	const transcript = "roof repair the flashing is loose again"
	active := []model.NoteIndex{{ID: "note_0000000000000001_0000000000000001", Title: "Roof repair"}}
	candidates := []routing.Candidate{routeCandidate(active[0])}
	userPrompt, err := routing.UserPrompt(transcript, candidates, "en")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/replay/synthetic-route.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, provider.RecordingKey("MiniMax-M3", routing.SystemPrompt(), userPrompt)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_REPLAY", dir)
	c, err := provider.NewOpenAICleanup("replay", "", "MiniMax-M3", nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Route(context.Background(), transcript, candidates, "en")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Usage.InputTokens != 900 {
		t.Errorf("usage = %+v, want the recorded usage", reply.Usage)
	}
	d, matchedBy, outcome := decide(reply, transcript, active)
	if outcome != outcomeAppend || d.NoteID != active[0].ID || matchedBy != "title" || d.Content != "the flashing is loose again" {
		t.Errorf("decide = %s into %q by %q, content %q; want an append to Roof repair by title with the name removed", outcome, d.NoteID, matchedBy, d.Content)
	}
}

// routeEval is the route section of fixtures.json. The provider's
// TestEvalFixturesParse owns the schema and refuses unknown keys; this reads
// the same file leniently and keeps only what an outcome is judged on.
type routeEval struct {
	Route struct {
		Candidates []struct {
			Title   string   `json:"title"`
			Aliases []string `json:"aliases"`
			Tags    []string `json:"tags"`
		} `json:"candidates"`
		Cases []routeEvalCase `json:"cases"`
	} `json:"route"`
}

type routeEvalCase struct {
	Transcript       string   `json:"transcript"`
	Language         string   `json:"language"`
	Flaky            bool     `json:"flaky"`
	KnownFailure     string   `json:"known_failure"`
	Action           string   `json:"action"`
	ActionIn         []string `json:"action_in"`
	Dest             string   `json:"note"`
	Title            string   `json:"title"`
	Kind             string   `json:"kind"`
	TitleNames       string   `json:"title_names"`
	TitleExcludes    []string `json:"title_excludes"`
	TitleScript      string   `json:"title_script"`
	Content          *string  `json:"content"`
	ContentContains  []string `json:"content_contains"`
	ContentExcludes  []string `json:"content_excludes"`
	ContentUnchanged bool     `json:"content_unchanged"`
}

func loadRouteEval(t *testing.T) routeEval {
	t.Helper()
	b, err := os.ReadFile("../provider/testdata/eval/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx routeEval
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// notes are the candidates as the store would hold them, with the ids the
// provider's evalID gives them: the user prompt, and so the recording's key,
// depends on every byte of the candidate list.
func (fx routeEval) notes() ([]model.NoteIndex, map[string]string) {
	idOf := map[string]string{}
	notes := make([]model.NoteIndex, 0, len(fx.Route.Candidates))
	for i, c := range fx.Route.Candidates {
		id := fmt.Sprintf("note_%016x_%016x", i+1, i+1)
		idOf[c.Title] = id
		notes = append(notes, model.NoteIndex{ID: id, Title: c.Title, Aliases: c.Aliases, Tags: c.Tags})
	}
	return notes, idOf
}

// check judges the outcome, not the raw reply: "append" means filed without
// asking (a park at needs_target fails it), "new" means a note is started,
// and title_names means the note it names is where the recording went.
func (tc routeEvalCase) check(t *testing.T, d provider.RouteDecision, outcome string, idOf map[string]string) {
	t.Helper()
	if tc.Action != "" && outcome != tc.Action {
		t.Errorf("outcome = %s, want %s", outcome, tc.Action)
	}
	if len(tc.ActionIn) > 0 && !slices.Contains(tc.ActionIn, outcome) {
		t.Errorf("outcome = %s, want one of %v", outcome, tc.ActionIn)
	}
	if tc.Dest != "" && d.NoteID != idOf[tc.Dest] {
		t.Errorf("note = %q, want %q", d.NoteID, tc.Dest)
	}
	if tc.TitleNames != "" && (outcome != outcomeAppend || d.NoteID != idOf[tc.TitleNames]) {
		t.Errorf("outcome = %s into %q, want an append to %q", outcome, d.NoteID, tc.TitleNames)
	}
	if outcome == outcomeNew {
		if tc.Title != "" && !strings.EqualFold(d.Title, tc.Title) {
			t.Errorf("title = %q, want %q", d.Title, tc.Title)
		}
		if tc.Kind != "" && d.Checklist != (tc.Kind == model.NoteKindChecklist) {
			t.Errorf("checklist = %v, want kind %s", d.Checklist, tc.Kind)
		}
		evalText(t, "title", d.Title, nil, tc.TitleExcludes)
		if tc.TitleScript != "" {
			for _, r := range d.Title {
				if unicode.IsLetter(r) && !unicode.Is(unicode.Scripts[tc.TitleScript], r) {
					t.Errorf("title %q has %q outside %s", d.Title, r, tc.TitleScript)
					break
				}
			}
		}
	}
	if tc.Content != nil && d.Content != *tc.Content {
		t.Errorf("content = %q, want %q", d.Content, *tc.Content)
	}
	evalText(t, "content", d.Content, tc.ContentContains, tc.ContentExcludes)
	if tc.ContentUnchanged && d.Content != tc.Transcript {
		t.Errorf("content = %q, want the transcript unchanged", d.Content)
	}
}

func evalText(t *testing.T, what, got string, contains, excludes []string) {
	t.Helper()
	lower := strings.ToLower(got)
	for _, s := range contains {
		if !strings.Contains(lower, strings.ToLower(s)) {
			t.Errorf("%s lacks %q: %q", what, s, got)
		}
	}
	for _, s := range excludes {
		if strings.Contains(lower, strings.ToLower(s)) {
			t.Errorf("%s contains %q: %q", what, s, got)
		}
	}
}
