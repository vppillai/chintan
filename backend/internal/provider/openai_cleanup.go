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
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &OpenAICleanup{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: httpClient,
	}, nil
}

func (c *OpenAICleanup) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (string, error) {
	userPrompt, err := cleanup.UserPrompt(raw)
	if err != nil {
		return "", err
	}
	systemPrompt := cleanup.SystemPrompt(mode)

	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		// MiniMax-M3 enables thinking by default; disable for deterministic cleanup text.
		"thinking": map[string]string{"type": "disabled"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("provider: marshal cleanup request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("provider: build cleanup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("provider: llm request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("provider: read llm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not include response body — may contain transcript content.
		return "", fmt.Errorf("provider: llm cleanup failed: status %d", resp.StatusCode)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("provider: decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("provider: llm returned no choices")
	}
	out := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("provider: llm returned empty content")
	}
	return out, nil
}
