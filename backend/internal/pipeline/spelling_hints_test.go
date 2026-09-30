package pipeline

import (
	"context"
	"slices"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// hintsSentTo runs the seeded capture at durationMS and returns the hints the
// fake STT was handed on its first call.
func hintsSentTo(t *testing.T, noteID string, durationMS int64) []string {
	t.Helper()
	h := newHarness(t, harnessOpts{llm: &fake.LLM{Response: "Cleaned."}})
	ctx := context.Background()
	if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID: "n_other", Title: "Chintan feedback", Aliases: []string{"app bugs"}, UpdatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	capture := seedUploadedCapture(t, h, noteID)
	if noteID != "" {
		note := mustGetNote(t, h.store, "user1", noteID)
		note.Aliases = []string{"dest"}
		if _, err := h.store.PutNote(ctx, "user1", note); err != nil {
			t.Fatalf("set aliases: %v", err)
		}
	}
	capture.DurationMS = durationMS
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("set duration: %v", err)
	}
	if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(h.stt.Sources) == 0 {
		t.Fatal("STT was not called")
	}
	return h.stt.Sources[0].Hints
}

// R7-10b: a capture recorded into a note is hinted with that note's names
// only; a routed one with the recent notes' titles and aliases; and a clip
// of 1.5 s or less gets none, since Whisper can echo its prompt on silence.
func TestSpellingHintsFollowTheTargetAndSkipShortAudio(t *testing.T) {
	if got := hintsSentTo(t, "note1", 12_000); !slices.Equal(got, []string{"Destination", "dest"}) {
		t.Errorf("targeted hints = %q, want the note's title and alias", got)
	}
	got := hintsSentTo(t, "", 12_000)
	if !slices.Contains(got, "Chintan feedback") || !slices.Contains(got, "app bugs") {
		t.Errorf("routed hints = %q, want the recent notes' titles and aliases", got)
	}
	for _, ms := range []int64{0, 1_000, minHintAudioMS} {
		if got := hintsSentTo(t, "", ms); len(got) != 0 {
			t.Errorf("hints for %d ms = %q, want none", ms, got)
		}
	}
}

// Whisper can read its prompt back on silence with confident log-probs, so
// the silence gate passes it; the echo of the hint names must still end as
// no_content with nothing routed, while dictation that merely names a note
// is routed as before.
func TestAnEchoOfTheSpellingPromptEndsAsNoContent(t *testing.T) {
	cases := []struct {
		name, text string
		want       model.CaptureStatus
	}{
		{"the whole prompt", "Chintan feedback, app bugs.", model.StatusNoContent},
		{"an echo cut off mid-name", "feedback, app", model.StatusNoContent},
		{"dictation naming a note", "Chintan feedback the app crashed on save", model.StatusAppended},
		{"a word of a title said alone", "Chintan", model.StatusAppended},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{
				stt: &fake.STT{Result: &provider.Transcription{
					Text: tc.text, Duration: 5,
					Segments: []provider.Segment{{End: 5, Text: tc.text, NoSpeechProb: 0.9, AvgLogprob: -0.3}},
				}},
				llm: &fake.LLM{Response: "Cleaned."},
			})
			ctx := context.Background()
			if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
				ID: "n_other", Title: "Chintan feedback", Aliases: []string{"app bugs"}, UpdatedAt: model.Now(),
			}); err != nil {
				t.Fatalf("seed note: %v", err)
			}
			capture := seedUploadedCapture(t, h, "")
			capture.DurationMS = 5_000
			if _, err := h.store.PutCapture(ctx, capture); err != nil {
				t.Fatalf("set duration: %v", err)
			}
			got, err := h.pipeline.Run(ctx, "user1", "c_1")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(h.stt.Sources) == 0 || len(h.stt.Sources[0].Hints) == 0 {
				t.Fatal("no hints were sent")
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.want == model.StatusNoContent {
				if h.router.LastCandidates != nil || len(h.creator.createdTitles()) > 0 {
					t.Errorf("an echoed prompt was routed or filed (notes created: %q)", h.creator.createdTitles())
				}
				if got.RawKey == "" {
					t.Error("the echoed transcript was not kept")
				}
			}
		})
	}
}
