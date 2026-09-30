package pipeline

import (
	"context"
	"slices"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
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
