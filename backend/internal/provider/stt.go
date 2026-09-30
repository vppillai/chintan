package provider

import (
	"context"
	"io"
	"strings"
	"unicode"
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
	// segment is silence and in the words it chose; see NoSpeech. Zero when
	// the provider does not report them, which reads as speech.
	NoSpeechProb float64 `json:"no_speech_prob,omitempty"`
	AvgLogprob   float64 `json:"avg_logprob,omitempty"`
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

// Whisper's own silence thresholds, the defaults of its reference
// transcribe(): a segment is skipped as silence when no_speech_prob is over
// 0.6 unless avg_logprob is over -1, i.e. the model is also unsure of the
// words. Using its tuned pair rather than a number of ours means a quiet
// but confidently heard "Buy milk" is kept.
const (
	noSpeechThreshold = 0.6
	logprobThreshold  = -1.0
)

// NoSpeech reports that the recording held no dictation (R7-10c): the
// transcript has no letter or digit in it (a 1.5 s tone came back as "."
// and became a note called "Dictation"), or every segment is one Whisper
// itself would have skipped as silence. A transcript without segments is
// judged by its text alone.
func (t Transcription) NoSpeech() bool {
	if strings.IndexFunc(t.Text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) < 0 {
		return true
	}
	if len(t.Segments) == 0 {
		return false
	}
	for _, s := range t.Segments {
		if s.NoSpeechProb <= noSpeechThreshold || s.AvgLogprob > logprobThreshold {
			return false
		}
	}
	return true
}

// STT transcribes speech.
type STT interface {
	Transcribe(ctx context.Context, in Audio) (Transcription, error)
}
