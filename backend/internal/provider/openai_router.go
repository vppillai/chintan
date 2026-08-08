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

// maxTitleLen bounds a dictated note title.
const maxTitleLen = 120

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

	decision, err := parseRouteDecision(out)
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
	if !contentDerivedFrom(decision.Content, transcript) {
		// Counts only: note text does not belong in logs.
		log.Printf("provider: discarded router content not taken from the transcript (%d words returned, %d words dictated)",
			len(comparableWords(decision.Content)), len(comparableWords(transcript)))
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

// parseRouteDecision tolerates markdown fences and surrounding prose.
func parseRouteDecision(raw string) (RouteDecision, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return RouteDecision{}, err
	}

	var decision RouteDecision
	if err := json.Unmarshal([]byte(jsonText), &decision); err != nil {
		return RouteDecision{}, fmt.Errorf("provider: decode route decision: %w", err)
	}

	switch decision.Action {
	case RouteAppend:
		if strings.TrimSpace(decision.NoteID) == "" {
			return RouteDecision{}, fmt.Errorf("provider: router chose append without a note id")
		}
	case RouteNew:
	default:
		return RouteDecision{}, fmt.Errorf("provider: router returned unknown action %q", decision.Action)
	}

	if decision.Confidence < 0 {
		decision.Confidence = 0
	}
	if decision.Confidence > 1 {
		decision.Confidence = 1
	}
	decision.Content = strings.TrimSpace(decision.Content)
	decision.Title = sanitizeTitle(decision.Title)
	return decision, nil
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
