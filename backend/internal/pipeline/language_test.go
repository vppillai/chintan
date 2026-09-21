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
// tenant's default applies otherwise — including to the first transcription
// of every capture that is routed afterwards, since routing reads the
// transcript and so runs after transcription (the second, in the destination's
// language, is the test below). "auto" is sent as no language at all, and a
// tenant who never chose transcribes in English.
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

// routedTo files one transcript into note n1 by the router's decision, with
// the tenant default and n1's language set as given, and returns the capture
// and the fixture.
func routedTo(t *testing.T, defaultLanguage, noteLanguage string) (model.CaptureIndex, *routingFixture) {
	t.Helper()
	f := newRoutingFixture(t, "the gutter is also leaking",
		provider.RouteDecision{Action: provider.RouteAppend, NoteID: "n1", Confidence: 0.95, Content: "the gutter is also leaking"}, false)
	ctx := context.Background()
	if err := f.store.PutSettings(ctx, f.userID, model.Settings{DefaultLanguage: defaultLanguage}); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	note := mustGetNote(t, f.store, f.userID, "n1")
	note.Language = noteLanguage
	if _, err := f.store.PutNote(ctx, f.userID, note); err != nil {
		t.Fatalf("set note language: %v", err)
	}
	capture, err := f.run(ctx, "c_1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if capture.Status != model.StatusAppended {
		t.Fatalf("status = %s (%s), want appended", capture.Status, capture.Error)
	}
	return capture, f
}

// A recording made from Home is transcribed in the tenant's default before the
// router picks its note, so the note's own language was never applied — the
// owner's Malayalam dictation aimed by voice at an ml note went to Whisper as
// auto (review 2026-09-21, T2). Once the router lands on a note that asks for
// a language other than the one sent, the worker transcribes once more in it,
// and the second transcript is what is cleaned and appended. The language
// used is written on the capture, so a repeat delivery does not transcribe a
// third time.
func TestARoutedCaptureIsTranscribedAgainInTheDestinationNotesLanguage(t *testing.T) {
	capture, f := routedTo(t, model.LanguageAuto, "ml")

	sent := make([]string, 0, len(f.h.stt.Sources))
	for _, s := range f.h.stt.Sources {
		sent = append(sent, s.Language)
	}
	if len(sent) != 2 || sent[0] != "" || sent[1] != "ml" {
		t.Fatalf("languages sent to the provider = %q, want auto (none) and then ml", sent)
	}
	if capture.Language != "ml" {
		t.Errorf("capture.Language = %q, want the language of the transcript that was kept", capture.Language)
	}
	if capture.RawKey == "" || capture.CleanKey == "" {
		t.Errorf("the second transcript was not stored: raw=%q clean=%q", capture.RawKey, capture.CleanKey)
	}
	if n := f.router.CallCount(); n != 1 {
		t.Errorf("router calls = %d, want one: the destination is decided, not re-routed", n)
	}
	if wantsNoteLanguage(capture, mustGetNote(t, f.store, f.userID, "n1")) {
		t.Error("the finished capture still asks for another transcription; a retry would loop")
	}
}

// Only a real difference costs a second call: a note that inherits the
// default, asks for auto, or asks for the code already sent is transcribed
// once.
func TestARoutedCaptureIsNotTranscribedAgainWhenTheNoteAsksForWhatWasSent(t *testing.T) {
	cases := []struct{ name, defaultLanguage, noteLanguage string }{
		{"note asks for the code that was sent", "ml", "ml"},
		{"note inherits the default", "ml", ""},
		{"note asks for auto", "en", model.LanguageAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, f := routedTo(t, tc.defaultLanguage, tc.noteLanguage)
			if n := len(f.h.stt.Sources); n != 1 {
				t.Fatalf("stt calls = %d, want 1", n)
			}
		})
	}
}

// A note the router creates used to have no language at all, so a later
// "Record into this" followed whatever the default had become. It now starts
// in the language its first recording was transcribed in, unless that was
// auto-detection, which is not a language to pin a note to.
func TestARouterCreatedNoteStartsInTheLanguageItsRecordingWasTranscribedIn(t *testing.T) {
	for _, tc := range []struct{ defaultLanguage, wantNoteLanguage string }{
		{"hi", "hi"},
		{model.LanguageAuto, ""},
	} {
		t.Run("default "+tc.defaultLanguage, func(t *testing.T) {
			f := newRoutingFixture(t, "remind me to book the dentist",
				provider.RouteDecision{Action: provider.RouteNew, Title: "Dentist", Confidence: 1, Content: "remind me to book the dentist"}, false)
			ctx := context.Background()
			if err := f.store.PutSettings(ctx, f.userID, model.Settings{DefaultLanguage: tc.defaultLanguage}); err != nil {
				t.Fatalf("PutSettings: %v", err)
			}
			capture, err := f.run(ctx, "c_1")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if capture.Status != model.StatusAppended {
				t.Fatalf("status = %s (%s)", capture.Status, capture.Error)
			}
			if got := mustGetNote(t, f.store, f.userID, capture.NoteID).Language; got != tc.wantNoteLanguage {
				t.Errorf("new note language = %q, want %q", got, tc.wantNoteLanguage)
			}
			if n := len(f.h.stt.Sources); n != 1 {
				t.Errorf("stt calls = %d, want 1: the new note asks for what was sent", n)
			}
		})
	}
}
