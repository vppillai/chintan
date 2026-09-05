package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
)

const (
	defaultLLMBaseURL = "https://api.minimax.io/v1"
	defaultLLMModel   = "MiniMax-M3"
)

// OpenAICleanup implements LLM via an OpenAI-compatible chat completions API
// (MiniMax M3 by default).
type OpenAICleanup struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOpenAICleanup builds a cleanup client. Empty baseURL/model use MiniMax defaults.
func NewOpenAICleanup(apiKey, baseURL, model string, httpClient *http.Client) (*OpenAICleanup, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("provider: llm api key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultLLMBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = defaultLLMModel
	}
	if httpClient == nil {
		// Below the worker's 900s Lambda timeout, so a hung provider surfaces as
		// an error the pipeline can record rather than as a killed invocation.
		httpClient = &http.Client{Timeout: 840 * time.Second}
	}
	return &OpenAICleanup{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: httpClient,
	}, nil
}

// Model reports the model this client completes with, so a caller can price the
// call without keeping a second copy of the name.
func (c *OpenAICleanup) Model() string { return c.model }

func (c *OpenAICleanup) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (Cleaned, error) {
	userPrompt, err := cleanup.UserPrompt(raw)
	if err != nil {
		return Cleaned{}, err
	}
	text, usage, err := c.complete(ctx, cleanup.SystemPrompt(mode), userPrompt, 0)
	if err != nil {
		return Cleaned{}, err
	}
	return Cleaned{Text: text, Usage: usage}, nil
}

// CleanNote rewrites a whole note as one document. Unlike Cleanup the
// completion is capped: the answer is bounded by the input it rewrites, and a
// model that starts repeating itself is cut off rather than billed to the end
// of its context.
func (c *OpenAICleanup) CleanNote(ctx context.Context, mode model.NoteCleanMode, body string) (Cleaned, error) {
	systemPrompt, userPrompt, err := cleanup.NotePrompt(mode, body)
	if err != nil {
		return Cleaned{}, err
	}
	text, usage, err := c.complete(ctx, systemPrompt, userPrompt, cleanup.NoteMaxTokens(body))
	if err != nil {
		return Cleaned{}, err
	}
	return Cleaned{Text: text, Usage: usage}, nil
}

// Ask answers one question over the packed notes. Like Route the completion
// is a JSON object and is read back with the shared extractor, so a model
// that wraps its answer in a fence or a sentence still parses; the caller
// filters the cited ids to the notes it packed and bounds the answer.
func (c *OpenAICleanup) Ask(ctx context.Context, q ask.Prompt) (Answer, error) {
	systemPrompt, userPrompt, err := q.Render()
	if err != nil {
		return Answer{}, err
	}
	out, usage, err := c.complete(ctx, systemPrompt, userPrompt, ask.MaxOutputTokens)
	if err != nil {
		return Answer{}, err
	}
	parsed, err := ask.ParseAnswer(out)
	if err != nil {
		return Answer{}, err
	}
	return Answer{Text: parsed.Text, Sources: parsed.Sources, Grounded: parsed.Grounded, Usage: usage}, nil
}

// complete runs a single chat completion and returns the assistant message text
// together with what it consumed. A positive maxTokens caps the completion;
// zero leaves the provider's default, which cleanup needs because its output is
// as long as the recording.
func (c *OpenAICleanup) complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, TokenUsage, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		// MiniMax-M3 enables thinking by default; disable for deterministic cleanup text.
		"thinking": map[string]string{"type": "disabled"},
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: marshal llm request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: build llm request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: llm request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: read llm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not include response body — may contain transcript content.
		// Typed, so the pipeline can tell a revoked key from a throttle. The
		// rendered string is unchanged.
		return "", TokenUsage{}, &StatusError{Op: "llm request failed", StatusCode: resp.StatusCode}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", TokenUsage{}, fmt.Errorf("provider: llm returned no choices")
	}
	out := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if out == "" {
		return "", TokenUsage{}, fmt.Errorf("provider: llm returned empty content")
	}
	usage := TokenUsage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	return out, usage, nil
}
