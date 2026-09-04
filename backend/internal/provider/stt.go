package provider

import (
	"context"
	"io"
)

// Audio is one recording handed to a transcription provider.
//
// It is deliberately not a []byte. v1 read the whole object into the Lambda
// heap and re-POSTed it, which made the 512MB heap — not the microphone — the
// real cap on recording length. An adapter is expected to stream: either from
// URL, or from Body, and never to hold the whole recording at once.
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
}

// Segment is one timestamped span of the raw transcript.
//
// Start and End are seconds from the beginning of the recording, as the
// provider reports them.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
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

// STT transcribes speech.
type STT interface {
	Transcribe(ctx context.Context, in Audio) (Transcription, error)
}
