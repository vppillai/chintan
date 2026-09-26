package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
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
// passes. LLM_BASE_URL and LLM_MODEL default to the worker's. The key is read
// from the environment and never printed; the output is the fixture text and
// the model's reply to it, one line per case, and nothing else. Adding a case
// is appending an object to the fixtures file; TestEvalFixturesParse, which
// always runs, refuses a misspelt key so a typo fails CI without a key.
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
	fx := loadEvalFixtures(t)
	ctx := context.Background()

	t.Run("route", func(t *testing.T) {
		candidates, idOf := fx.candidates()
		for i, tc := range fx.Route.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				// tc.Language is what the routing prompt would name once it
				// takes a language (PR-2); Route does not yet.
				d, err := c.Route(ctx, tc.Transcript, candidates)
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Transcript, err)
				}
				t.Logf("%s | %s %s %q conf=%.2f | %q", tc.Transcript, d.Action, d.NoteID, d.Title, d.Confidence, d.Content)
				checkRoute(t, tc, d, idOf)
			})
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		for i, tc := range fx.Cleanup.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				out, err := c.Cleanup(ctx, model.CleanupMode(tc.Mode), tc.Raw, tc.Language)
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
				out, err := c.Items(ctx, tc.Transcript, fx.Items.ListTitle, tc.Language)
				if err != nil {
					t.Fatalf("%s | ERROR %v", tc.Transcript, err)
				}
				joined := strings.Join(out.Items, " · ")
				t.Logf("%s | %s", tc.Transcript, joined)
				if tc.Want != nil && !equalLines(out.Items, tc.Want, false) {
					t.Errorf("items = %s, want %s", joined, strings.Join(tc.Want, " · "))
				}
				checkCount(t, "items", len(out.Items), tc.Count, tc.CountIn)
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
				out, err := c.CleanNote(ctx, model.NoteCleanTasks, tc.Body, "")
				if err != nil {
					t.Fatalf("%q | ERROR %v", tc.Body, err)
				}
				text, dropped, err := cleanup.NoteOutput(model.NoteCleanTasks, out.Text, tc.Body)
				t.Logf("%q | %q dropped=%d", tc.Body, out.Text, dropped)
				if err != nil {
					t.Fatalf("NoteOutput refused the reply: %v", err)
				}
				lines := strings.Split(text, "\n")
				// Casing of a split item is the model's; the words are asserted.
				if tc.Want != nil && !equalLines(lines, tc.Want, true) {
					t.Errorf("tasks = %q, want %q", lines, tc.Want)
				}
				if tc.WantUnchanged && text != strings.TrimSpace(tc.Body) {
					t.Errorf("tasks = %q, want the body unchanged", text)
				}
				checkCount(t, "tasks", len(lines), tc.Count, nil)
			})
		}
	})
	t.Run("ask", func(t *testing.T) {
		notes, idOf := fx.packedNotes()
		for i, tc := range fx.Ask.Cases {
			t.Run(caseName(i), func(t *testing.T) {
				a, err := c.Ask(ctx, ask.Prompt{Today: time.Now().UTC().Format("2006-01-02"), Notes: notes, Question: tc.Question})
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
	if tc.Title != "" && d.Title != tc.Title {
		t.Errorf("title = %q, want %q", d.Title, tc.Title)
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
