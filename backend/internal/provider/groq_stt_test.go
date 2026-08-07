package provider

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroqSTTTranscribeRequestShape(t *testing.T) {
	t.Parallel()

	const wantModel = "whisper-large-v3-turbo"
	audio := []byte("fake-audio-bytes")
	contentType := "audio/wav"

	var gotAuth string
	var gotModel string
	var gotFile []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %q, want /audio/transcriptions", r.URL.Path)
		}

		gotAuth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse Content-Type: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("media type = %q, want multipart/form-data", mediaType)
		}

		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read multipart part: %v", err)
			}

			switch part.FormName() {
			case "model":
				b, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("read model field: %v", err)
				}
				gotModel = string(b)
			case "file":
				gotFile, err = io.ReadAll(part)
				if err != nil {
					t.Fatalf("read file field: %v", err)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello world"}`))
	}))
	t.Cleanup(srv.Close)

	stt, err := NewGroqSTT("test-groq-key", srv.URL, wantModel, srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	got, err := stt.Transcribe(context.Background(), audio, contentType)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("text = %q, want hello world", got)
	}
	if gotAuth != "Bearer test-groq-key" {
		t.Errorf("Authorization = %q, want Bearer test-groq-key", gotAuth)
	}
	if gotModel != wantModel {
		t.Errorf("model = %q, want %q", gotModel, wantModel)
	}
	if string(gotFile) != string(audio) {
		t.Errorf("file payload = %q, want %q", gotFile, audio)
	}
}

func TestGroqSTTTranscribeHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	stt, err := NewGroqSTT("test-groq-key", srv.URL, "whisper-large-v3-turbo", srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	_, err = stt.Transcribe(context.Background(), []byte("audio"), "audio/wav")
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %v, want status 429 mention", err)
	}
}
