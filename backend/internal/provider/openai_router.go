package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/routing"
)

const (
	// maxTitleLen bounds a dictated note title.
	maxTitleLen = 120
	// maxInstructionOnlyWords is the longest transcript that is plausibly nothing but a
	// spoken app instruction. Past it, spans that cover every word look like lost dictation.
	maxInstructionOnlyWords = 20
	// maxSpokenTitleWords is the longest title that still reads as a name rather than a
	// sentence the router mistook for one.
	maxSpokenTitleWords = 8
	// routeMaxTokens caps the routing completion. A well-formed reply is an action, an
	// id or a short title, a confidence and a span or two — under fifty tokens — so the
	// cap never shortens a real answer; it bounds a runaway one, which then fails to
	// parse and takes the pipeline's fallback instead of billing a page of output.
	routeMaxTokens = 200
)

// Route asks the LLM which note the transcript belongs to.
//
// The model returns the destination and the word positions of any spoken app
// instruction; the note content is derived here by deleting those positions
// (routing.RemoveSpans). The model never writes the note back, so the reply is a
// few dozen tokens whatever the recording's length, and it cannot smuggle in a
// summary, translation, answer or commentary: whatever reaches cleanup is the
// transcript with words deleted, by construction. Spans that do not fit the
// transcript are ignored and every word is kept — a stray instruction word in
// the note is trivial to fix, dictation left out of it is lost.
func (c *OpenAICleanup) Route(ctx context.Context, transcript string, candidates []routing.Candidate) (RouteDecision, error) {
	userPrompt, err := routing.UserPrompt(transcript, candidates)
	if err != nil {
		return RouteDecision{}, err
	}

	out, usage, err := c.complete(ctx, routing.SystemPrompt(), userPrompt, routeMaxTokens)
	if err != nil {
		return RouteDecision{}, err
	}

	decision, reply, err := parseRouteDecision(out)
	if err != nil {
		return RouteDecision{}, err
	}
	decision.Usage = usage

	// The model may return an id that is not on the list; refuse to trust it.
	if decision.Action == RouteAppend && !containsNoteID(candidates, decision.NoteID) {
		return RouteDecision{}, fmt.Errorf("provider: router returned unknown note id")
	}
	decision.Content = routedContent(ctx, transcript, decision.Title, reply)
	return decision, nil
}

// routeReply is the part of the router's answer that says which words to drop.
// Pointers distinguish a field the model left out from one it set to empty.
type routeReply struct {
	Spans *[]routing.Span
	// Content is the pre-span contract, honoured only when a model still
	// answers with it and only when it is verbatim. It costs nothing to accept
	// and keeps a model that ignores the new format from filing the instruction
	// into the note.
	Content *string
}

// routedContent derives the note content from the transcript and the router's
// spans. Every failure keeps the whole transcript; nothing here can lose a word
// the speaker said. Logs carry counts only: note text does not belong in logs.
func routedContent(ctx context.Context, transcript, title string, reply routeReply) string {
	discard := func(reason, msg string, attrs ...any) string {
		obs.Log(ctx).Warn(msg, attrs...)
		obs.Count(ctx, "RouterSpansDiscarded", map[string]string{"Reason": reason})
		return transcript
	}
	dictated := len(routing.Words(transcript))

	if reply.Spans == nil {
		if reply.Content != nil && llm.VerifySubsequence(*reply.Content, transcript) && strings.TrimSpace(*reply.Content) != "" {
			obs.Count(ctx, "RouterLegacyContent", map[string]string{"Outcome": "accepted"})
			return strings.TrimSpace(*reply.Content)
		}
		// No spans at all is a router that ignored the format, not an answer.
		legacyField := reply.Content != nil
		return discard("missing_field", "router returned no instruction_spans; keeping the dictation",
			slog.Bool("legacy_content_field", legacyField))
	}

	content, err := routing.RemoveSpans(transcript, *reply.Spans)
	switch {
	case errors.Is(err, routing.ErrSpansTooLong):
		return discard("too_long", "router spans would remove more words than an instruction holds; keeping the dictation",
			slog.Int("dictated_words", dictated), slog.Int("spans", len(*reply.Spans)))
	case err != nil:
		return discard("malformed", "router spans do not fit the transcript; keeping the dictation",
			slog.Int("dictated_words", dictated), slog.Int("spans", len(*reply.Spans)))
	}

	if strings.TrimSpace(content) == "" {
		// A recording can be nothing but an instruction ("create a note called
		// test123"), which leaves no content. Believe that only while the transcript
		// is too short to have held dictation worth keeping, and while the title is
		// short enough to be a name: a title the length of a sentence means the router
		// swallowed the dictation into it instead of splitting the two.
		titleWords := len(routing.Words(title))
		if dictated > maxInstructionOnlyWords || titleWords > maxSpokenTitleWords {
			return discard("empty_content", "router spans cover a recording too long to be instruction-only; keeping the dictation",
				slog.Int("dictated_words", dictated), slog.Int("title_words", titleWords))
		}
		return ""
	}

	// Holds by construction; checked anyway because it is the property the note
	// depends on, and a bug in the derivation must keep the dictation, not lose it.
	if !llm.VerifySubsequence(content, transcript) {
		return discard("not_derived", "derived content is not the transcript with words deleted; keeping the dictation",
			slog.Int("dictated_words", dictated))
	}
	return content
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

// parseRouteDecision tolerates markdown fences and surrounding prose. The reply
// carries the span list (or the legacy content field) as the model gave it, so
// routedContent can tell "nothing to remove" from "ignored the format".
func parseRouteDecision(raw string) (RouteDecision, routeReply, error) {
	jsonText, err := llm.ExtractJSONObject(raw)
	if err != nil {
		return RouteDecision{}, routeReply{}, fmt.Errorf("provider: router %w", err)
	}

	var parsed struct {
		Action     RouteAction `json:"action"`
		NoteID     string      `json:"note_id"`
		Title      string      `json:"title"`
		Confidence float64     `json:"confidence"`
		// Numbers rather than ints: a model that writes 7.0 has still answered.
		Spans *[]struct {
			StartWord *float64 `json:"start_word"`
			EndWord   *float64 `json:"end_word"`
		} `json:"instruction_spans"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return RouteDecision{}, routeReply{}, fmt.Errorf("provider: decode route decision: %w", err)
	}

	decision := RouteDecision{
		Action:     parsed.Action,
		NoteID:     parsed.NoteID,
		Title:      parsed.Title,
		Confidence: parsed.Confidence,
	}
	switch decision.Action {
	case RouteAppend:
		if strings.TrimSpace(decision.NoteID) == "" {
			return RouteDecision{}, routeReply{}, fmt.Errorf("provider: router chose append without a note id")
		}
	case RouteNew:
	default:
		return RouteDecision{}, routeReply{}, fmt.Errorf("provider: router returned unknown action %q", decision.Action)
	}

	if decision.Confidence < 0 {
		decision.Confidence = 0
	}
	if decision.Confidence > 1 {
		decision.Confidence = 1
	}
	decision.Title = sanitizeTitle(decision.Title)

	reply := routeReply{Content: parsed.Content}
	if parsed.Spans != nil {
		spans := make([]routing.Span, 0, len(*parsed.Spans))
		for _, s := range *parsed.Spans {
			start, okStart := wordIndex(s.StartWord)
			end, okEnd := wordIndex(s.EndWord)
			if !okStart || !okEnd {
				// A span with a missing or fractional index cannot be applied.
				// Mark the whole set unusable so routedContent keeps every word.
				spans = append(spans, routing.Span{StartWord: -1, EndWord: -1})
				continue
			}
			spans = append(spans, routing.Span{StartWord: start, EndWord: end})
		}
		reply.Spans = &spans
	}
	return decision, reply, nil
}

// wordIndex accepts a JSON number as a word position only when it is a whole number.
func wordIndex(v *float64) (int, bool) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v != math.Trunc(*v) || math.Abs(*v) > math.MaxInt32 {
		return 0, false
	}
	return int(*v), true
}

func containsNoteID(candidates []routing.Candidate, noteID string) bool {
	for _, c := range candidates {
		if c.NoteID == noteID {
			return true
		}
	}
	return false
}
