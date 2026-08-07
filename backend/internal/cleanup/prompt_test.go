package cleanup_test

import (
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
)

func TestSystemPromptDiffersByMode(t *testing.T) {
	faithful := cleanup.SystemPrompt(model.CleanupFaithful)
	polished := cleanup.SystemPrompt(model.CleanupPolished)

	if faithful == "" || polished == "" {
		t.Fatal("system prompts must not be empty")
	}
	if faithful == polished {
		t.Fatal("faithful and polished system prompts must differ")
	}
}

func TestSystemPromptFaithfulPreservesWording(t *testing.T) {
	prompt := strings.ToLower(cleanup.SystemPrompt(model.CleanupFaithful))

	if !strings.Contains(prompt, "preserve") {
		t.Fatal("faithful prompt must instruct preserving wording")
	}
	if !strings.Contains(prompt, "wording") && !strings.Contains(prompt, "phrasing") {
		t.Fatal("faithful prompt must mention wording or phrasing")
	}
}

func TestSystemPromptPolishedAllowsRephrase(t *testing.T) {
	prompt := strings.ToLower(cleanup.SystemPrompt(model.CleanupPolished))

	if !strings.Contains(prompt, "rephrase") {
		t.Fatal("polished prompt must allow rephrasing for clarity")
	}
	if !strings.Contains(prompt, "meaning") {
		t.Fatal("polished prompt must preserve meaning")
	}
	if !strings.Contains(prompt, "technical") {
		t.Fatal("polished prompt must preserve technical terms")
	}
}

func TestSystemPromptForbidsInventingFacts(t *testing.T) {
	for _, mode := range []model.CleanupMode{model.CleanupFaithful, model.CleanupPolished} {
		prompt := strings.ToLower(cleanup.SystemPrompt(mode))
		if !strings.Contains(prompt, "invent") {
			t.Fatalf("%q prompt must forbid inventing facts", mode)
		}
	}
}

func TestUserPromptRejectsEmptyRaw(t *testing.T) {
	_, err := cleanup.UserPrompt("")
	if err == nil {
		t.Fatal("expected error for empty raw input")
	}

	_, err = cleanup.UserPrompt("   \t\n")
	if err == nil {
		t.Fatal("expected error for whitespace-only raw input")
	}
}

func TestUserPromptReturnsRawTranscript(t *testing.T) {
	raw := "um so the kubernetes pod was crash looping"
	got, err := cleanup.UserPrompt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, raw) {
		t.Fatalf("user prompt must include raw transcript, got %q", got)
	}
}
