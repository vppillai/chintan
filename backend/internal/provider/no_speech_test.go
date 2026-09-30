package provider

import (
	"strings"
	"testing"
)

// R7-10c: Whisper's per-segment silence measures survive decoding, and
// NoSpeech uses Whisper's own pair of thresholds, so a confidently heard
// short dictation is speech even when no_speech_prob is high.
func TestNoSpeechUsesWhispersSegmentMeasures(t *testing.T) {
	t.Parallel()

	got, err := decodeTranscription(strings.NewReader(`{"text":" .","duration":1.5,
		"segments":[{"start":0,"end":1.5,"text":" .","no_speech_prob":0.93,"avg_logprob":-1.7}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Segments[0]; s.NoSpeechProb != 0.93 || s.AvgLogprob != -1.7 {
		t.Fatalf("segment measures not decoded: %+v", s)
	}

	seg := func(noSpeech, logprob float64) []Segment {
		return []Segment{{Text: "x", NoSpeechProb: noSpeech, AvgLogprob: logprob}}
	}
	cases := []struct {
		name string
		t    Transcription
		want bool
	}{
		{"punctuation only", Transcription{Text: " . ,", Segments: seg(0.1, -0.2)}, true},
		{"empty", Transcription{Text: "  "}, true},
		{"silent segments", Transcription{Text: "Thank you.", Segments: seg(0.9, -1.2)}, true},
		{"buy milk", Transcription{Text: "Buy milk", Segments: seg(0.05, -0.3)}, false},
		{"quiet but confident", Transcription{Text: "Buy milk", Segments: seg(0.8, -0.5)}, false},
		{"one spoken segment among silent ones", Transcription{Text: "uh Buy milk", Segments: append(seg(0.9, -1.5), seg(0.1, -0.3)...)}, false},
		{"no segments reported", Transcription{Text: "Buy milk"}, false},
		{"digits are speech", Transcription{Text: "42"}, false},
		{"non-Latin script is speech", Transcription{Text: "പാൽ"}, false},
	}
	for _, tc := range cases {
		if got := tc.t.NoSpeech(); got != tc.want {
			t.Errorf("%s: NoSpeech() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
