package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

func TestOpenAICleanupRequestShape(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Cleaned text."}}],"usage":{"prompt_tokens":91,"completion_tokens":13}}`))
	}))
	t.Cleanup(srv.Close)

	llm, err := NewOpenAICleanup("test-llm-key", srv.URL, "MiniMax-M3", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}

	got, err := llm.Cleanup(context.Background(), model.CleanupFaithful, "raw transcript here")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got.Text != "Cleaned text." {
		t.Fatalf("got %q", got.Text)
	}
	// Token counts are what the breaker reconciles its reservation against, so a
	// day's spend reflects what was actually consumed rather than what was
	// guessed. They are counts only and can never carry what was said.
	if got.Usage.InputTokens != 91 || got.Usage.OutputTokens != 13 {
		t.Errorf("usage = %+v, want 91 in / 13 out", got.Usage)
	}
	if gotAuth != "Bearer test-llm-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["model"] != "MiniMax-M3" {
		t.Errorf("model = %v", gotBody["model"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	// Thinking disabled for MiniMax-M3
	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("thinking = %#v, want type=disabled", gotBody["thinking"])
	}
}

func TestOpenAICleanupHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	llm, err := NewOpenAICleanup("k", srv.URL, "MiniMax-M3", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}
	_, err = llm.Cleanup(context.Background(), model.CleanupPolished, "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %v, want 502", err)
	}
}

func TestOpenAICleanupRejectsEmptyRaw(t *testing.T) {
	t.Parallel()
	llm, err := NewOpenAICleanup("k", "http://example.invalid", "MiniMax-M3", nil)
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}
	_, err = llm.Cleanup(context.Background(), model.CleanupFaithful, "   ")
	if err == nil {
		t.Fatal("expected error for empty raw")
	}
}

// The whole-note call is the one completion that is capped: its answer is
// bounded by the body it rewrites, so max_tokens is sent, at about one and a
// half times the input, where per-capture cleanup deliberately sends none.
func TestOpenAICleanNoteCapsTheCompletionAndFencesTheBody(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# Roof\n\n- call the roofer"}}],"usage":{"prompt_tokens":120,"completion_tokens":20}}`))
	}))
	t.Cleanup(srv.Close)

	llm, err := NewOpenAICleanup("test-llm-key", srv.URL, "MiniMax-M3", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}
	body := strings.Repeat("the roof leaks and the roofer comes on the fourteenth. ", 100) // ~5.5 KB
	got, err := llm.CleanNote(context.Background(), model.NoteCleanStructured, body)
	if err != nil {
		t.Fatalf("CleanNote: %v", err)
	}
	if got.Text != "# Roof\n\n- call the roofer" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.Usage.InputTokens != 120 || got.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v", got.Usage)
	}

	maxTokens, ok := gotBody["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens was not sent: %v", gotBody)
	}
	estimate := float64(len(body)/4 + 1)
	if maxTokens < estimate*1.4 || maxTokens > estimate*1.6 {
		t.Errorf("max_tokens = %v for a %d-byte body; want about 1.5x the 4-chars-per-token estimate (%v)", maxTokens, len(body), estimate)
	}
	messages := gotBody["messages"].([]any)
	user := messages[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "-----TRANSCRIPT-----\n"+body+"\n-----TRANSCRIPT-----") {
		t.Errorf("the body was not fenced: %q", user[:80])
	}
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(strings.ToLower(system), "structured") {
		t.Errorf("system prompt is not the structured brief: %q", system[:60])
	}
}
