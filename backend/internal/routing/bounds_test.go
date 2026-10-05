package routing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// routingRule is one row of the rule table: the rule as routing.md §The
// bounds and the 2026-10-01 review name it, the bounds in bounds.go it reads (by
// constant name and the literal it must hold), and where a rule whose
// numbers are not routing's keeps them.
type routingRule struct {
	n      int
	rule   string
	bounds map[string]string
	where  string
}

// The sixteen rules in force. A rule with no numeric bound says so. The literal is
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
	{13, "nothing heard: the recorder's peak before the call, then the letter test, the silence phrases and the logprob (pipeline.transcribe, provider.Transcription.NoSpeech)", map[string]string{"QuietPeakRMS": "0.04", "LogprobThreshold": "-1.0"}, ""},
	{14, "hint echo and the short-dictation tidy (pipeline.spellingHints, pipeline.transcriptOutcome, pipeline.isShortDictation)", map[string]string{"MinHintAudioMS": "1500", "MaxHintNotes": "50", "ShortDictationWords": "12"}, ""},
	{15, "the cleaned words share (cleanup.WordShare; pipeline.clean, pipeline.CleanNote)", map[string]string{"MinCleanedWordShare": "0.5"}, ""},
	{16, "the respelled-item rescue in the invented-item drop (cleanup.DropUnspoken; pipeline.extractItems)", map[string]string{"RespellMaxEdits": "2", "RespellMinRunes": "4"}, ""},
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
				lit, ok := numericLiteral(vs.Values[i])
				if !ok {
					t.Errorf("%s is not a literal; a bound is a number, written once", name.Name)
					continue
				}
				declared[name.Name] = lit
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

// numericLiteral is the source text of a number, a leading minus included.
func numericLiteral(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.INT || v.Kind == token.FLOAT {
			return v.Value, true
		}
	case *ast.UnaryExpr:
		if lit, ok := v.X.(*ast.BasicLit); ok && v.Op == token.SUB && (lit.Kind == token.INT || lit.Kind == token.FLOAT) {
			return "-" + lit.Value, true
		}
	}
	return "", false
}

// ruleFiles are where the routing rules are written; a new rule lands in
// one of them. budgetConstants are the numbers those files may still
// declare: estimates and caps for the model call, not bounds a rule
// decides by.
var (
	ruleFiles       = []string{"spans.go", "../pipeline/route.go", "../provider/openai_router.go"}
	budgetConstants = map[string]bool{"routeOutputTokensEstimate": true, "routeMaxTokens": true}
)

// TestRuleFilesHoldNoUnregisteredLiterals is the other direction of the pin:
// the table holds bounds.go, and this holds the rule files to bounds.go. A
// numeric constant declared in one of them, or a comparison against a number
// other than zero or one (an index check, a clamp), is a bound that bypassed
// the table. The budget constants are the allowlist.
func TestRuleFilesHoldNoUnregisteredLiterals(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range ruleFiles {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) || budgetConstants[name.Name] {
						continue
					}
					if lit, ok := numericLiteral(vs.Values[i]); ok {
						t.Errorf("%s declares %s = %s; a rule's number lives in bounds.go with its row", path, name.Name, lit)
					}
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			switch bin.Op {
			case token.LSS, token.GTR, token.LEQ, token.GEQ, token.EQL, token.NEQ:
			default:
				return true
			}
			for _, side := range []ast.Expr{bin.X, bin.Y} {
				if lit, ok := numericLiteral(side); ok && lit != "0" && lit != "1" {
					t.Errorf("%s compares against %s at %s; a rule's number lives in bounds.go with its row", path, lit, fset.Position(bin.Pos()))
				}
			}
			return true
		})
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
