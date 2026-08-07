// Package routing builds the prompt that decides which note a dictated capture belongs to.
package routing

import (
	"fmt"
	"strings"
)

// Candidate is an existing note the transcript could be routed to.
type Candidate struct {
	NoteID  string
	Title   string
	Aliases []string
}

const systemPrompt = `You route a dictated note to its destination.

You receive a list of the user's existing notes and a speech-to-text transcript. The speaker
may say where the note should go, for example "add this to my roof repair note" or "this is a
continuation of the Portugal trip note". That instruction is addressed to the app; it is not
part of the note content.

Choose one action:
- "append": the speaker clearly asked for this to go into one of the listed notes.
- "new": the speaker did not ask for a destination, or asked for a note that is not listed.

Reply with ONLY a JSON object. No markdown fence, no commentary. Either:
{"action":"append","note_id":"<exact id from the list>","confidence":<number 0-1>,"content":"<transcript without the routing instruction>"}
or:
{"action":"new","title":"<short descriptive title, 3-8 words>","confidence":<number 0-1>,"content":"<transcript without the routing instruction>"}

Rules:
- note_id must be copied exactly from the list. Never invent an id.
- confidence is your certainty about the destination: 1.0 when the speaker named a listed note
  unambiguously, around 0.5 for a plausible guess, 0.0 when you are guessing blindly.
- Merely mentioning a topic that resembles a note title is not a request to append. When in
  doubt between append and new, choose new.
- "content" is the transcript with only the routing instruction removed. Keep every other word
  verbatim: do not summarise, reorder, translate, or fix grammar. If no routing instruction was
  spoken, return the transcript unchanged.
- For "new", write a title that describes what the note is about. Do not just copy the opening
  words of the transcript.`

// SystemPrompt returns the routing system prompt.
func SystemPrompt() string {
	return systemPrompt
}

// UserPrompt renders the candidate notes and transcript for the router.
func UserPrompt(transcript string, candidates []Candidate) (string, error) {
	if strings.TrimSpace(transcript) == "" {
		return "", fmt.Errorf("routing: transcript is required")
	}

	var b strings.Builder
	b.WriteString("Existing notes:\n")
	if len(candidates) == 0 {
		b.WriteString("(none)\n")
	}
	for _, c := range candidates {
		fmt.Fprintf(&b, "- id: %s | title: %s", c.NoteID, c.Title)
		if len(c.Aliases) > 0 {
			fmt.Fprintf(&b, " | aliases: %s", strings.Join(c.Aliases, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nTranscript:\n")
	b.WriteString(transcript)
	return b.String(), nil
}
