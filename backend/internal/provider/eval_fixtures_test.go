package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// TestEvalFixturesParse is the offline half: the file decodes with unknown
// keys refused, every case carries at least one expectation, and every name a
// case uses — a destination or source title, a Unicode script — is one the
// file or the runtime knows.
func TestEvalFixturesParse(t *testing.T) {
	fx := loadEvalFixtures(t)
	_, candidate := fx.candidates()
	_, note := fx.packedNotes()
	for i, tc := range fx.Route.Cases {
		for _, title := range []string{tc.Dest, tc.TitleNames} {
			if _, ok := candidate[title]; title != "" && !ok {
				t.Errorf("route case %d names %q, which is not a candidate", i+1, title)
			}
		}
		checkScriptName(t, "route", i, tc.TitleScript)
		if tc.Kind != "" && tc.Kind != "note" && tc.Kind != "checklist" {
			t.Errorf("route case %d: kind %q is not note or checklist", i+1, tc.Kind)
		}
	}
	for i, tc := range fx.Cleanup.Cases {
		checkScriptName(t, "cleanup", i, tc.Script)
	}
	for i, tc := range fx.Items.Cases {
		checkScriptName(t, "items", i, tc.Script)
	}
	for i, tc := range fx.Ask.Cases {
		for _, title := range tc.Sources {
			if _, ok := note[title]; !ok {
				t.Errorf("ask case %d cites %q, which is not a fixture note", i+1, title)
			}
		}
	}

	// The guard itself: a misspelt key is refused, not ignored.
	dec := json.NewDecoder(strings.NewReader(`{"route":{"cases":[{"transcript":"x","actoin":"new"}]}}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&evalFixtures{}); err == nil {
		t.Error("a case with an unknown key decoded; a typo in an expectation would pass the live run silently")
	}

	// Every case has to assert something, or it only spends money.
	inputs := map[string]bool{"transcript": true, "language": true, "raw": true, "body": true, "list_title": true, "question": true, "_note": true, "flaky": true, "known_failure": true}
	raw, err := os.ReadFile(evalFixturesPath)
	if err != nil {
		t.Fatal(err)
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"route", "cleanup", "items", "tasks", "ask"} {
		var sec struct {
			Cases []map[string]json.RawMessage `json:"cases"`
		}
		if err := json.Unmarshal(sections[name], &sec); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sec.Cases) == 0 {
			t.Errorf("%s has no cases", name)
		}
		for i, c := range sec.Cases {
			expectations := 0
			for k := range c {
				if !inputs[k] {
					expectations++
				}
			}
			if expectations == 0 {
				t.Errorf("%s case %d asserts nothing", name, i+1)
			}
		}
	}
}

const evalFixturesPath = "testdata/eval/fixtures.json"

// evalFixtures mirrors fixtures.json. Every key a case may carry is a field
// here, and the file is decoded with unknown fields refused, so a misspelt
// expectation fails TestEvalFixturesParse rather than silently passing the
// live run.
type evalFixtures struct {
	Comment string `json:"_comment"`
	Route   struct {
		Candidates []struct {
			Title   string   `json:"title"`
			Aliases []string `json:"aliases"`
			Tags    []string `json:"tags"`
		} `json:"candidates"`
		Cases []routeCase `json:"cases"`
	} `json:"route"`
	Cleanup struct {
		Cases []cleanupCase `json:"cases"`
	} `json:"cleanup"`
	Items struct {
		ListTitle string      `json:"list_title"`
		Cases     []itemsCase `json:"cases"`
	} `json:"items"`
	Tasks struct {
		ListTitle string      `json:"list_title"`
		Cases     []tasksCase `json:"cases"`
	} `json:"tasks"`
	Ask struct {
		Notes []struct {
			Title   string `json:"title"`
			Updated string `json:"updated"`
			Text    string `json:"text"`
		} `json:"notes"`
		Cases []askCase `json:"cases"`
	} `json:"ask"`
}

// Two keys a route, items or tasks case may carry about the model, not the
// case. flaky says the live eval saw the model answer it differently across
// runs: the replay logs the recorded reply's outcome and skips the
// expectation, since whichever reply was kept is one of several the model
// gives. known_failure names a case the model answers the same wrong way
// every run: the replay logs the outcome and skips with that reason, and
// the key is cleared only when a prompt change fixes the case. The live
// eval asserts both as it does every other case.
type routeCase struct {
	Transcript       string   `json:"transcript"`
	Language         string   `json:"language"`
	Comment          string   `json:"_note"`
	Flaky            bool     `json:"flaky"`
	KnownFailure     string   `json:"known_failure"`
	Action           string   `json:"action"`
	ActionIn         []string `json:"action_in"`
	Dest             string   `json:"note"`
	MinConfidence    *float64 `json:"min_confidence"`
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

type cleanupCase struct {
	Language      string   `json:"language"`
	Raw           string   `json:"raw"`
	Comment       string   `json:"_note"`
	Contains      []string `json:"contains"`
	Excludes      []string `json:"excludes"`
	Subsequence   bool     `json:"subsequence"`
	MaxWordsRatio float64  `json:"max_words_ratio"`
	Script        string   `json:"script"`
	Flaky         bool     `json:"flaky"`
	KnownFailure  string   `json:"known_failure"`
}

// itemsCase: want is the tree as RenderItems writes it, two spaces before a
// child; count and count_in count children; top_level counts parents alone.
type itemsCase struct {
	Transcript   string   `json:"transcript"`
	Language     string   `json:"language"`
	Comment      string   `json:"_note"`
	Flaky        bool     `json:"flaky"`
	KnownFailure string   `json:"known_failure"`
	Want         []string `json:"want"`
	Count        *int     `json:"count"`
	CountIn      []int    `json:"count_in"`
	TopLevel     *int     `json:"top_level"`
	ContainsAny  []string `json:"contains_any"`
	ExcludesAny  []string `json:"excludes_any"`
	Script       string   `json:"script"`
}

// tasksCase: want is task-list lines, indent included, as SplitOutput
// stores them; list_title, when set, is the title the user prompt names
// instead of the section's, for a case about the list's own name.
type tasksCase struct {
	Body          string   `json:"body"`
	ListTitle     string   `json:"list_title"`
	Comment       string   `json:"_note"`
	Flaky         bool     `json:"flaky"`
	KnownFailure  string   `json:"known_failure"`
	Want          []string `json:"want"`
	WantUnchanged bool     `json:"want_unchanged"`
	Count         *int     `json:"count"`
	CountIn       []int    `json:"count_in"`
	ExcludesAny   []string `json:"excludes_any"`
}

type askCase struct {
	Question     string   `json:"question"`
	Comment      string   `json:"_note"`
	Grounded     *bool    `json:"grounded"`
	Sources      []string `json:"sources"`
	Contains     []string `json:"contains"`
	Excludes     []string `json:"excludes"`
	Flaky        bool     `json:"flaky"`
	KnownFailure string   `json:"known_failure"`
}

func loadEvalFixtures(t *testing.T) evalFixtures {
	t.Helper()
	f, err := os.Open(evalFixturesPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var fx evalFixtures
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("%s: %v", evalFixturesPath, err)
	}
	return fx
}

// evalID is a note id in the shape the store issues, so the prompt is
// measured on what production sends. The cases name notes by title; these
// maps carry the title to the id and back.
func evalID(i int) string { return fmt.Sprintf("note_%016x_%016x", i+1, i+1) }

func (fx evalFixtures) candidates() ([]routing.Candidate, map[string]string) {
	idOf := make(map[string]string, len(fx.Route.Candidates))
	candidates := make([]routing.Candidate, 0, len(fx.Route.Candidates))
	for i, c := range fx.Route.Candidates {
		idOf[c.Title] = evalID(i)
		candidates = append(candidates, routing.Candidate{NoteID: evalID(i), Title: c.Title, Aliases: c.Aliases, Tags: c.Tags})
	}
	return candidates, idOf
}

func (fx evalFixtures) packedNotes() ([]ask.Packed, map[string]string) {
	idOf := make(map[string]string, len(fx.Ask.Notes))
	notes := make([]ask.Packed, 0, len(fx.Ask.Notes))
	for i, n := range fx.Ask.Notes {
		idOf[n.Title] = evalID(i)
		notes = append(notes, ask.Packed{NoteID: evalID(i), Title: n.Title, Updated: n.Updated, Text: n.Text})
	}
	return notes, idOf
}

func checkScriptName(t *testing.T, section string, i int, script string) {
	t.Helper()
	if script != "" && unicode.Scripts[script] == nil {
		t.Errorf("%s case %d: %q is not a Unicode script name", section, i+1, script)
	}
}
