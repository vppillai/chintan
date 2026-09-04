package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGroqSTTTranscribeRequestShape(t *testing.T) {
	t.Parallel()

	const wantModel = "whisper-large-v3-turbo"
	audio := []byte("fake-audio-bytes")
	contentType := "audio/wav"

	var gotAuth string
	var gotModel string
	var gotFormat string
	var gotGranularities []string
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
			case "response_format":
				b, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("read response_format field: %v", err)
				}
				gotFormat = string(b)
			case "timestamp_granularities[]":
				b, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("read granularity field: %v", err)
				}
				gotGranularities = append(gotGranularities, string(b))
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

	got, err := stt.Transcribe(context.Background(), Audio{
		Body:        bytes.NewReader(audio),
		ContentType: contentType,
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got.Text != "hello world" {
		t.Fatalf("text = %q, want hello world", got.Text)
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

	// Timestamps are what drive tap-to-seek. Requesting them is not optional:
	// there is no way to recover them from the text afterwards.
	if gotFormat != "verbose_json" {
		t.Errorf("response_format = %q, want verbose_json", gotFormat)
	}
	wantGranularities := map[string]bool{"segment": true, "word": true}
	for _, g := range gotGranularities {
		delete(wantGranularities, g)
	}
	if len(wantGranularities) != 0 {
		t.Errorf("timestamp granularities = %v, missing %v", gotGranularities, wantGranularities)
	}
}

// The verbose_json shape: segment and word timings, and the duration the spend
// estimate is priced from.
func TestGroqSTTTranscribeParsesTimestamps(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"task": "transcribe",
			"language": "english",
			"duration": 7.25,
			"text": "the gutter is leaking",
			"segments": [
				{"id":0,"start":0.0,"end":3.5,"text":"the gutter"},
				{"id":1,"start":3.5,"end":7.25,"text":" is leaking"}
			],
			"words": [
				{"word":"the","start":0.0,"end":0.28},
				{"word":"gutter","start":0.28,"end":0.9}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	stt, err := NewGroqSTT("k", srv.URL, "", srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	got, err := stt.Transcribe(context.Background(), Audio{
		Body: strings.NewReader("audio"), ContentType: "audio/webm",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got.Text != "the gutter is leaking" {
		t.Errorf("text = %q", got.Text)
	}
	if got.Language != "english" {
		t.Errorf("language = %q, want english", got.Language)
	}
	if got.DurationMS() != 7250 {
		t.Errorf("duration = %dms, want 7250", got.DurationMS())
	}
	if len(got.Segments) != 2 || got.Segments[1].Start != 3.5 {
		t.Fatalf("segments = %+v", got.Segments)
	}
	if len(got.Words) != 2 || got.Words[1].Word != "gutter" {
		t.Fatalf("words = %+v", got.Words)
	}
}

// A provider that omits duration must not price the call at zero.
func TestGroqSTTDerivesDurationFromTimings(t *testing.T) {
	t.Parallel()

	got, err := decodeTranscription(strings.NewReader(
		`{"text":"x","segments":[{"start":0,"end":4.5,"text":"x"}]}`))
	if err != nil {
		t.Fatalf("decodeTranscription: %v", err)
	}
	if got.DurationMS() != 4500 {
		t.Fatalf("duration = %dms, want 4500 derived from the last segment", got.DurationMS())
	}
}

func TestGroqSTTTranscribeHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	stt, err := NewGroqSTT("test-groq-key", srv.URL, "whisper-large-v3-turbo", srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	_, err = stt.Transcribe(context.Background(), Audio{
		Body: strings.NewReader("audio"), ContentType: "audio/wav",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %v, want status 429 mention", err)
	}
	if strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error echoed the response body: %v", err)
	}
}

// The recording is fetched from a presigned GET rather than handed over as
// bytes, which is what lets a twenty-minute drive be transcribed at all.
func TestGroqSTTFetchesFromAPresignedURL(t *testing.T) {
	t.Parallel()

	const payload = "opus-frames-would-go-here"
	var fetched atomic.Int32

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(source.Close)

	var forwarded string
	groq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse Content-Type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			if part.FormName() == "file" {
				b, _ := io.ReadAll(part)
				forwarded = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok","duration":1}`))
	}))
	t.Cleanup(groq.Close)

	stt, err := NewGroqSTT("k", groq.URL, "", groq.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	if _, err := stt.Transcribe(context.Background(), Audio{URL: source.URL, ContentType: "audio/webm"}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if fetched.Load() != 1 {
		t.Fatalf("source fetched %d times, want 1", fetched.Load())
	}
	if forwarded != payload {
		t.Fatalf("forwarded %q, want the source payload", forwarded)
	}
}

// windowedReader produces size bytes and records the largest number of bytes
// that were ever outstanding — produced by the source but not yet consumed by
// the destination.
//
// This is the property that matters. Reading the whole object into a []byte and
// writing it into a bytes.Buffer makes the outstanding window the whole file,
// and the Lambda heap the real cap on recording length. Streaming keeps the
// window at one copy buffer no matter how long the recording is.
type windowedReader struct {
	remaining int64
	produced  *atomic.Int64
	consumed  *atomic.Int64
	maxWindow *atomic.Int64
}

func (r *windowedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'a'
	}
	r.remaining -= n

	outstanding := r.produced.Add(n) - r.consumed.Load()
	for {
		peak := r.maxWindow.Load()
		if outstanding <= peak || r.maxWindow.CompareAndSwap(peak, outstanding) {
			break
		}
	}
	return int(n), nil
}

func TestGroqSTTDoesNotBufferTheWholeRecording(t *testing.T) {
	t.Parallel()

	const (
		size = 64 << 20 // 64 MiB, comfortably past the API Lambda's whole heap
		// Generous: http chunking, the multipart writer, and the pipe together
		// hold well under this. Buffering the file would blow it by 8x.
		maxOutstanding = 8 << 20
	)

	var produced, consumed, maxWindow atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				consumed.Add(int64(n))
			}
			if err != nil {
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok","duration":1200}`))
	}))
	t.Cleanup(srv.Close)

	stt, err := NewGroqSTT("k", srv.URL, "", srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	source := &windowedReader{remaining: size, produced: &produced, consumed: &consumed, maxWindow: &maxWindow}
	if _, err := stt.Transcribe(context.Background(), Audio{Body: source, ContentType: "audio/webm"}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if produced.Load() != size {
		t.Fatalf("produced %d bytes, want %d", produced.Load(), size)
	}
	if peak := maxWindow.Load(); peak > maxOutstanding {
		t.Fatalf("peak outstanding bytes = %s of a %s recording; the adapter is buffering rather than streaming",
			humanBytes(peak), humanBytes(size))
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// The language field is optional on Groq's side and is sent only when the
// caller chose one: "" means detect, and the field is omitted rather than sent
// empty, which the API would reject.
func TestGroqSTTSendsTheLanguageOnlyWhenGiven(t *testing.T) {
	t.Parallel()

	fieldsOf := func(t *testing.T, language string) map[string]string {
		t.Helper()
		got := map[string]string{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Fatalf("parse Content-Type: %v", err)
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read part: %v", err)
				}
				b, _ := io.ReadAll(part)
				if part.FormName() != "file" {
					got[part.FormName()] = string(b)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"ok","duration":1}`))
		}))
		t.Cleanup(srv.Close)

		stt, err := NewGroqSTT("k", srv.URL, "", srv.Client())
		if err != nil {
			t.Fatalf("NewGroqSTT: %v", err)
		}
		if _, err := stt.Transcribe(context.Background(), Audio{
			Body: bytes.NewReader([]byte("audio")), ContentType: "audio/webm", Language: language,
		}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		return got
	}

	if got := fieldsOf(t, "ta"); got["language"] != "ta" {
		t.Errorf("language field = %q, want ta", got["language"])
	}
	if got := fieldsOf(t, ""); got["language"] != "" {
		t.Errorf("an unset language was sent as %q; the field must be omitted so Whisper detects", got["language"])
	}
}
