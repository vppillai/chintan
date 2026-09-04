package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// languageSentTo runs one capture and reports the language the fake STT was
// handed. noteLanguage is set on the destination note when withNote is true;
// defaultLanguage is written to the tenant's settings when non-empty.
func languageSentTo(t *testing.T, withNote bool, noteLanguage, defaultLanguage string) string {
	t.Helper()
	h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Cleaned."}})
	ctx := context.Background()

	noteID := ""
	if withNote {
		noteID = "note1"
	}
	seedUploadedCapture(t, h, noteID)
	if withNote && noteLanguage != "" {
		note := mustGetNote(t, h.store, "user1", noteID)
		note.Language = noteLanguage
		if _, err := h.store.PutNote(ctx, "user1", note); err != nil {
			t.Fatalf("set note language: %v", err)
		}
	}
	if defaultLanguage != "" {
		if err := h.store.PutSettings(ctx, "user1", model.Settings{DefaultLanguage: defaultLanguage}); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
	}

	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(h.stt.Sources) != 1 {
		t.Fatalf("stt calls = %d, want 1", len(h.stt.Sources))
	}
	return h.stt.Sources[0].Language
}

// The target note's language wins when the capture was started with one; the
// tenant's default applies otherwise — including to every capture that is
// routed afterwards, since routing reads the transcript and so runs after
// transcription. "auto" is sent as no language at all, and a tenant who never
// chose transcribes in English.
func TestTranscriptionLanguageComesFromTheTargetNoteThenTheTenantDefault(t *testing.T) {
	cases := []struct {
		name          string
		withNote      bool
		noteLanguage  string
		defaultLang   string
		wantSentAsLng string
	}{
		{"note language wins over the default", true, "ta", "hi", "ta"},
		{"note without a language falls back to the default", true, "", "hi", "hi"},
		{"no note (routed later) uses the default", false, "", "hi", "hi"},
		{"nothing chosen anywhere is English", false, "", "", "en"},
		{"auto on the note sends no language", true, "auto", "hi", ""},
		{"auto as the default sends no language", false, "", "auto", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := languageSentTo(t, tc.withNote, tc.noteLanguage, tc.defaultLang); got != tc.wantSentAsLng {
				t.Fatalf("language sent to the provider = %q, want %q", got, tc.wantSentAsLng)
			}
		})
	}
}
