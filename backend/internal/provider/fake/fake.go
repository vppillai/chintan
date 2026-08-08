// Package fake holds test doubles for the provider interfaces.
//
// It is a separate package so it is never linked into the API binary: nothing
// under cmd/ imports it, and a guard test asserts that stays true.
package fake

import (
	"context"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// STT is a fake STT implementation for testing
type STT struct {
	ShouldFail bool
	Response   string
}

func (f *STT) Transcribe(ctx context.Context, audio []byte, contentType string) (text string, err error) {
	if f.ShouldFail {
		return "", fmt.Errorf("fake STT failed")
	}
	if f.Response != "" {
		return f.Response, nil
	}
	return fmt.Sprintf("transcribed audio: %d bytes of %s", len(audio), contentType), nil
}

// LLM is a fake LLM implementation for testing
type LLM struct {
	ShouldFail bool
	Response   string
}

func (f *LLM) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (cleaned string, err error) {
	if f.ShouldFail {
		return "", fmt.Errorf("fake LLM failed")
	}
	if f.Response != "" {
		return f.Response, nil
	}

	// Simple fake cleanup based on mode
	switch mode {
	case model.CleanupFaithful:
		return "[faithful] " + strings.ToLower(raw), nil
	case model.CleanupPolished:
		return "[polished] " + strings.Title(strings.ToLower(raw)), nil
	default:
		return raw, nil
	}
}

// Router is a fake Router implementation for testing.
type Router struct {
	ShouldFail bool
	Decision   provider.RouteDecision
	// NoContent means the recording held nothing but an app instruction, so an empty
	// Decision.Content is deliberate rather than a test that did not set it.
	NoContent bool
	// Calls records the transcripts the router was asked about.
	Calls []string
	// LastCandidates records the candidates from the most recent Route call.
	LastCandidates []routing.Candidate
}

func (f *Router) Route(ctx context.Context, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	f.Calls = append(f.Calls, transcript)
	f.LastCandidates = make([]routing.Candidate, len(candidates))
	copy(f.LastCandidates, candidates)

	if f.ShouldFail {
		return provider.RouteDecision{}, fmt.Errorf("fake router failed")
	}

	decision := f.Decision
	if decision.Action == "" {
		decision.Action = provider.RouteNew
		decision.Title = "Fake routed note"
		decision.Confidence = 1
	}
	if decision.Content == "" && !f.NoContent {
		decision.Content = transcript
	}
	return decision, nil
}
