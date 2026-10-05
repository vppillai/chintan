package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// recordDir and replayDir are the eval's LLM_RECORD and LLM_REPLAY
	// (docs/design/routing.md, "Replay: testing the rules without a key"), read only in a test
	// binary (recordReplayAllowed); both empty is a plain call.
	recordDir string
	replayDir string
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
	c := &OpenAICleanup{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: httpClient,
	}
	if recordReplayAllowed() {
		c.recordDir, c.replayDir = os.Getenv("LLM_RECORD"), os.Getenv("LLM_REPLAY")
	}
	return c, nil
}

// Model reports the model this client completes with, so a caller can price the
// call without keeping a second copy of the name.
func (c *OpenAICleanup) Model() string { return c.model }

func (c *OpenAICleanup) Cleanup(ctx context.Context, raw, language string) (Cleaned, error) {
	userPrompt, err := cleanup.UserPrompt(raw, language)
	if err != nil {
		return Cleaned{}, err
	}
	text, usage, err := c.complete(ctx, cleanup.SystemPrompt(), userPrompt, 0, ProviderRetryAttempts)
	if err != nil {
		return Cleaned{}, err
	}
	return Cleaned{Text: text, Usage: usage}, nil
}

// CleanNote rewrites a whole note as one document. Unlike Cleanup the
// completion is capped: the answer is bounded by the input it rewrites, and a
// model that starts repeating itself is cut off rather than billed to the end
// of its context. Tasks mode is its own prompt and cap: the answer is a JSON
// object per item rather than a document.
func (c *OpenAICleanup) CleanNote(ctx context.Context, mode model.NoteCleanMode, body, language, title string) (Cleaned, error) {
	var systemPrompt, userPrompt string
	var err error
	maxTokens := cleanup.NoteMaxTokens(body)
	if mode == model.NoteCleanTasks {
		systemPrompt, userPrompt, err = cleanup.TasksPrompt(body, title, language)
		maxTokens = cleanup.TasksMaxTokens(body)
	} else {
		systemPrompt, userPrompt, err = cleanup.NotePrompt(mode, body, language)
	}
	if err != nil {
		return Cleaned{}, err
	}
	text, usage, err := c.complete(ctx, systemPrompt, userPrompt, maxTokens, ProviderRetryAttempts)
	if err != nil {
		return Cleaned{}, err
	}
	return Cleaned{Text: text, Usage: usage}, nil
}

// Items asks for the checklist items in one recording. The completion is a
// JSON object read back with the shared extractor and bounded by the input it
// extracts from; the caller falls back to the recording as one item when the
// reply is not a list. An empty completion is one such reply — the model
// answered with nothing, which is not a list — rather than the provider
// failure it is for cleanup, where empty text would be stored as the note.
func (c *OpenAICleanup) Items(ctx context.Context, transcript, listTitle, language string) (ChecklistItems, error) {
	systemPrompt, userPrompt, err := cleanup.ItemsPrompt(transcript, listTitle, language)
	if err != nil {
		return ChecklistItems{}, err
	}
	out, usage, err := c.complete(ctx, systemPrompt, userPrompt, cleanup.ItemsMaxTokens(transcript), ProviderRetryAttempts)
	if errors.Is(err, errEmptyContent) {
		return ChecklistItems{}, fmt.Errorf("%w: %v", cleanup.ErrNotAnItemList, err)
	}
	if err != nil {
		return ChecklistItems{}, err
	}
	items, err := cleanup.ParseItems(out)
	if err != nil {
		return ChecklistItems{Usage: usage}, err
	}
	return ChecklistItems{Items: items, Usage: usage}, nil
}

// errEmptyContent is a completion whose message content is empty after
// trimming. Every caller but Items treats it as a failed call.
var errEmptyContent = errors.New("provider: llm returned empty content")

// Ask answers one question over the packed notes. Like Route the completion
// is a JSON object and is read back with the shared extractor, so a model
// that wraps its answer in a fence or a sentence still parses; the caller
// filters the cited ids to the notes it packed and bounds the answer.
func (c *OpenAICleanup) Ask(ctx context.Context, q ask.Prompt) (Answer, error) {
	systemPrompt, userPrompt, err := q.Render()
	if err != nil {
		return Answer{}, err
	}
	out, usage, err := c.complete(ctx, systemPrompt, userPrompt, ask.MaxOutputTokens, ProviderRetryAttemptsAsk)
	if err != nil {
		return Answer{}, err
	}
	parsed, err := ask.ParseAnswer(out)
	if err != nil {
		return Answer{}, err
	}
	return Answer{Text: parsed.Text, Sources: parsed.Sources, Grounded: parsed.Grounded, Usage: usage}, nil
}

// complete runs one chat completion and returns the assistant message text
// together with what it consumed. A positive maxTokens caps the completion;
// zero leaves the provider's default, which cleanup needs because its output is
// as long as the recording. attempts is how many times the HTTP call is sent
// before a refusal is final (retrying; bounds.go says which calls get which).
func (c *OpenAICleanup) complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens, attempts int) (string, TokenUsage, error) {
	var content string
	var usage TokenUsage
	var err error
	if c.replayDir != "" {
		content, usage, err = readRecording(c.replayDir, RecordingKey(c.model, systemPrompt, userPrompt))
	} else {
		err = retrying(ctx, "openai", attempts, func(ctx context.Context) error {
			var callErr error
			content, usage, callErr = c.call(ctx, systemPrompt, userPrompt, maxTokens)
			return callErr
		})
		if err == nil && c.recordDir != "" {
			err = writeRecording(c.recordDir, RecordingKey(c.model, systemPrompt, userPrompt), recording{Model: c.model, Reply: content, Usage: usage})
		}
	}
	if err != nil {
		return "", TokenUsage{}, err
	}
	out := strings.TrimSpace(content)
	if out == "" {
		return "", TokenUsage{}, errEmptyContent
	}
	return out, usage, nil
}

// call is one HTTP chat completion: the message content as the provider sent
// it, untrimmed, so a recording holds the raw reply and a replay goes through
// the same trimming as a live call.
func (c *OpenAICleanup) call(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, TokenUsage, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		// MiniMax-M3 enables thinking by default; disable for deterministic cleanup text.
		"thinking": map[string]string{"type": "disabled"},
		// Pinned: live QA (2026-09-29) saw one transcript cleaned three
		// different ways across runs at the provider's default sampling.
		"temperature": 0,
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
		return "", TokenUsage{}, &StatusError{
			Op: "llm request failed", StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
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
	usage := TokenUsage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	return parsed.Choices[0].Message.Content, usage, nil
}

// recordReplayAllowed gates LLM_RECORD and LLM_REPLAY to test binaries: a
// worker that replayed files would answer every capture from disk, and one
// that recorded would write transcripts to its filesystem. A variable so a
// test can show a normal build ignores both.
var recordReplayAllowed = testing.Testing

// recording is one recorded completion on disk, <dir>/<RecordingKey>.json.
type recording struct {
	Model string     `json:"model"`
	Reply string     `json:"reply"`
	Usage TokenUsage `json:"usage"`
}

// RecordingKey names the recording of one completion: the sha256 of the
// model and both prompts. Any change to a prompt's wording, the candidate
// list or the fixture text is a new key, so a stale recording is a miss and
// never a silent pass over a reply to a prompt that no longer exists.
func RecordingKey(model, systemPrompt, userPrompt string) string {
	h := sha256.New()
	for _, part := range []string{model, systemPrompt, userPrompt} {
		// The length prefix keeps ("ab","c") and ("a","bc") apart.
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeRecording(dir, key string, r recording) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("provider: record llm reply: %w", err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("provider: record llm reply: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("provider: record llm reply: %w", err)
	}
	return nil
}

// errNoRecording is a replay miss: the prompt this call sent was never
// recorded, which after a prompt change is the expected state.
var errNoRecording = errors.New("provider: no recording for this prompt; the prompt, model or fixture changed since it was recorded — re-record on the VM with scripts/dev/record-replay.sh and commit the directory (docs/design/routing.md, \"Replay\")")

func readRecording(dir, key string) (string, TokenUsage, error) {
	b, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", TokenUsage{}, fmt.Errorf("%w (%s in %s)", errNoRecording, key, dir)
	}
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: read recording: %w", err)
	}
	var r recording
	if err := json.Unmarshal(b, &r); err != nil {
		return "", TokenUsage{}, fmt.Errorf("provider: decode recording %s: %w", key, err)
	}
	return r.Reply, r.Usage, nil
}
