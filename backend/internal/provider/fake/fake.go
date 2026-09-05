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

	"github.com/vppillai/chintan/backend/internal/ask"
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
	// HangCalls makes that many leading calls block until their context is
	// done and answer with its error, the way a stalled provider looks to the
	// pipeline once the stage deadline fires.
	HangCalls int

	mu    sync.Mutex
	calls int
	// Sources records how each recording was handed over, so a test can assert
	// the adapter was given a URL rather than a buffer.
	Sources []provider.Audio
}

func (f *STT) Transcribe(ctx context.Context, in provider.Audio) (provider.Transcription, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls - 1
	f.Sources = append(f.Sources, in)
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if call < f.HangCalls {
		<-ctx.Done()
		return provider.Transcription{}, fmt.Errorf("fake stt: groq request: %w", ctx.Err())
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

	// NoteResponse, when set, is what CleanNote answers verbatim; otherwise
	// the fake derives a document from the body. NoteErr fails CleanNote alone,
	// so a test can have the per-capture cleanup succeed and the whole-note
	// pass fail.
	NoteResponse string
	NoteErr      error

	// Answer, when set, is what Ask answers verbatim; otherwise the fake cites
	// every packed note and answers from their titles. AskErrs are returned,
	// in order, by the leading Ask calls (a nil entry answers normally), so a
	// test can fail the first attempt and answer the second. AskHang makes
	// that many leading calls block until their context is done.
	Answer  *provider.Answer
	AskErrs []error
	AskHang int
	// HangCalls and NoteHang are AskHang for Cleanup and CleanNote: that many
	// leading calls block until their context is done.
	HangCalls int
	NoteHang  int

	mu    sync.Mutex
	calls int
	// noteCalls records every whole-note request: the mode and the body, so a
	// test can assert what the worker sent.
	noteCalls []NoteCall
	// askCalls records every ask prompt as the worker built it.
	askCalls []ask.Prompt
}

// Ask records the prompt and answers as configured.
func (f *LLM) Ask(ctx context.Context, q ask.Prompt) (provider.Answer, error) {
	f.mu.Lock()
	f.askCalls = append(f.askCalls, q)
	call := len(f.askCalls) - 1
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if call < f.AskHang {
		<-ctx.Done()
		return provider.Answer{}, fmt.Errorf("fake llm: ask request: %w", ctx.Err())
	}
	if call < len(f.AskErrs) && f.AskErrs[call] != nil {
		return provider.Answer{}, f.AskErrs[call]
	}
	if f.ShouldFail {
		return provider.Answer{}, fmt.Errorf("fake LLM failed")
	}
	// Priced from the prompt's size, the way a real provider would report it.
	words := len(strings.Fields(q.Question))
	for _, n := range q.Notes {
		words += len(strings.Fields(n.Text))
	}
	usage := provider.TokenUsage{InputTokens: words + 40, OutputTokens: 30}
	if f.Answer != nil {
		out := *f.Answer
		out.Usage = usage
		return out, nil
	}
	if len(q.Notes) == 0 {
		return provider.Answer{Text: "That is not in your notes.", Usage: usage}, nil
	}
	out := provider.Answer{Grounded: true, Usage: usage}
	titles := make([]string, 0, len(q.Notes))
	for _, n := range q.Notes {
		out.Sources = append(out.Sources, n.NoteID)
		titles = append(titles, n.Title)
	}
	out.Text = "From your notes: " + strings.Join(titles, ", ") + "."
	return out, nil
}

// AskCalls reports every ask prompt, in order.
func (f *LLM) AskCalls() []ask.Prompt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ask.Prompt(nil), f.askCalls...)
}

// NoteCall is one CleanNote request as the fake saw it.
type NoteCall struct {
	Mode model.NoteCleanMode
	Body string
}

func (f *LLM) CleanNote(ctx context.Context, mode model.NoteCleanMode, body string) (provider.Cleaned, error) {
	f.mu.Lock()
	f.noteCalls = append(f.noteCalls, NoteCall{Mode: mode, Body: body})
	call := len(f.noteCalls) - 1
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if call < f.NoteHang {
		<-ctx.Done()
		return provider.Cleaned{}, fmt.Errorf("fake llm: clean-note request: %w", ctx.Err())
	}
	if f.NoteErr != nil {
		return provider.Cleaned{}, f.NoteErr
	}
	if f.ShouldFail {
		return provider.Cleaned{}, fmt.Errorf("fake LLM failed")
	}
	usage := provider.TokenUsage{InputTokens: len(strings.Fields(body)), OutputTokens: len(strings.Fields(body)) + 2}
	if f.NoteResponse != "" {
		return provider.Cleaned{Text: f.NoteResponse, Usage: usage}, nil
	}
	if mode == model.NoteCleanPolished {
		return provider.Cleaned{Text: strings.Join(strings.Fields(body), " "), Usage: usage}, nil
	}
	return provider.Cleaned{Text: "# Cleaned\n\n" + body, Usage: usage}, nil
}

// NoteCalls reports every whole-note request, in order.
func (f *LLM) NoteCalls() []NoteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]NoteCall(nil), f.noteCalls...)
}

func (f *LLM) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (provider.Cleaned, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls - 1
	f.mu.Unlock()

	if f.OnCall != nil {
		f.OnCall()
	}
	if call < f.HangCalls {
		<-ctx.Done()
		return provider.Cleaned{}, fmt.Errorf("fake llm: cleanup request: %w", ctx.Err())
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
