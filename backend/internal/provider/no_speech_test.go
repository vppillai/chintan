package provider

import (
	"strings"
	"testing"
)

// R7-10c: Whisper's per-segment measures survive decoding, and NoSpeech
// reads avg_logprob alone against Whisper's own bound, so a confidently
// heard short dictation is speech whatever no_speech_prob says — and noise
// the model was unsure of is not, even at no_speech_prob 0, which is what
// Groq reports on every segment (PR12-20: 45 of 45 scored production captures).
func TestNoSpeechUsesWhispersSegmentMeasures(t *testing.T) {
	t.Parallel()

	got, err := decodeTranscription(strings.NewReader(`{"text":" .","duration":1.5,
		"segments":[{"start":0,"end":1.5,"text":" .","no_speech_prob":0.93,"avg_logprob":-1.7,"compression_ratio":0.4}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Segments[0]; s.NoSpeechProb != 0.93 || s.AvgLogprob != -1.7 || s.CompressionRatio != 0.4 {
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
		// The three production noise filings, as Groq scored them: one word
		// at -2.08, two at -2.56, thirteen in 1.6 s at -1.35, no_speech_prob 0.
		{"a fan heard as a word", Transcription{Text: "Olá", Segments: seg(0, -2.080058)}, true},
		{"room tone heard as two words", Transcription{Text: "the end", Segments: seg(0, -2.5619473)}, true},
		{"noise heard as a run of words", Transcription{Text: "a run of words that nobody said in under two seconds", Segments: seg(0, -1.3539717)}, true},
		{"the bound itself is unsure", Transcription{Text: "Buy milk", Segments: seg(0, -1.0)}, true},
		// The least confident scored English dictation in prod was -0.604;
		// the -0.88 line was a stock silence phrase the phrase list caught,
		// which the scores alone would have kept.
		{"the least confident real dictation seen in prod", Transcription{Text: "Buy milk", Segments: seg(0, -0.604)}, false},
		{"a silence phrase at -0.88 is kept by the scores, caught by the phrase list", Transcription{Text: "Thank you.", Segments: seg(0, -0.8806)}, true},
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

// R7-10d/e: three seconds of digital silence came back as "Thank you." and
// filed a note. On prod Whisper scored it no_speech_prob 0 and avg_logprob
// -0.29, so a transcript that is only a stock silence phrase is no speech
// whatever the scores; any other short dictation is kept.
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
		{"prod silence scores", one(" Thank you.", 0, -0.29), true},
		{"silence heard as thank you", one(" Thank you.", 0.7, -0.2), true},
		{"a thank you really said", one("Thank you.", 0.05, -0.2), true},
		{"thanks for watching", one("Thanks for watching!", 0.45, -0.3), true},
		{"lone you", one(" you", 0.5, -0.6), true},
		{"subtitle credit", one("Subtitles by the Amara.org community", 0.4, -0.3), true},
		{"repeated", Transcription{Text: "Thank you. Thank you.", Segments: []Segment{seg("Thank you.", 0, -0.2), seg("Thank you.", 0, -0.2)}}, true},
		{"no segments", Transcription{Text: "Thank you."}, true},
		{"buy milk", one("Buy milk", 0.05, -0.3), false},
		{"quiet buy milk", one("Buy milk", 0.7, -0.2), false},
		{"thank you to someone", one("Thank you, Anu.", 0, -0.2), false},
		{"thanks then a task", one("Thank you. Buy milk.", 0, -0.2), false},
	}
	for _, tc := range cases {
		if got := tc.t.NoSpeech(); got != tc.want {
			t.Errorf("%s: NoSpeech() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
