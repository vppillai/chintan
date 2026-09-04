// Package fake holds test doubles for the provider interfaces.
//
// It is a separate package so it is never linked into the API or worker binary:
// nothing under cmd/ imports it, and a guard test asserts that stays true.
package fake

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// STT is a fake STT implementation for testing.
type STT struct {
	ShouldFail bool
	// Err, when set, is returned instead of the generic ShouldFail error. It is
	// how a test hands the pipeline a typed provider.StatusError, which is what
	// tells a revoked key from a throttle.
	Err      error
	Response string
	// Result, when set, is returned verbatim and Response is ignored. Use it to
	// exercise segments, words, and duration.
	Result *provider.Transcription
	// Duration is the reported audio length in seconds when Result is nil. It is
	// what the breaker prices a transcription from.
	Duration float64
	// OnCall runs before the call returns, so a test can advance a clock or block.
	OnCall func()

	mu    sync.Mutex
	calls int
	// Sources records how each recording was handed over, so a test can assert
	// the adapter was given a URL rather than a buffer.
	Sources []provider.Audio
}

func (f *STT) Transcribe(ctx context.Context, in provider.Audio) (provider.Transcription, error) {
	f.mu.Lock()
	f.calls++
	f.Sources = append(f.Sources, in)
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if f.Err != nil {
		return provider.Transcription{}, f.Err
	}
	if f.ShouldFail {
		return provider.Transcription{}, fmt.Errorf("fake STT failed")
	}
	if f.Result != nil {
		return *f.Result, nil
	}

	text := f.Response
	if text == "" {
		read := int64(0)
		if in.Body != nil {
			n, _ := io.Copy(io.Discard, in.Body)
			read = n
		}
		text = fmt.Sprintf("transcribed audio: %d bytes of %s", read, in.ContentType)
	}
	duration := f.Duration
	if duration <= 0 {
		duration = 1
	}
	return provider.Transcription{
		Text:     text,
		Duration: duration,
		Segments: []provider.Segment{{Start: 0, End: duration, Text: text}},
	}, nil
}

// Calls reports how many transcriptions were requested.
func (f *STT) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// LLM is a fake LLM implementation for testing.
type LLM struct {
	ShouldFail bool
	// Err, when set, is returned instead of the generic ShouldFail error. See
	// STT.Err.
	Err      error
	Response string
	OnCall   func()

	mu    sync.Mutex
	calls int
}

func (f *LLM) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (provider.Cleaned, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if f.Err != nil {
		return provider.Cleaned{}, f.Err
	}
	if f.ShouldFail {
		return provider.Cleaned{}, fmt.Errorf("fake LLM failed")
	}

	usage := provider.TokenUsage{InputTokens: len(strings.Fields(raw)), OutputTokens: 4}
	if f.Response != "" {
		return provider.Cleaned{Text: f.Response, Usage: usage}, nil
	}

	// Simple fake cleanup based on mode
	switch mode {
	case model.CleanupFaithful:
		return provider.Cleaned{Text: "[faithful] " + strings.ToLower(raw), Usage: usage}, nil
	case model.CleanupPolished:
		polished := strings.ToLower(raw)
		if polished != "" {
			polished = strings.ToUpper(polished[:1]) + polished[1:]
		}
		return provider.Cleaned{Text: "[polished] " + polished, Usage: usage}, nil
	default:
		return provider.Cleaned{Text: raw, Usage: usage}, nil
	}
}

// Calls reports how many cleanups were requested.
func (f *LLM) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Router is a fake Router implementation for testing.
type Router struct {
	ShouldFail bool
	Decision   provider.RouteDecision
	// NoContent means the recording held nothing but an app instruction, so an empty
	// Decision.Content is deliberate rather than a test that did not set it.
	NoContent bool
	// Spans, when set and Decision.Content is empty, derives the content the way the
	// real adapter does: by deleting these word ranges from the transcript. Spans
	// that do not fit keep the whole transcript, as the adapter does.
	Spans  []routing.Span
	OnCall func()
	// HangCalls is how many leading calls block until their context is done and
	// then return its error, the way a provider that has queued the request
	// looks from this side.
	HangCalls int
	// ErrCalls are returned, in order, by the leading calls; a nil entry answers
	// normally. It is how a test hands the pipeline a typed provider.StatusError
	// on the first attempt and a decision on the second.
	ErrCalls []error

	mu sync.Mutex
	// Calls records the transcripts the router was asked about.
	Calls []string
	// LastCandidates records the candidates from the most recent Route call.
	LastCandidates []routing.Candidate
}

func (f *Router) Route(ctx context.Context, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, transcript)
	call := len(f.Calls) - 1
	f.LastCandidates = make([]routing.Candidate, len(candidates))
	copy(f.LastCandidates, candidates)
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if call < f.HangCalls {
		<-ctx.Done()
		return provider.RouteDecision{}, fmt.Errorf("fake router: llm request: %w", ctx.Err())
	}
	if call < len(f.ErrCalls) && f.ErrCalls[call] != nil {
		return provider.RouteDecision{}, f.ErrCalls[call]
	}
	if f.ShouldFail {
		return provider.RouteDecision{}, fmt.Errorf("fake router failed")
	}

	decision := f.Decision
	if decision.Action == "" {
		decision.Action = provider.RouteNew
		decision.Title = "Fake routed note"
		decision.Confidence = 1
	}
	if decision.Content == "" && f.Spans != nil {
		content, err := routing.RemoveSpans(transcript, f.Spans)
		if err != nil {
			content = transcript
		}
		decision.Content = content
	} else if decision.Content == "" && !f.NoContent {
		decision.Content = transcript
	}
	decision.Usage = provider.TokenUsage{InputTokens: len(strings.Fields(transcript)), OutputTokens: 8}
	return decision, nil
}

// CallCount reports how many routing decisions were requested.
func (f *Router) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
