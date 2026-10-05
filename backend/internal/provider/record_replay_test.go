package provider

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// LLM_RECORD writes the raw reply and its usage under the prompt's key, and
// LLM_REPLAY serves that file with no server at all; a prompt that was never
// recorded fails with the re-record instruction rather than calling out or
// passing on an empty reply.
func TestRecordThenReplayServesTheSameReplyAndAMissSaysReRecord(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  Cleaned text.\n"}}],"usage":{"prompt_tokens":91,"completion_tokens":13}}`))
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	t.Setenv("LLM_RECORD", dir)
	rec, err := NewOpenAICleanup("k", srv.URL, "m", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Cleanup(ctx, "raw transcript", ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("recorded %d files, want 1", len(files))
	}

	t.Setenv("LLM_RECORD", "")
	t.Setenv("LLM_REPLAY", dir)
	rep, err := NewOpenAICleanup("k", "http://127.0.0.1:1", "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rep.Cleanup(ctx, "raw transcript", "")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got.Text != "Cleaned text." || got.Usage.InputTokens != 91 || got.Usage.OutputTokens != 13 {
		t.Errorf("replay = %+v, want the recorded reply trimmed and its usage", got)
	}
	if calls != 1 {
		t.Errorf("server called %d times, want 1 (the replay must not call out)", calls)
	}

	if _, err := rep.Cleanup(ctx, "a transcript nobody recorded", ""); !errors.Is(err, ErrNoRecording) {
		t.Errorf("miss = %v, want ErrNoRecording", err)
	}
}

// The live eval's interval: 10/10 is not "certainly always", and 5/10 is
// wide. Reference values from the Wilson formula at z = 1.96.
func TestWilsonInterval(t *testing.T) {
	for _, tc := range []struct {
		k, n   int
		lo, hi float64
	}{{10, 10, 0.722, 1}, {5, 10, 0.237, 0.763}, {0, 10, 0, 0.278}} {
		lo, hi := wilson(tc.k, tc.n)
		if math.Abs(lo-tc.lo) > 0.001 || math.Abs(hi-tc.hi) > 0.001 {
			t.Errorf("wilson(%d, %d) = [%.3f, %.3f], want [%.3f, %.3f]", tc.k, tc.n, lo, hi, tc.lo, tc.hi)
		}
	}
}

// A normal build ignores both variables: set in a worker's environment they
// must neither serve captures from disk nor write transcripts to it.
func TestANonTestBuildIgnoresRecordAndReplay(t *testing.T) {
	t.Setenv("LLM_RECORD", t.TempDir())
	t.Setenv("LLM_REPLAY", t.TempDir())
	allowed := recordReplayAllowed
	recordReplayAllowed = func() bool { return false }
	t.Cleanup(func() { recordReplayAllowed = allowed })

	c, err := NewOpenAICleanup("k", "", "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.recordDir != "" || c.replayDir != "" {
		t.Errorf("record %q replay %q, want both ignored outside a test binary", c.recordDir, c.replayDir)
	}
}
