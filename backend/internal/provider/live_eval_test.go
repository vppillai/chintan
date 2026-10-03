package provider

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/model"
)

// TestLiveEval runs every prompt against the real model over the cases in
// testdata/eval/fixtures.json. It is skipped unless LIVE_LLM=1, because it
// costs money and needs the instance's key; the owner runs it with
//
//	LIVE_LLM=1 LLM_API_KEY=… go test ./internal/provider -run TestLiveEval -v -count=3
//	LIVE_LLM=1 LLM_API_KEY=… go test ./internal/provider -run 'TestLiveEval/route' -v
//
// and -count=3 because a prompt that passes once is not yet a prompt that
// passes. After the last of the -count runs it logs each case's pass rate
// with a 95% Wilson interval, so -count=10 says how often, not only whether.
// LLM_RECORD=<dir> also writes every reply for the CI replay (TestEvalReplay
// and pipeline.TestRoutingEvalReplay; scripts/dev/record-replay.sh runs it
// that way; docs/design/routing.md, "Replay"). LLM_BASE_URL and LLM_MODEL
// default to the worker's. The key is read from the environment and never
// printed; the output is the fixture text and the model's reply to it, one
// line per case, and nothing else. Adding a case is appending an object to
// the fixtures file; TestEvalFixturesParse, which always runs, refuses a
// misspelt key so a typo fails CI without a key.
func TestLiveEval(t *testing.T) {
	if os.Getenv("LIVE_LLM") != "1" {
		t.Skip("set LIVE_LLM=1 and LLM_API_KEY to evaluate the prompts against the real model")
	}
	key := os.Getenv("LLM_API_KEY")
	if key == "" {
		t.Fatal("LLM_API_KEY is required with LIVE_LLM=1")
	}
	c, err := NewOpenAICleanup(key, os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_MODEL"), &http.Client{Timeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reportPassRates(t) })
	runEval(t, c, false)
}

// evalRecordings is where scripts/dev/record-replay.sh puts the live eval's
// replies, one file per prompt (RecordingKey), and where the replay reads
// them. The pipeline's routing replay reads the same directory.
const evalRecordings = "testdata/eval/recordings"

// TestEvalReplay is the eval CI runs: the same cases and the same checks as
// TestLiveEval over the replies the owner recorded, served by LLM_REPLAY with
// no key and no call. A case marked flaky in the fixtures has its outcome
// logged, not asserted. It skips only while the recordings directory is
// empty; a prompt, model or fixture change is a replay miss, which fails
// with the re-record command. The route cases are also replayed through the
// pipeline's decide() by pipeline.TestRoutingEvalReplay, which judges the
// outcome rather than the reply.
func TestEvalReplay(t *testing.T) {
	if files, _ := filepath.Glob(filepath.Join(evalRecordings, "*.json")); len(files) == 0 {
		t.Skip("no eval recordings; record them on the VM with scripts/dev/record-replay.sh and commit the directory")
	}
	t.Setenv("LLM_REPLAY", evalRecordings)
	c, err := NewOpenAICleanup("replay", "", os.Getenv("LLM_MODEL"), nil)
	if err != nil {
		t.Fatal(err)
	}
	runEval(t, c, true)
}

// runEval is the eval's sub-tests over one client, live or replaying.
// replay is what the flaky mark means: a flaky case's call still has to be
// served (a miss is a stale recording set), its outcome is logged, and the
// expectation is skipped.
func runEval(t *testing.T, c *OpenAICleanup, replay bool) {
	fx := loadEvalFixtures(t)
	ctx := context.Background()
	flaky := func(t *testing.T, is bool) {
		t.Helper()
		if replay && is {
			t.Skip("flaky in the live eval: the recorded reply is one of several the model gives; its outcome is logged above, not asserted")
		}
	}

	t.Run("route", func(t *testing.T) {
		candidates, idOf := fx.candidates()
		for i, tc := range fx.Route.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				tally(t)
				d, err := c.Route(ctx, tc.Transcript, candidates, tc.Language)
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Transcript, err)
				}
				t.Logf("%s | %s %s %q checklist=%v conf=%.2f | %q", tc.Transcript, d.Action, d.NoteID, d.Title, d.Checklist, d.Confidence, d.Content)
				flaky(t, tc.Flaky)
				checkRoute(t, tc, d, idOf)
			})
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		for i, tc := range fx.Cleanup.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				tally(t)
				out, err := c.Cleanup(ctx, tc.Raw, tc.Language)
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Raw, err)
				}
				t.Logf("%s | %s", tc.Raw, out.Text)
				checkText(t, "cleaned text", out.Text, tc.Contains, tc.Excludes)
				if tc.Subsequence && !llm.VerifySubsequence(out.Text, tc.Raw) {
					t.Errorf("cleaned text is not the transcript with words deleted")
				}
				if tc.MaxWordsRatio > 0 && float64(len(strings.Fields(out.Text))) > tc.MaxWordsRatio*float64(len(strings.Fields(tc.Raw))) {
					t.Errorf("cleaned text has %d words for %d spoken, over %.1fx", len(strings.Fields(out.Text)), len(strings.Fields(tc.Raw)), tc.MaxWordsRatio)
				}
				checkScript(t, "cleaned text", out.Text, tc.Script)
			})
		}
	})
	t.Run("items", func(t *testing.T) {
		for i, tc := range fx.Items.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				tally(t)
				out, err := c.Items(ctx, tc.Transcript, fx.Items.ListTitle, tc.Language)
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Transcript, err)
				}
				// The tree as lines, two spaces before a child; the case's
				// want is written the same way. Casing is the model's.
				var lines []string
				if rendered := cleanup.RenderItems(out.Items); rendered != "" {
					lines = strings.Split(rendered, "\n")
				}
				joined := strings.Join(lines, " · ")
				t.Logf("%s | %s", tc.Transcript, joined)
				flaky(t, tc.Flaky)
				if tc.Want != nil && !equalLines(lines, tc.Want, true) {
					t.Errorf("items = %s, want %s", joined, strings.Join(tc.Want, " · "))
				}
				checkCount(t, "items", len(lines), tc.Count, tc.CountIn)
				if tc.TopLevel != nil && len(out.Items) != *tc.TopLevel {
					t.Errorf("%d top-level items, want %d: %s", len(out.Items), *tc.TopLevel, joined)
				}
				if len(tc.ContainsAny) > 0 && !containsAny(joined, tc.ContainsAny) {
					t.Errorf("no item contains any of %q", tc.ContainsAny)
				}
				checkText(t, "items", joined, nil, tc.ExcludesAny)
				checkScript(t, "items", joined, tc.Script)
			})
		}
	})
	t.Run("tasks", func(t *testing.T) {
		for i, tc := range fx.Tasks.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				tally(t)
				title := fx.Tasks.ListTitle
				if tc.ListTitle != "" {
					title = tc.ListTitle
				}
				out, err := c.CleanNote(ctx, model.NoteCleanTasks, tc.Body, "", title)
				if err != nil {
					t.Fatalf("%q | ERROR %v", tc.Body, err)
				}
				text, dropped, err := cleanup.SplitOutput(out.Text, tc.Body)
				t.Logf("%q | %q dropped=%d", tc.Body, out.Text, dropped)
				flaky(t, tc.Flaky)
				if err != nil {
					t.Fatalf("SplitOutput refused the reply: %v", err)
				}
				lines := strings.Split(text, "\n")
				// Casing of a split item is the model's; the words are asserted.
				if tc.Want != nil && !equalLines(lines, tc.Want, true) {
					t.Errorf("tasks = %q, want %q", lines, tc.Want)
				}
				if tc.WantUnchanged && !equalLines(lines, strings.Split(strings.TrimSpace(tc.Body), "\n"), true) {
					t.Errorf("tasks = %q, want the body unchanged", text)
				}
				checkCount(t, "tasks", len(lines), tc.Count, tc.CountIn)
				checkText(t, "tasks", text, nil, tc.ExcludesAny)
			})
		}
	})
	t.Run("ask", func(t *testing.T) {
		notes, idOf := fx.packedNotes()
		for i, tc := range fx.Ask.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				tally(t)
				// A fixed date: the date is in the system prompt, and so in
				// the recording's key; the fixture notes are dated too.
				a, err := c.Ask(ctx, ask.Prompt{Today: "2026-10-03", Notes: notes, Question: tc.Question})
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Question, err)
				}
				t.Logf("%s | grounded=%v sources=%v | %s", tc.Question, a.Grounded, a.Sources, a.Text)
				if tc.Grounded != nil && a.Grounded != *tc.Grounded {
					t.Errorf("grounded = %v, want %v", a.Grounded, *tc.Grounded)
				}
				for _, title := range tc.Sources {
					if !containsString(a.Sources, idOf[title]) {
						t.Errorf("sources %v do not cite %q", a.Sources, title)
					}
				}
				checkText(t, "answer", a.Text, tc.Contains, tc.Excludes)
			})
		}
	})
}

func caseName(i int) string { return fmt.Sprintf("%02d", i+1) }

// passes counts, per case name, the runs that passed and the runs made,
// across the -count repetitions of TestLiveEval in one process.
var (
	passesMu sync.Mutex
	passes   = map[string][2]int{}
	evalRuns int
)

// tally counts this case's run once it has finished, failed or not.
func tally(t *testing.T) {
	t.Cleanup(func() {
		passesMu.Lock()
		defer passesMu.Unlock()
		p := passes[t.Name()]
		if !t.Failed() {
			p[0]++
		}
		p[1]++
		passes[t.Name()] = p
	})
}

// reportPassRates logs the table after the last -count run: one line per
// case, passes over runs and the 95% Wilson interval, which unlike p±2σ
// stays inside [0,1] and is honest at ten runs and a rate of 10/10.
func reportPassRates(t *testing.T) {
	passesMu.Lock()
	defer passesMu.Unlock()
	evalRuns++
	count := 1
	if f := flag.Lookup("test.count"); f != nil {
		if n, err := strconv.Atoi(f.Value.String()); err == nil && n > 0 {
			count = n
		}
	}
	if evalRuns < count {
		return
	}
	names := make([]string, 0, len(passes))
	for name := range passes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := passes[name]
		lo, hi := wilson(p[0], p[1])
		t.Logf("pass rate %-28s %2d/%-2d  95%% CI [%.2f, %.2f]", name, p[0], p[1], lo, hi)
	}
}

// wilson is the 95% Wilson score interval for k successes in n trials.
func wilson(k, n int) (float64, float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.96
	p, nf := float64(k)/float64(n), float64(n)
	denom := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / denom
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}

func checkRoute(t *testing.T, tc routeCase, d RouteDecision, idOf map[string]string) {
	t.Helper()
	if tc.Action != "" && string(d.Action) != tc.Action {
		t.Errorf("action = %s, want %s", d.Action, tc.Action)
	}
	if len(tc.ActionIn) > 0 && !containsString(tc.ActionIn, string(d.Action)) {
		t.Errorf("action = %s, want one of %v", d.Action, tc.ActionIn)
	}
	if tc.Dest != "" && d.NoteID != idOf[tc.Dest] {
		t.Errorf("note = %q, want the id of %q", d.NoteID, tc.Dest)
	}
	if tc.MinConfidence != nil && d.Confidence < *tc.MinConfidence {
		t.Errorf("confidence = %.2f, want at least %.2f", d.Confidence, *tc.MinConfidence)
	}
	// The Titles rule is about words, not case: "Groceries list" for the
	// spoken "groceries list" is the prompt's own example.
	if tc.Title != "" && !strings.EqualFold(d.Title, tc.Title) {
		t.Errorf("title = %q, want %q", d.Title, tc.Title)
	}
	// kind: the parser keeps it for a new note only, so an append passes
	// whatever the model said; the case's action assertion is what pins that.
	if tc.Kind != "" && d.Action == RouteNew && d.Checklist != (tc.Kind == "checklist") {
		t.Errorf("checklist = %v, want kind %s", d.Checklist, tc.Kind)
	}
	// title_names: the decision names that note, as a new note with its
	// title or as an append to it — the same outcome once
	// pipeline.preferExistingTitle has run.
	if tc.TitleNames != "" && !strings.EqualFold(fold(d.Title), fold(tc.TitleNames)) && d.NoteID != idOf[tc.TitleNames] {
		t.Errorf("title = %q, note = %q; want the decision to name %q", d.Title, d.NoteID, tc.TitleNames)
	}
	checkText(t, "title", d.Title, nil, tc.TitleExcludes)
	checkScript(t, "title", d.Title, tc.TitleScript)
	if tc.Content != nil && d.Content != *tc.Content {
		t.Errorf("content = %q, want %q", d.Content, *tc.Content)
	}
	checkText(t, "content", d.Content, tc.ContentContains, tc.ContentExcludes)
	if tc.ContentUnchanged && d.Content != tc.Transcript {
		t.Errorf("content = %q, want the transcript unchanged", d.Content)
	}
}

// checkText asserts case-insensitive presence and absence of phrases.
func checkText(t *testing.T, what, got string, contains, excludes []string) {
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

// checkScript asserts that every letter of got is in the named Unicode
// script ("Malayalam"); marks and digits are not letters and pass.
func checkScript(t *testing.T, what, got, script string) {
	t.Helper()
	if script == "" {
		return
	}
	for _, r := range got {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Scripts[script], r) {
			t.Errorf("%s has %q outside the %s script: %q", what, r, script, got)
			return
		}
	}
}

func checkCount(t *testing.T, what string, got int, want *int, wantIn []int) {
	t.Helper()
	if want != nil && got != *want {
		t.Errorf("%d %s, want %d", got, what, *want)
	}
	if len(wantIn) > 0 {
		ok := false
		for _, n := range wantIn {
			ok = ok || n == got
		}
		if !ok {
			t.Errorf("%d %s, want one of %v", got, what, wantIn)
		}
	}
}

// equalLines compares two lists line for line, case-insensitively when fold
// is set.
func equalLines(got, want []string, fold bool) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] && (!fold || !strings.EqualFold(got[i], want[i])) {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsAny(text string, phrases []string) bool {
	lower := strings.ToLower(text)
	for _, s := range phrases {
		if strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// fold is the comparison form of a title: one space between words.
func fold(s string) string { return strings.Join(strings.Fields(s), " ") }
