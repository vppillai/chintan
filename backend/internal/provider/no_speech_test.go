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

// R7-10d: three seconds of digital silence came back as "Thank you." with a
// confident logprob and filed a note 3 of 3. A stock silence phrase over
// hallucinationNoSpeech is no speech whatever the logprob; the same words
// clearly spoken, and any other short dictation, are kept.
func TestNoSpeechCatchesWhispersSilenceHallucinations(t *testing.T) {
	t.Parallel()

	seg := func(text string, noSpeech, logprob float64) Segment {
		return Segment{Start: 0, End: 3, Text: text, NoSpeechProb: noSpeech, AvgLogprob: logprob}
	}
	one := func(text string, noSpeech, logprob float64) Transcription {
		return Transcription{Text: text, Duration: 3, Segments: []Segment{seg(text, noSpeech, logprob)}}
	}
	cases := []struct {
		name string
		t    Transcription
		want bool
	}{
		{"silence heard as thank you", one(" Thank you.", 0.7, -0.2), true},
		{"just over the line", one("Thank you.", 0.31, -0.1), true},
		{"thanks for watching", one("Thanks for watching!", 0.45, -0.3), true},
		{"lone you", one(" you", 0.5, -0.6), true},
		{"subtitle credit", one("Subtitles by the Amara.org community", 0.4, -0.3), true},
		{"repeated", Transcription{Text: "Thank you. Thank you.", Segments: []Segment{seg("Thank you.", 0.6, -0.2), seg("Thank you.", 0.5, -0.2)}}, true},
		{"a thank you really said", one("Thank you.", 0.05, -0.2), false},
		{"one segment spoken", Transcription{Text: "Thank you. Thank you.", Segments: []Segment{seg("Thank you.", 0.6, -0.2), seg("Thank you.", 0.1, -0.2)}}, false},
		{"buy milk", one("Buy milk", 0.05, -0.3), false},
		{"quiet buy milk", one("Buy milk", 0.7, -0.2), false},
		{"thank you to someone", one("Thank you, Anu.", 0.7, -0.2), false},
		{"thanks then a task", one("Thank you. Buy milk.", 0.7, -0.2), false},
		{"no segments", Transcription{Text: "Thank you."}, false},
	}
	for _, tc := range cases {
		if got := tc.t.NoSpeech(); got != tc.want {
			t.Errorf("%s: NoSpeech() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
