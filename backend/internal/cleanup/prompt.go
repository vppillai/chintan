package cleanup

import (
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
)

const (
	faithfulSystemPrompt = `You clean up speech-to-text transcripts for personal notes.

Mode: faithful.
- Fix STT garbling, punctuation, and obvious grammar mistakes.
- Preserve the speaker's wording, phrasing, and vocabulary as much as possible.
- Do not invent facts, details, names, numbers, or events that are not in the transcript.
- Return only the cleaned transcript with no preamble or commentary.`

	polishedSystemPrompt = `You clean up speech-to-text transcripts for personal notes.

Mode: polished.
- Make the text read like clean written notes.
- You may rephrase for clarity when needed, but preserve meaning and technical terms.
- Do not invent facts, details, names, numbers, or events that are not in the transcript.
- Return only the cleaned transcript with no preamble or commentary.`
)

func SystemPrompt(mode model.CleanupMode) string {
	switch mode {
	case model.CleanupPolished:
		return polishedSystemPrompt
	default:
		return faithfulSystemPrompt
	}
}

func UserPrompt(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("cleanup: raw transcript is required")
	}
	return "Clean up the following speech-to-text transcript:\n\n" + raw, nil
}
