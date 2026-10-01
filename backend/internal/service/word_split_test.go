package service

import (
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/match"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// Every word splitter cuts the same words (R9 PR9-8). Until then seven rules
// coexisted: ask.Tokenize kept the emoji variation selector that llm.IsWordRune
// drops, the recording filename slug kept enclosing marks, the silence check
// dropped Indic vowel signs and match split on `[^a-z]+`, so a Malayalam
// title had no tokens at all. The inputs avoid what the callers legitimately
// add on top: stopwords and one-rune words (ask), apostrophes (routing) and
// the chillu fold (ask; owner decision D7 keeps it out of llm).
func TestEveryWordSplitterAgrees(t *testing.T) {
	for _, in := range []string{
		"Passport, milk. BREAD!",
		"പാൽ വാങ്ങണം; പുൽ.",
		"दाल और दिल",
		"✈️ Travel plans ❤️ Milk❤️",
		"Mixed CASE 2026 ✈️",
	} {
		t.Run(in, func(t *testing.T) {
			want := llm.Words(in)
			if len(want) == 0 {
				t.Fatalf("llm.Words(%q) is empty; the case tests nothing", in)
			}
			for name, got := range map[string][]string{
				"llm.FoldWords":           strings.Fields(llm.FoldWords(in)),
				"ask.Tokenize":            ask.Tokenize(in),
				"routing.NormalizeSpeech": strings.Fields(routing.NormalizeSpeech(in)),
				"filenameSlug":            strings.Split(filenameSlug(in), "-"),
			} {
				if strings.Join(got, " ") != strings.Join(want, " ") {
					t.Errorf("%s(%q) = %q, llm.Words = %q", name, in, got, want)
				}
			}
			// match's tokens are unexported; a title that differs from the
			// query only by punctuation has no substring in common and
			// ranks on token overlap alone, so its presence proves the split.
			ranked := match.Rank(in, []model.NoteIndex{{ID: "n1", Title: strings.Join(want, " · ")}}, 0)
			if len(ranked) != 1 {
				t.Errorf("match.Rank(%q) found %d notes by token overlap, want 1", in, len(ranked))
			}
		})
	}
}
