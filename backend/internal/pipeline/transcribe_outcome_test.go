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
	for name, tc := range map[string]struct {
		result provider.Transcription
		hints  []string
		want   transcriptVerdict
	}{
		"the prompt read back": {provider.Transcription{Text: "Chintan feedback, app bugs.", Segments: confident}, hints,
			transcriptVerdict{status: model.StatusNoContent, metric: "CaptureHintEcho", echoed: true}},
		"a tone": {provider.Transcription{Text: ".", Segments: silent}, hints,
			transcriptVerdict{status: model.StatusNoContent, metric: "CaptureNoSpeech"}},
		"the stock silence phrase, confident": {provider.Transcription{Text: "Thank you.", Segments: confident}, nil,
			transcriptVerdict{status: model.StatusNoContent, metric: "CaptureNoSpeech"}},
		"speech": {provider.Transcription{Text: "Buy milk", Segments: confident}, hints,
			transcriptVerdict{status: model.StatusTranscribed}},
		"part of a name said alone is speech": {provider.Transcription{Text: "App", Segments: confident}, hints,
			transcriptVerdict{status: model.StatusTranscribed}},
	} {
		if got := transcriptOutcome(tc.result, tc.hints); got != tc.want {
			t.Errorf("%s: transcriptOutcome = %+v, want %+v", name, got, tc.want)
		}
	}
}
