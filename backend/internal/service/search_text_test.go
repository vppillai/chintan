package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/model"
)

func TestSearchTextLowercasesStripsMarkersAndCollapsesWhitespace(t *testing.T) {
	body := "# Roof\n\n" + CaptureMarker("c_1") + "\nThe   GUTTER is\tleaking again.\n\n" + CaptureMarker("c_2") + "\nCall the Roofer."
	got := SearchText(body)
	want := "# roof the gutter is leaking again. call the roofer."
	if got != want {
		t.Fatalf("SearchText = %q, want %q", got, want)
	}
	if strings.Contains(got, "chintan:capture") {
		t.Fatalf("a marker survived into the search text: %q", got)
	}
}

func TestSearchTextIsCappedOnARuneBoundary(t *testing.T) {
	// Three-byte runes, so a byte cap that ignored rune boundaries would land
	// mid-rune two times out of three.
	body := strings.Repeat("日", model.MaxSearchTextBytes)
	got := SearchText(body)
	if len(got) > model.MaxSearchTextBytes {
		t.Fatalf("len = %d, want <= %d", len(got), model.MaxSearchTextBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("the cap cut a rune in half")
	}
	if len(got) < model.MaxSearchTextBytes-utf8.UTFMax {
		t.Fatalf("len = %d; the cut backed off further than one rune", len(got))
	}
}

// ToLower can grow a string. The cap has to apply to what is stored, not to
// what was read.
func TestSearchTextCapAppliesAfterLowercasing(t *testing.T) {
	// U+023A folds from two bytes to three (U+2C65).
	body := strings.Repeat("Ⱥ", model.MaxSearchTextBytes/2)
	got := SearchText(body)
	if len(got) > model.MaxSearchTextBytes {
		t.Fatalf("len = %d after folding, want <= %d", len(got), model.MaxSearchTextBytes)
	}
}

func TestSearchTextOfAnEmptyBodyIsEmpty(t *testing.T) {
	if got := SearchText("  \n" + CaptureMarker("c_1") + "\n"); got != "" {
		t.Fatalf("SearchText = %q, want empty", got)
	}
}

// A body that spells a chillu the old way and a query that spells it the new
// way — or the other way round — meet on the folded form (review 2026-09-21,
// T58). SearchText folds what it stores and searchTerms folds what it looks
// for, so the two spellings match whichever side each is on.
func TestSearchTextAndQueryFoldChilluSpellings(t *testing.T) {
	old, atomic := "അവന്\u200d വന്നു", "അവൻ വന്നു"
	if SearchText(old) != SearchText(atomic) {
		t.Fatalf("SearchText differs by chillu spelling: %q vs %q", SearchText(old), SearchText(atomic))
	}
	// Both sides fold: a title, alias or tag stored in either spelling is
	// found by a query in either, including the old spelling typed the old
	// way, which matched before the fold and must not stop.
	for _, tc := range []struct{ stored, query string }{{old, atomic}, {atomic, old}, {old, old}, {atomic, atomic}} {
		terms := searchTerms(tc.query)
		if _, ok := matchNote(model.NoteIndex{Title: "Trip", SearchText: SearchText(tc.stored)}, terms); !ok {
			t.Errorf("query %q did not match a body spelled %q", tc.query, tc.stored)
		}
		for field, note := range map[string]model.NoteIndex{
			MatchTitle: {Title: tc.stored},
			MatchAlias: {Title: "Trip", Aliases: []string{tc.stored}},
			MatchTag:   {Title: "Trip", Tags: []string{tc.stored}},
			MatchBody:  {Title: "Trip", Snippet: tc.stored},
		} {
			hit, ok := matchNote(note, terms)
			if !ok || len(hit.MatchedIn) != 1 || hit.MatchedIn[0] != field {
				t.Errorf("query %q against a %s spelled %q: ok=%v matched_in=%v", tc.query, field, tc.stored, ok, hit.MatchedIn)
			}
		}
	}
}
