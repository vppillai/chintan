package provider

import (
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

	// maxTranscriptResponseBytes bounds the decoded response. It is sized for a
	// transcript with word timestamps, which is proportional to speech, not to
	// the audio file — the audio itself never lands in memory.
	maxTranscriptResponseBytes = 64 << 20
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
		// Below the worker's 900s Lambda timeout, so a hung provider surfaces as
		// an error the pipeline can record rather than as a killed invocation.
		httpClient = &http.Client{Timeout: 840 * time.Second}
	}
	return &GroqSTT{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: httpClient,
	}, nil
}

// Model reports the model this client transcribes with, so the caller can price
// the call without hard-coding a second copy of the name.
func (g *GroqSTT) Model() string { return g.model }

// Transcribe streams the recording into a multipart upload without ever holding
// it in memory.
//
// The source is a presigned GET: the object goes S3 -> this process -> Groq
// through an io.Pipe, so peak allocation is one copy buffer regardless of how
// long the recording is. v1 did objects.Get followed by part.Write(audio), which
// is why a long drive could not be transcribed at all.
func (g *GroqSTT) Transcribe(ctx context.Context, in Audio) (Transcription, error) {
	source, closeSource, err := g.openSource(ctx, in)
	if err != nil {
		return Transcription{}, err
	}
	defer closeSource()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		pw.CloseWithError(g.writeMultipart(mw, source, in.ContentType, in.Language))
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/audio/transcriptions", pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return Transcription{}, fmt.Errorf("provider: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return Transcription{}, fmt.Errorf("provider: groq request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not include response body — may echo transcript fragments.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		// Typed, so the pipeline can tell a revoked key from a throttle. The
		// rendered string is unchanged.
		return Transcription{}, &StatusError{Op: "groq transcription failed", StatusCode: resp.StatusCode}
	}

	return decodeTranscription(io.LimitReader(resp.Body, maxTranscriptResponseBytes))
}

// openSource resolves the recording to a stream. A URL is fetched lazily so the
// body is consumed by the copy below rather than read into a slice first.
func (g *GroqSTT) openSource(ctx context.Context, in Audio) (io.Reader, func(), error) {
	if in.URL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("provider: build source request: %w", err)
		}
		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("provider: fetch source: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return nil, nil, fmt.Errorf("provider: fetch source: status %d", resp.StatusCode)
		}
		return resp.Body, func() { _ = resp.Body.Close() }, nil
	}
	if in.Body != nil {
		return in.Body, func() {}, nil
	}
	return nil, nil, fmt.Errorf("provider: no audio source")
}

// writeMultipart builds the request body incrementally on the writer side of the
// pipe. verbose_json with both granularities is what produces segments.json and
// the duration the spend estimate is priced from.
func (g *GroqSTT) writeMultipart(mw *multipart.Writer, source io.Reader, contentType, language string) error {
	fields := [][2]string{
		{"model", g.model},
		{"response_format", "verbose_json"},
		{"timestamp_granularities[]", "segment"},
		{"timestamp_granularities[]", "word"},
	}
	// Optional in the API and only sent when the caller chose one: an empty
	// value is "detect", and the field is omitted rather than sent empty.
	if language != "" {
		fields = append(fields, [2]string{"language", language})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return fmt.Errorf("provider: write field %s: %w", f[0], err)
		}
	}
	part, err := mw.CreateFormFile("file", filenameForContentType(contentType))
	if err != nil {
		return fmt.Errorf("provider: create file part: %w", err)
	}
	if _, err := io.Copy(part, source); err != nil {
		return fmt.Errorf("provider: stream recording: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("provider: close multipart: %w", err)
	}
	return nil
}

// groqTranscription is the verbose_json shape. The plain-json shape is the same
// document with only `text`, so one decoder covers both and an instance
// configured for the older format still works.
type groqTranscription struct {
	Text     string  `json:"text"`
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
	Words []struct {
		Word  string  `json:"word"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"words"`
}

func decodeTranscription(r io.Reader) (Transcription, error) {
	var parsed groqTranscription
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return Transcription{}, fmt.Errorf("provider: decode groq response: %w", err)
	}

	out := Transcription{
		Text:     parsed.Text,
		Language: parsed.Language,
		Duration: parsed.Duration,
	}
	for _, s := range parsed.Segments {
		out.Segments = append(out.Segments, Segment{Start: s.Start, End: s.End, Text: s.Text})
	}
	for _, w := range parsed.Words {
		out.Words = append(out.Words, Word{Start: w.Start, End: w.End, Word: w.Word})
	}

	// A provider that omits duration must not price the call at zero. The last
	// segment or word ends where the speech ends, which is close enough to bill
	// against and far better than nothing.
	if out.Duration <= 0 {
		if n := len(out.Segments); n > 0 {
			out.Duration = out.Segments[n-1].End
		}
		if n := len(out.Words); n > 0 && out.Words[n-1].End > out.Duration {
			out.Duration = out.Words[n-1].End
		}
	}
	return out, nil
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
