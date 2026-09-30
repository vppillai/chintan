package model_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/model"
)

func TestCaptureExcerpt(t *testing.T) {
	if got := model.CaptureExcerpt("  call the\n plumber  "); got != "call the plumber" {
		t.Errorf("short text = %q, want the whitespace collapsed and nothing cut", got)
	}
	if got := model.CaptureExcerpt(""); got != "" {
		t.Errorf("empty text = %q, want empty", got)
	}

	long := strings.Repeat("roof tiles ", 20)
	got := model.CaptureExcerpt(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long text = %q, want an ellipsis", got)
	}
	body := strings.TrimSuffix(got, "…")
	if utf8.RuneCountInString(body) > model.ExcerptRunes {
		t.Errorf("excerpt is %d runes, want at most %d", utf8.RuneCountInString(body), model.ExcerptRunes)
	}
	if !strings.HasSuffix(body, "roof") && !strings.HasSuffix(body, "tiles") {
		t.Errorf("excerpt %q ends mid-word or on a space", got)
	}

	// Cut on runes, not bytes: Malayalam is three bytes a character.
	mal := strings.Repeat("മ", 200)
	if got := model.CaptureExcerpt(mal); !utf8.ValidString(got) || utf8.RuneCountInString(got) != model.ExcerptRunes+1 {
		t.Errorf("one long word = %d runes (valid %v), want %d cut where it stands plus the ellipsis",
			utf8.RuneCountInString(got), utf8.ValidString(got), model.ExcerptRunes+1)
	}
}
