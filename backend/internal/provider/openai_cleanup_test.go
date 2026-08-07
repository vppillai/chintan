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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Cleaned text."}}]}`))
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
	if got != "Cleaned text." {
		t.Fatalf("got %q", got)
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
