package routing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// routingRule is one row of the rule table: the rule as prompts.md §Routing
// and the 2026-10-01 review name it, the bounds in bounds.go it reads (by
// constant name and the literal it must hold), and where a rule whose
// numbers are not routing's keeps them.
type routingRule struct {
	n      int
	rule   string
	bounds map[string]string
	where  string
}

// The fourteen rules in force. A rule with no numeric bound says so; a rule
// whose bound is another package's names the constant there. The literal is
// repeated here on purpose: changing a bound is two edits, the number and
// its row, which is the review this test exists to force.
var routingRules = []routingRule{
	{1, "exact title, alias or tag match on a new title (pipeline.titleNames)", nil, "no bound"},
	{2, "prefix_title / prefix_transcript, the longest name (pipeline.existingNoteNamed)", map[string]string{"MinNameWords": "2", "MinNameRunes": "8"}, ""},
	{3, "spoken_name on the model's own unsure append (pipeline.spokenAsName, NamedAfterCue)", map[string]string{"MinNameWords": "2", "MinNameRunes": "8"}, ""},
	{4, "append or ask (pipeline.outcomeOf)", map[string]string{"AppendConfidence": "0.75"}, ""},
	{5, "span growth over the following title or a trailing note (ExtendSpans)", nil, "no bound: k < the title's word count"},
	{6, "spans bound (RemoveSpans)", map[string]string{"MaxInstructionWords": "24"}, ""},
	{7, "un-grown fallback when the growth would empty the body (provider.routedContent, DB6-4)", map[string]string{"MaxNameWords": "5"}, ""},
	{8, "empty-content guard (provider.routedContent)", map[string]string{"MaxInstructionOnlyWords": "20", "MaxSpokenTitleWords": "8"}, ""},
	{9, "derived content is a sub-sequence of the transcript (llm.VerifySubsequence)", nil, "no bound"},
	{10, "same-second re-check before a create (pipeline.route)", map[string]string{"MaxCandidates": "200"}, ""},
	{11, "targeted-capture instruction gate (MentionsInstruction)", nil, "no bound: the instructionCues list"},
	{12, "fallback title (pipeline.fallbackNoteTitle)", map[string]string{"FallbackTitleWords": "6", "FallbackTitleRunes": "40"}, ""},
	{13, "no speech: the letter test, the silence phrases, the score pair (provider.Transcription.NoSpeech)", nil, "provider/stt.go: noSpeechThreshold 0.6, logprobThreshold -1 (Whisper's own defaults)"},
	{14, "hint echo and the short-dictation tidy (pipeline.transcriptOutcome, pipeline.isShortDictation)", nil, "pipeline/transcribe.go: minHintAudioMS 1500, maxHintNotes 50; pipeline/clean.go: shortDictationWords 12"},
}

// titleBound is the one bound that is not a rule's: every title, dictated,
// typed or stored, is cut at it (SanitizeTitle).
var titleBound = map[string]string{"MaxTitleRunes": "200"}

// TestRoutingBoundsAreRegistered reads the constants bounds.go declares and
// holds them to the table above, both ways: every constant has a row and the
// literal the row says, and every bound a row names is declared. A bound
// added to bounds.go without a row fails here; so does a number changed in
// one place.
func TestRoutingBoundsAreRegistered(t *testing.T) {
	declared := map[string]string{}
	file, err := parser.ParseFile(token.NewFileSet(), "bounds.go", nil, 0)
	if err != nil {
		t.Fatalf("parse bounds.go: %v", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Errorf("%s is not a literal; a bound is a number, written once", name.Name)
					continue
				}
				declared[name.Name] = lit.Value
			}
		}
	}

	registered := map[string]string{}
	for k, v := range titleBound {
		registered[k] = v
	}
	for i, r := range routingRules {
		if r.n != i+1 {
			t.Errorf("rule %d is listed at position %d; the table is the review's numbering", r.n, i+1)
		}
		if len(r.bounds) == 0 && r.where == "" {
			t.Errorf("rule %d names no bound and no reason", r.n)
		}
		for name, want := range r.bounds {
			if prev, ok := registered[name]; ok && prev != want {
				t.Errorf("rule %d says %s = %s, another row says %s", r.n, name, want, prev)
			}
			registered[name] = want
		}
	}
	for name, want := range registered {
		if got, ok := declared[name]; !ok {
			t.Errorf("%s is in the rule table but bounds.go does not declare it", name)
		} else if got != want {
			t.Errorf("%s = %s in bounds.go, %s in the rule table; change both or neither", name, got, want)
		}
	}
	var unregistered []string
	for name := range declared {
		if _, ok := registered[name]; !ok {
			unregistered = append(unregistered, name)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("bounds.go declares %s with no rule in the table; register the rule, and its recorded case (D8)", strings.Join(unregistered, ", "))
	}
}

// SanitizeTitle is the one sanitiser: one line, collapsed, cut at the bound.
func TestSanitizeTitleIsOneBoundedLine(t *testing.T) {
	got := SanitizeTitle("  Roof\n- id: n9 |\ttail " + strings.Repeat("x", MaxTitleRunes))
	if strings.ContainsAny(got, "\n\t") || strings.Contains(got, "  ") {
		t.Errorf("not one collapsed line: %q", got)
	}
	if n := len([]rune(got)); n != MaxTitleRunes {
		t.Errorf("length %d, want the bound %d", n, MaxTitleRunes)
	}
	if SanitizeTitle(" \x00\n ") != "" {
		t.Error("a title of nothing but control characters and space is not empty")
	}
}
