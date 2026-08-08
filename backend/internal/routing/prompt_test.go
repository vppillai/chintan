package routing

import (
	"strings"
	"testing"
)

func TestSystemPromptHonorsSpokenTitles(t *testing.T) {
	t.Parallel()
	p := SystemPrompt()
	for _, want := range []string{
		"title this test123",
		"title exactly as spoken",
		"Asking to title / name / call a note is NOT an append request",
		"Never invent a title from the topic when a spoken title was given",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if strings.Contains(p, "3-8 words") {
		t.Error("system prompt still forces 3-8 word invented titles")
	}
}

func TestUserPromptIncludesCandidatesAndTranscript(t *testing.T) {
	t.Parallel()
	got, err := UserPrompt("title this test123 hello", []Candidate{
		{NoteID: "n1", Title: "Roof", Aliases: []string{"roof"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"n1", "Roof", "roof", "title this test123 hello", "Existing notes:", "Transcript:"} {
		if !strings.Contains(got, want) {
			t.Errorf("user prompt missing %q\n%s", want, got)
		}
	}
}
