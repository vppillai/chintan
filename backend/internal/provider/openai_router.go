package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/routing"
)

const (
	// maxTitleLen bounds a dictated note title.
	maxTitleLen = 120
	// maxInstructionOnlyWords is the longest transcript that is plausibly nothing but a
	// spoken app instruction. Past it, an empty content field looks like lost dictation.
	maxInstructionOnlyWords = 20
	// maxSpokenTitleWords is the longest title that still reads as a name rather than a
	// sentence the router mistook for one.
	maxSpokenTitleWords = 8
)

// Route asks the LLM which note the transcript belongs to.
func (c *OpenAICleanup) Route(ctx context.Context, transcript string, candidates []routing.Candidate) (RouteDecision, error) {
	userPrompt, err := routing.UserPrompt(transcript, candidates)
	if err != nil {
		return RouteDecision{}, err
	}

	out, err := c.complete(ctx, routing.SystemPrompt(), userPrompt)
	if err != nil {
		return RouteDecision{}, err
	}

	decision, contentGiven, err := parseRouteDecision(out)
	if err != nil {
		return RouteDecision{}, err
	}

	// The model may return an id that is not on the list; refuse to trust it.
	if decision.Action == RouteAppend && !containsNoteID(candidates, decision.NoteID) {
		return RouteDecision{}, fmt.Errorf("provider: router returned unknown note id")
	}
	// A transcript is untrusted, and honouring spoken instructions invites more of them.
	// The router is therefore allowed to delete words and nothing else: any content it
	// did not copy from the transcript is dropped, so no summary, translation, answer or
	// commentary can reach the note behind cleanup's back.
	dictated := len(comparableWords(transcript))
	switch {
	case !contentGiven:
		// No content field at all is a router that ignored the format, not an answer.
		log.Printf("provider: router returned no content field; keeping the transcript")
		decision.Content = transcript
	case strings.TrimSpace(decision.Content) == "":
		// A recording can be nothing but an instruction ("create a note called
		// test123"), which leaves no content. Believe that only while the transcript
		// is too short to have held dictation worth keeping, and while the title is
		// short enough to be a name: a title the length of a sentence means the router
		// swallowed the dictation into it instead of splitting the two.
		titleWords := len(comparableWords(decision.Title))
		if dictated > maxInstructionOnlyWords || titleWords > maxSpokenTitleWords {
			log.Printf("provider: router returned no content for %d dictated words with a %d word title; keeping the transcript",
				dictated, titleWords)
			decision.Content = transcript
		}
	case !contentDerivedFrom(decision.Content, transcript):
		// Counts only: note text does not belong in logs.
		log.Printf("provider: discarded router content not taken from the transcript (%d words returned, %d words dictated)",
			len(comparableWords(decision.Content)), dictated)
		decision.Content = transcript
	}
	return decision, nil
}

// contentDerivedFrom reports whether every word of content appears, in order, in
// transcript — that is, whether content is the transcript with words deleted.
func contentDerivedFrom(content, transcript string) bool {
	contentWords := comparableWords(content)
	if len(contentWords) == 0 {
		return false
	}

	i := 0
	for _, word := range comparableWords(transcript) {
		if word == contentWords[i] {
			if i++; i == len(contentWords) {
				return true
			}
		}
	}
	return false
}

// comparableWords reduces text to lowercase alphanumeric words, so that punctuation
// and casing differences do not count as rewriting.
func comparableWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// sanitizeTitle bounds a title to one line, since it comes from dictation and is
// later stored and rendered back into prompts.
func sanitizeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > maxTitleLen {
		title = strings.TrimSpace(string(runes[:maxTitleLen]))
	}
	return title
}

// parseRouteDecision tolerates markdown fences and surrounding prose. The second result
// reports whether the model supplied a content field at all, which distinguishes "there
// was nothing to write" from a reply that ignored the format.
func parseRouteDecision(raw string) (RouteDecision, bool, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return RouteDecision{}, false, err
	}

	var parsed struct {
		Action     RouteAction `json:"action"`
		NoteID     string      `json:"note_id"`
		Title      string      `json:"title"`
		Confidence float64     `json:"confidence"`
		Content    *string     `json:"content"`
	}
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return RouteDecision{}, false, fmt.Errorf("provider: decode route decision: %w", err)
	}

	decision := RouteDecision{
		Action:     parsed.Action,
		NoteID:     parsed.NoteID,
		Title:      parsed.Title,
		Confidence: parsed.Confidence,
	}
	if parsed.Content != nil {
		decision.Content = *parsed.Content
	}

	switch decision.Action {
	case RouteAppend:
		if strings.TrimSpace(decision.NoteID) == "" {
			return RouteDecision{}, false, fmt.Errorf("provider: router chose append without a note id")
		}
	case RouteNew:
	default:
		return RouteDecision{}, false, fmt.Errorf("provider: router returned unknown action %q", decision.Action)
	}

	if decision.Confidence < 0 {
		decision.Confidence = 0
	}
	if decision.Confidence > 1 {
		decision.Confidence = 1
	}
	decision.Content = strings.TrimSpace(decision.Content)
	decision.Title = sanitizeTitle(decision.Title)
	return decision, parsed.Content != nil, nil
}

// extractJSONObject returns the outermost {...} span, ignoring fences and prose.
func extractJSONObject(raw string) (string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("provider: router response contained no JSON object")
	}
	return raw[start : end+1], nil
}

func containsNoteID(candidates []routing.Candidate, noteID string) bool {
	for _, c := range candidates {
		if c.NoteID == noteID {
			return true
		}
	}
	return false
}
