package pipeline

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
)

// R7-10c: a 1.5 s tone came back from Whisper as "." and was routed into a
// new note called "Dictation". A recording with no speech now ends as
// no_content before routing; a short real dictation still reaches a note.
func TestARecordingWithNoSpeechEndsAsNoContent(t *testing.T) {
	cases := []struct {
		name   string
		result provider.Transcription
		want   model.CaptureStatus
	}{
		{"a tone transcribed as punctuation", provider.Transcription{
			Text: ".", Duration: 1.5,
			Segments: []provider.Segment{{End: 1.5, Text: ".", NoSpeechProb: 0.2, AvgLogprob: -0.4}},
		}, model.StatusNoContent},
		{"every segment silent by Whisper's own measure", provider.Transcription{
			Text: "Thank you.", Duration: 2,
			Segments: []provider.Segment{{End: 2, Text: "Thank you.", NoSpeechProb: 0.9, AvgLogprob: -1.4}},
		}, model.StatusNoContent},
		{"a short dictation", provider.Transcription{
			Text: "Buy milk", Duration: 1.2,
			Segments: []provider.Segment{{End: 1.2, Text: "Buy milk", NoSpeechProb: 0.05, AvgLogprob: -0.3}},
		}, model.StatusAppended},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			h := newHarness(t, harnessOpts{stt: &fake.STT{Result: &result}, llm: &fake.LLM{Response: "Buy milk."}})
			seedUploadedCapture(t, h, "")
			got, err := h.pipeline.Run(context.Background(), "user1", "c_1")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			routed := h.router.LastCandidates != nil || len(h.creator.createdTitles()) > 0
			if tc.want == model.StatusNoContent && routed {
				t.Errorf("a recording with no speech was routed or filed (notes created: %q)", h.creator.createdTitles())
			}
			if tc.want == model.StatusNoContent && got.RawKey == "" {
				t.Error("the transcript of a no-speech recording was not kept")
			}
		})
	}
}
