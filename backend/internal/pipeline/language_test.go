package pipeline

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
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

// The worker records what it asked for and what Whisper answered, so a
// Malayalam recording coming back as Tamil is visible in the log and countable
// in a metric rather than inferred from bytes per word (review 2026-09-21,
// T10). Whisper names the language ("tamil"), the setting is a code ("ta");
// the table in model joins them.
func TestTranscribedLanguageOutcomeComparesTheCodeSentWithTheNameDetected(t *testing.T) {
	cases := []struct{ sent, detected, want string }{
		{"ml", "malayalam", "match"},
		{"ml", "tamil", "mismatch"},
		{"", "tamil", "auto"},
		{"ml", "", "undetected"},
		{"ml", "klingon", "unknown"},
	}
	for _, tc := range cases {
		if got := languageOutcome(tc.sent, tc.detected); got != tc.want {
			t.Errorf("languageOutcome(%q, %q) = %q, want %q", tc.sent, tc.detected, got, tc.want)
		}
	}

	h := newHarness(t, harnessOpts{
		stt: &fake.STT{Result: &provider.Transcription{Text: "words", Language: "tamil", Duration: 3}},
		llm: &fake.LLM{Response: "Words."},
	})
	seedUploadedCapture(t, h, "note1")
	note := mustGetNote(t, h.store, "user1", "note1")
	note.Language = "ml"
	if _, err := h.store.PutNote(context.Background(), "user1", note); err != nil {
		t.Fatalf("set note language: %v", err)
	}
	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	_, err := h.pipeline.Run(context.Background(), "user1", "c_1")
	restore()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outcome := ""
	for _, rec := range decodeMetrics(t, metrics.Bytes()) {
		if slices.Contains(rec.Names, "TranscribedLanguage") {
			outcome = dimension(rec, "Outcome")
		}
	}
	if outcome != "mismatch" {
		t.Fatalf("TranscribedLanguage Outcome = %q, want mismatch for ml sent and tamil detected", outcome)
	}
}
