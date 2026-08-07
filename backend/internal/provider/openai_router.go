package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/routing"
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

	decision, err := parseRouteDecision(out)
	if err != nil {
		return RouteDecision{}, err
	}

	// The model may return an id that is not on the list; refuse to trust it.
	if decision.Action == RouteAppend && !containsNoteID(candidates, decision.NoteID) {
		return RouteDecision{}, fmt.Errorf("provider: router returned unknown note id")
	}
	if decision.Content == "" {
		decision.Content = transcript
	}
	return decision, nil
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
	decision.Title = strings.TrimSpace(decision.Title)
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
