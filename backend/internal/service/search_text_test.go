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
