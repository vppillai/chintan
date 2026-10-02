package pipeline

import (
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
)

// The three gates of the transcription stage, read as one decision: the hint
// echo first (it has confident scores, so the silence gate would pass it),
// then no speech, then a transcript that goes on. The echo keeps nothing
// but the text; no speech keeps the segments too.
func TestTranscriptOutcomeOrdersTheThreeGates(t *testing.T) {
	hints := []string{"Chintan feedback", "App bugs"}
	confident := []provider.Segment{{End: 2, Text: "x", NoSpeechProb: 0.01, AvgLogprob: -0.2}}
	silent := []provider.Segment{{End: 2, Text: ".", NoSpeechProb: 0.9, AvgLogprob: -1.4}}
	unsure := []provider.Segment{{End: 5.4, Text: "the end", NoSpeechProb: 0, AvgLogprob: -2.56}}
	for name, tc := range map[string]struct {
		result    provider.Transcription
		hints     []string
		skipGates bool
		want      transcriptVerdict
	}{
		// The scores the production noise filings carried; and the person's
		// Transcribe again over the gate, which files the words unless there
		// are none, and never files the prompt read back.
		"noise the model was unsure of": {provider.Transcription{Text: "the end", Segments: unsure}, nil, false,
			transcriptVerdict{status: model.StatusNoContent, gate: "no_speech"}},
		"the same, asked for again": {provider.Transcription{Text: "the end", Segments: unsure}, nil, true,
			transcriptVerdict{status: model.StatusTranscribed}},
		"a tone asked for again": {provider.Transcription{Text: ".", Segments: silent}, nil, true,
			transcriptVerdict{status: model.StatusNoContent, gate: "no_speech"}},
		"the prompt read back, asked for again": {provider.Transcription{Text: "Chintan feedback, app bugs.", Segments: confident}, hints, true,
			transcriptVerdict{status: model.StatusNoContent, gate: "hint_echo", echoed: true}},
		"the prompt read back": {provider.Transcription{Text: "Chintan feedback, app bugs.", Segments: confident}, hints, false,
			transcriptVerdict{status: model.StatusNoContent, gate: "hint_echo", echoed: true}},
		"a tone": {provider.Transcription{Text: ".", Segments: silent}, hints, false,
			transcriptVerdict{status: model.StatusNoContent, gate: "no_speech"}},
		"the stock silence phrase, confident": {provider.Transcription{Text: "Thank you.", Segments: confident}, nil, false,
			transcriptVerdict{status: model.StatusNoContent, gate: "no_speech"}},
		"speech": {provider.Transcription{Text: "Buy milk", Segments: confident}, hints, false,
			transcriptVerdict{status: model.StatusTranscribed}},
		"part of a name said alone is speech": {provider.Transcription{Text: "App", Segments: confident}, hints, false,
			transcriptVerdict{status: model.StatusTranscribed}},
	} {
		if got := transcriptOutcome(tc.result, tc.hints, tc.skipGates); got != tc.want {
			t.Errorf("%s: transcriptOutcome = %+v, want %+v", name, got, tc.want)
		}
	}
}
