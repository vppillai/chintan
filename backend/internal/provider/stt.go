package provider

import (
	"context"
	"io"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// Audio is one recording handed to a transcription provider.
//
// It is deliberately not a []byte. Reading the whole object into the Lambda
// heap and re-POSTing it makes the 512MB heap — not the microphone — the real
// cap on recording length. An adapter is expected to stream: either from URL,
// or from Body, and never to hold the whole recording at once.
type Audio struct {
	// URL is a short-lived presigned GET for the stored object. Preferred: the
	// adapter fetches and forwards it without buffering.
	URL string
	// Body is an already-open stream, used when there is no URL. The caller
	// closes it.
	Body io.Reader
	// ContentType names the container so the provider gets a plausible filename.
	ContentType string
	// SizeBytes is the object's length when known, 0 otherwise. It is metadata,
	// not an allocation hint.
	SizeBytes int64
	// Language is the ISO-639-1 code the speech is in, or "" to let the
	// provider detect it. Whisper is faster and more accurate when told; left
	// to guess on a short clip it can answer in the wrong script entirely.
	Language string
	// Hints are names the speech may contain — note titles and aliases, the
	// likeliest first — sent to Whisper as a spelling prompt, so "Chintan"
	// is not heard as "chin tan" and the router can match the note. The
	// adapter keeps as many as fit the provider's prompt limit. The caller
	// leaves it empty for very short audio (see pipeline.spellingHints).
	Hints []string
}

// Segment is one timestamped span of the raw transcript.
//
// Start and End are seconds from the beginning of the recording, as the
// provider reports them.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// NoSpeechProb and AvgLogprob are Whisper's own confidence that the
	// segment is silence and in the words it chose, and CompressionRatio is
	// how repetitive the text is (a looping hallucination runs past 2.4).
	// Zero when the provider does not report them, which reads as speech;
	// NoSpeech reads AvgLogprob alone, since Groq reports no_speech_prob as
	// 0 on every segment, and the other two are logged for the next tuning.
	NoSpeechProb     float64 `json:"no_speech_prob,omitempty"`
	AvgLogprob       float64 `json:"avg_logprob,omitempty"`
	CompressionRatio float64 `json:"compression_ratio,omitempty"`
}

// Word is one timestamped word of the raw transcript.
type Word struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Word  string  `json:"word"`
}

// Transcription is what a provider returned for one recording.
//
// Duration is what makes the spend estimate for a transcription honest: audio
// seconds are the billable unit, and nothing else in the pipeline knows how
// long the recording was.
type Transcription struct {
	Text     string
	Language string
	Duration float64 // seconds
	Segments []Segment
	Words    []Word
}

// DurationMS is Duration rounded to whole milliseconds.
func (t Transcription) DurationMS() int64 {
	if t.Duration <= 0 {
		return 0
	}
	return int64(t.Duration*1000 + 0.5)
}

// NoSpeech reports that the recording held no dictation (R7-10c): the
// transcript has no letter or digit in it (a 1.5 s tone came back as "."
// and became a note called "Dictation"), or it is only Whisper's stock
// answer to silence (see silenceHallucination), or the model was unsure of
// every word of every segment — avg_logprob at or under
// routing.LogprobThreshold — which is noise heard as words: in production
// three noise recordings scored -1.35, -2.08 and -2.56 and each became a
// note, while speech never went under -0.88. Whisper's no_speech_prob is
// not consulted; this provider reports it as 0 for every segment. A
// transcript without segments is judged by its text alone.
func (t Transcription) NoSpeech() bool {
	if !HasLetterOrDigit(t.Text) {
		return true
	}
	// Whatever the scores (R7-10e): on prod, 3 s of digital silence came
	// back as "Thank you." with no_speech_prob 0 and avg_logprob -0.29 on
	// every segment, so no score can tell it from the words said. A
	// recording that is only "Thank you." has nothing to file either way.
	if silenceHallucination(t.Text) {
		return true
	}
	if len(t.Segments) == 0 {
		return false
	}
	for _, s := range t.Segments {
		if s.AvgLogprob > routing.LogprobThreshold {
			return false
		}
	}
	return true
}

// HasLetterOrDigit reports that text holds something that can be filed: a
// letter or a digit in any script. Punctuation alone ("." for a tone) is not
// dictation; the pipeline's override reads it too.
func HasLetterOrDigit(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// silenceHallucinations are the transcripts Whisper is known to produce for
// silence or room tone, learned from subtitled video, as llm.FoldWords
// spells them. Exact phrases only: "Thank you, Anu" or "bye to the old car" is
// speech. ponytail: English only; add a language's stock phrases when a
// silent capture in it is seen filing one.
var silenceHallucinations = map[string]bool{
	"thank you":                            true,
	"thank you so much":                    true,
	"thank you very much":                  true,
	"thank you for watching":               true,
	"thanks for watching":                  true,
	"you":                                  true,
	"bye":                                  true,
	"bye bye":                              true,
	"subtitles by the amara org community": true,
}

// silenceHallucination reports that every sentence of text is a stock
// silence phrase, so "Thank you. Thank you." counts and "Thank you. Buy
// milk." does not. The whole text is tried first, since a credit like
// "Amara.org" has a full stop inside it. The phrases are compared by words
// (llm.FoldWords), so Whisper's punctuation and capitals do not matter.
func silenceHallucination(text string) bool {
	if silenceHallucinations[llm.FoldWords(text)] {
		return true
	}
	pieces := 0
	for _, piece := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(".!?\n", r) }) {
		norm := llm.FoldWords(piece)
		if norm == "" {
			continue
		}
		if !silenceHallucinations[norm] {
			return false
		}
		pieces++
	}
	return pieces > 0
}

// STT transcribes speech.
type STT interface {
	Transcribe(ctx context.Context, in Audio) (Transcription, error)
}
