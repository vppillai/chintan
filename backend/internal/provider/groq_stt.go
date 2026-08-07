package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const (
	defaultGroqBaseURL = "https://api.groq.com/openai/v1"
	defaultGroqModel   = "whisper-large-v3-turbo"
)

// GroqSTT implements STT via Groq's OpenAI-compatible Whisper API.
type GroqSTT struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewGroqSTT builds a Groq STT client. baseURL and model may be empty for defaults.
// httpClient may be nil to use a default client (tests pass httptest.Server.Client()).
func NewGroqSTT(apiKey, baseURL, model string, httpClient *http.Client) (*GroqSTT, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("provider: groq api key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGroqBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = defaultGroqModel
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &GroqSTT{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: httpClient,
	}, nil
}

func (g *GroqSTT) Transcribe(ctx context.Context, audio []byte, contentType string) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("provider: empty audio")
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("model", g.model); err != nil {
		return "", fmt.Errorf("provider: write model field: %w", err)
	}
	filename := filenameForContentType(contentType)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("provider: create file part: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("provider: write audio: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("provider: close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("provider: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("provider: groq request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("provider: read groq response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not include response body — may echo transcript fragments.
		return "", fmt.Errorf("provider: groq transcription failed: status %d", resp.StatusCode)
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("provider: decode groq response: %w", err)
	}
	return parsed.Text, nil
}

func filenameForContentType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(ct, "webm"):
		return "audio.webm"
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return "audio.mp3"
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "m4a"):
		return "audio.m4a"
	case strings.Contains(ct, "ogg"):
		return "audio.ogg"
	default:
		return "audio.wav"
	}
}
