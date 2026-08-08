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
may give the app instructions, for example:
- where to file it: "add this to my roof repair note"
- what to name a new note: "title this test123", "call this note dentist", "create a note titled Portugal trip"

Those instructions are addressed to the app; they are not part of the note content.

Choose one action:
- "append": the speaker clearly asked for this to go into one of the listed notes.
- "new": the speaker did not ask for a destination, or asked for a note that is not listed.

Reply with ONLY a JSON object. No markdown fence, no commentary. Either:
{"action":"append","note_id":"<exact id from the list>","confidence":<number 0-1>,"content":"<transcript without app instructions>"}
or:
{"action":"new","title":"<note title>","confidence":<number 0-1>,"content":"<transcript without app instructions>"}

Rules:
- note_id must be copied exactly from the list. Never invent an id.
- confidence is your certainty about the destination: 1.0 when the speaker named a listed note
  unambiguously, around 0.5 for a plausible guess, 0.0 when you are guessing blindly.
- Merely mentioning a topic that resembles a note title is not a request to append. When in
  doubt between append and new, choose new.
- Asking to title / name / call a note is NOT an append request. Even if a listed note has a
  similar title, choose "new" and use the spoken title unless the speaker also said to add or
  append to that existing note.
- "content" is the transcript with only app instructions removed (routing and title-setting).
  Keep every other word verbatim: do not summarise, reorder, translate, or fix grammar. If no
  app instruction was spoken, return the transcript unchanged.
- For "new" titles:
  - If the speaker named a title (e.g. "title this test123", "call it roof notes"), use that
    title exactly as spoken, including short or single-word titles.
  - Only invent a short descriptive title (a few words) when the speaker did not name one.
  - Never invent a title from the topic when a spoken title was given.`

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
