package provider

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveNoiseProbe sends every clip in STT_PROBE_DIR through the real
// transcription provider and prints what rule 13 reads of each: the text,
// NoSpeech's answer, and every segment's no_speech_prob, avg_logprob and
// compression_ratio. It is how the silence gate's numbers are measured
// (docs/design/routing.md, "Before routing: the transcription gates") and
// is skipped unless asked for: it costs cents and needs the instance's key,
// read from the environment and never printed. The clips are generated, not
// recorded — ffmpeg's anoisesrc for noise, a text-to-speech sentence as the
// control — so what they transcribe to is not anyone's speech.
//
//	LIVE_STT=1 GROQ_API_KEY=… STT_PROBE_DIR=~/temp/noise go test ./internal/provider -run TestLiveNoiseProbe -v -count=1
//
// GROQ_BASE_URL and GROQ_STT_MODEL default to the worker's.
func TestLiveNoiseProbe(t *testing.T) {
	if os.Getenv("LIVE_STT") != "1" {
		t.Skip("set LIVE_STT=1, GROQ_API_KEY and STT_PROBE_DIR to measure the silence gate against the real provider")
	}
	key, dir := os.Getenv("GROQ_API_KEY"), os.Getenv("STT_PROBE_DIR")
	if key == "" || dir == "" {
		t.Fatal("GROQ_API_KEY and STT_PROBE_DIR are required with LIVE_STT=1")
	}
	stt, err := NewGroqSTT(key, os.Getenv("GROQ_BASE_URL"), os.Getenv("GROQ_STT_MODEL"), &http.Client{Timeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	clips, _ := filepath.Glob(filepath.Join(dir, "*.*"))
	if len(clips) == 0 {
		t.Fatalf("no clips in %s", dir)
	}
	for _, clip := range clips {
		t.Run(filepath.Base(clip), func(t *testing.T) {
			f, err := os.Open(clip)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			out, err := stt.Transcribe(context.Background(), Audio{Body: f, ContentType: probeContentType(clip)})
			if err != nil {
				t.Fatalf("ERROR %v", err)
			}
			t.Logf("%.1fs | no_speech=%v | language=%s | %q", out.Duration, out.NoSpeech(), out.Language, out.Text)
			for i, s := range out.Segments {
				t.Logf("  segment %d %.2f–%.2fs no_speech_prob=%.3f avg_logprob=%.3f compression_ratio=%.3f %q",
					i+1, s.Start, s.End, s.NoSpeechProb, s.AvgLogprob, s.CompressionRatio, s.Text)
			}
		})
	}
}

// probeContentType is the container the app would have uploaded the clip as.
func probeContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	default:
		return "audio/webm"
	}
}
