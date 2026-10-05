package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/obs"
)

const okCompletion = `{"choices":[{"message":{"content":"Cleaned text."}}],"usage":{"prompt_tokens":9,"completion_tokens":3}}`

// scriptedServer answers the n-th request with script[n] (a status, with an
// optional Retry-After); past the script, or on a 200, with a completion.
// Status 0 hijacks and closes the connection, a reset as the client sees it.
type scriptedServer struct {
	*httptest.Server
	calls  atomic.Int32
	script []scripted
}

type scripted struct {
	status     int
	retryAfter string
}

func newScriptedServer(t *testing.T, script ...scripted) *scriptedServer {
	t.Helper()
	s := &scriptedServer{script: script}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.calls.Add(1)) - 1
		if n < len(script) && script[n].status != http.StatusOK {
			if script[n].status == 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
				return
			}
			if script[n].retryAfter != "" {
				w.Header().Set("Retry-After", script[n].retryAfter)
			}
			http.Error(w, "refused", script[n].status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okCompletion))
	}))
	t.Cleanup(s.Close)
	return s
}

// recordWaits swaps the retry's sleep for one that records the wait and
// returns at once, and silences the metric output. The tests that need the
// real wait (a cancellation during it) do not call this.
func recordWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	prev := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	restore := obs.SetMetricOutput(io.Discard)
	t.Cleanup(func() { retrySleep = prev; restore() })
	return &waits
}

func newTestLLM(t *testing.T, s *scriptedServer) *OpenAICleanup {
	t.Helper()
	llm, err := NewOpenAICleanup("k", s.URL, "MiniMax-M3", s.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}
	return llm
}

func statusCode(t *testing.T, err error) int {
	t.Helper()
	code, ok := statusOf(err)
	if !ok {
		t.Fatalf("error %v carries no provider status", err)
	}
	return code
}

// The shape the replay recording met: two 529s and then an answer. The call
// succeeds, took three requests, waited twice under the cap, and counted
// each retry by provider and status.
func TestRetryRecoversFromAnOverloadBurst(t *testing.T) {
	waits := recordWaits(t)
	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	defer restore()
	s := newScriptedServer(t, scripted{status: 529}, scripted{status: 529}, scripted{status: 200})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	got, err := newTestLLM(t, s).Cleanup(ctx, "raw transcript here", "")
	if err != nil || got.Text != "Cleaned text." {
		t.Fatalf("Cleanup = %q, %v; want the answer after two refusals", got.Text, err)
	}
	if n := s.calls.Load(); n != 3 {
		t.Errorf("server saw %d requests, want 3", n)
	}
	if len(*waits) != 2 {
		t.Fatalf("waited %d times, want 2: %v", len(*waits), *waits)
	}
	for i, w := range *waits {
		if w < 0 || w >= ProviderRetryMaxWait {
			t.Errorf("wait %d = %s, want under the %s cap", i+1, w, ProviderRetryMaxWait)
		}
	}
	lines := strings.Split(strings.TrimSpace(metrics.String()), "\n")
	counted := 0
	for _, line := range lines {
		if strings.Contains(line, `"ProviderRetried"`) && strings.Contains(line, `"Provider":"openai"`) && strings.Contains(line, `"Status":"5xx"`) {
			counted++
		}
	}
	if counted != 2 {
		t.Errorf("ProviderRetried{openai,5xx} was emitted %d times, want 2:\n%s", counted, metrics.String())
	}
}

// A burst longer than the attempts is an outage: the last refusal is the
// error, with its status, after exactly ProviderRetryAttempts requests.
func TestRetryGivesUpWithTheLastStatus(t *testing.T) {
	recordWaits(t)
	s := newScriptedServer(t, scripted{status: 529}, scripted{status: 529}, scripted{status: 503}, scripted{status: 529})

	_, err := newTestLLM(t, s).Cleanup(context.Background(), "raw transcript here", "")
	if code := statusCode(t, err); code != 503 {
		t.Errorf("error status = %d, want 503, the last refusal", code)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error = %v, want the attempt count on it", err)
	}
	if n := s.calls.Load(); n != ProviderRetryAttempts {
		t.Errorf("server saw %d requests, want %d", n, ProviderRetryAttempts)
	}
}

// A Retry-After is the provider saying when; it is waited as sent, in
// seconds or as a date, instead of the jittered wait.
func TestRetryHonoursRetryAfter(t *testing.T) {
	waits := recordWaits(t)
	at := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	s := newScriptedServer(t, scripted{status: 429, retryAfter: "7"}, scripted{status: 529, retryAfter: at}, scripted{status: 200})

	if _, err := newTestLLM(t, s).Cleanup(context.Background(), "raw transcript here", ""); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(*waits) != 2 || (*waits)[0] != 7*time.Second {
		t.Fatalf("waits = %v, want 7s first", *waits)
	}
	if w := (*waits)[1]; w < 8*time.Second || w > 10*time.Second {
		t.Errorf("second wait = %s, want about 10s from the HTTP date", w)
	}
}

// A Retry-After past ProviderRetryMaxRetryAfter is "come back later": the
// refusal is returned at once, neither waited on nor truncated.
func TestRetryDoesNotWaitOnALongRetryAfter(t *testing.T) {
	waits := recordWaits(t)
	s := newScriptedServer(t, scripted{status: 429, retryAfter: "30"}, scripted{status: 200})

	_, err := newTestLLM(t, s).Cleanup(context.Background(), "raw transcript here", "")
	if code := statusCode(t, err); code != 429 {
		t.Errorf("error status = %d, want the 429 itself", code)
	}
	if n := s.calls.Load(); n != 1 || len(*waits) != 0 {
		t.Errorf("%d requests and %d waits, want 1 and 0", n, len(*waits))
	}
}

// The budget covers the retried request too, measured by the refused one:
// here the wait alone fits before the deadline but the wait plus a request
// as long as the first does not, so the refusal is returned rather than a
// retry the deadline would cut — which the stage would read as a timeout
// and hand to Lambda, where the refusal is a verdict.
func TestRetryBudgetsTheRetriedRequest(t *testing.T) {
	recordWaits(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "overloaded", 529)
	}))
	t.Cleanup(srv.Close)
	llm, err := NewOpenAICleanup("k", srv.URL, "MiniMax-M3", srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAICleanup: %v", err)
	}
	// 1.8 s: after the 0.5 s request about 1.3 s remain — more than the 1 s
	// wait, less than the wait plus another 0.5 s request.
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()

	_, err = llm.Cleanup(ctx, "raw transcript here", "")
	if code := statusCode(t, err); code != 529 {
		t.Errorf("error status = %d, want the 529 itself", code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the refusal was reported as a deadline: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// A fault a moment later cannot clear — a name that does not resolve, a
// certificate that does not verify — is not transport and is not retried.
func TestRetrySkipsFaultsThatWillNotClear(t *testing.T) {
	waits := recordWaits(t)
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okCompletion))
	}))
	t.Cleanup(tls.Close)
	for name, base := range map[string]string{
		"an unresolvable name":        "http://provider.invalid",
		"an unverifiable certificate": tls.URL,
	} {
		// http.DefaultClient's transport, not the test server's, so the
		// certificate is checked like a real one.
		llm, err := NewOpenAICleanup("k", base, "MiniMax-M3", &http.Client{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("NewOpenAICleanup: %v", err)
		}
		if _, err := llm.Cleanup(context.Background(), "raw transcript here", ""); err == nil {
			t.Errorf("%s: no error", name)
		} else if _, ok := statusOf(err); ok {
			t.Errorf("%s: carries a provider status: %v", name, err)
		}
		if len(*waits) != 0 {
			t.Errorf("%s: waited %v; want no retry", name, *waits)
		}
	}
}

// The budget is the context's: a wait that would end after the deadline is
// not taken, and the refusal comes back at once rather than as a timeout.
func TestRetryDoesNotOutliveTheDeadline(t *testing.T) {
	recordWaits(t)
	s := newScriptedServer(t, scripted{status: 529, retryAfter: "2"}, scripted{status: 200})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := newTestLLM(t, s).Cleanup(ctx, "raw transcript here", "")
	if code := statusCode(t, err); code != 529 {
		t.Errorf("error status = %d, want the 529 itself", code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the refusal was reported as a deadline: %v", err)
	}
	if n := s.calls.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
	if took := time.Since(started); took > 400*time.Millisecond {
		t.Errorf("returned after %s; want at once, not at the deadline", took)
	}
}

// A 4xx other than 429 is this request or this key: one request, no wait.
func TestRetrySkipsClientErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413} {
		s := newScriptedServer(t, scripted{status: status}, scripted{status: 200})
		waits := recordWaits(t)
		_, err := newTestLLM(t, s).Cleanup(context.Background(), "raw transcript here", "")
		if code := statusCode(t, err); code != status {
			t.Errorf("%d: error status = %d", status, code)
		}
		if n := s.calls.Load(); n != 1 || len(*waits) != 0 {
			t.Errorf("%d: %d requests and %d waits, want 1 and 0", status, n, len(*waits))
		}
	}
}

// A context cancelled during the wait ends it: the error names both the
// refusal and the cancellation, and nothing is sent again.
func TestRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	restore := obs.SetMetricOutput(io.Discard)
	defer restore()
	s := newScriptedServer(t, scripted{status: 529, retryAfter: "10"}, scripted{status: 200})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	started := time.Now()
	_, err := newTestLLM(t, s).Cleanup(ctx, "raw transcript here", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want one wrapping context.Canceled", err)
	}
	if code := statusCode(t, err); code != 529 {
		t.Errorf("the refusal's status is lost: %v", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("returned after %s; want promptly on the cancellation", took)
	}
	if n := s.calls.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

// A connection the provider closes under the request is a transport fault
// and is sent again, on a fresh connection.
func TestRetrySendsAgainAfterAConnectionReset(t *testing.T) {
	recordWaits(t)
	s := newScriptedServer(t, scripted{status: 0}, scripted{status: 200})
	// One request per connection: a hijacked connection must not be reused.
	s.Client().Transport = &http.Transport{DisableKeepAlives: true, DialContext: (&net.Dialer{}).DialContext}

	got, err := newTestLLM(t, s).Cleanup(context.Background(), "raw transcript here", "")
	if err != nil || got.Text != "Cleaned text." {
		t.Fatalf("Cleanup = %q, %v; want the answer after the reset", got.Text, err)
	}
	if n := s.calls.Load(); n != 2 {
		t.Errorf("server saw %d requests, want 2", n)
	}
}

// Ask is interactive and gets one retry, not two.
func TestAskRetriesOnce(t *testing.T) {
	recordWaits(t)
	s := newScriptedServer(t, scripted{status: 529}, scripted{status: 529}, scripted{status: 200})

	_, err := newTestLLM(t, s).Ask(context.Background(), ask.Prompt{Today: "2026-01-01", Question: "when is the dentist?"})
	if code := statusCode(t, err); code != 529 {
		t.Fatalf("Ask = %v, want the second 529 as the answer", err)
	}
	if n := s.calls.Load(); n != ProviderRetryAttemptsAsk {
		t.Errorf("server saw %d requests, want %d", n, ProviderRetryAttemptsAsk)
	}
}

// The transcription is sent again only when the recording can be read
// again: a presigned URL is opened afresh per attempt; a Body, once read,
// cannot be, so that call gets one attempt.
func TestGroqRetriesAURLSourceAndNotABody(t *testing.T) {
	recordWaits(t)
	var uploads, sourceReads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio" {
			sourceReads.Add(1)
			_, _ = w.Write([]byte("fake-audio-bytes"))
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read upload: %v", err)
		}
		if uploads.Add(1) == 1 {
			http.Error(w, "overloaded", 529)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello","duration":1.5}`))
	}))
	t.Cleanup(srv.Close)
	stt, err := NewGroqSTT("k", srv.URL, "", srv.Client())
	if err != nil {
		t.Fatalf("NewGroqSTT: %v", err)
	}

	got, err := stt.Transcribe(context.Background(), Audio{URL: srv.URL + "/audio", ContentType: "audio/wav"})
	if err != nil || got.Text != "hello" {
		t.Fatalf("Transcribe(URL) = %q, %v; want the transcript after one 529", got.Text, err)
	}
	if u, s := uploads.Load(), sourceReads.Load(); u != 2 || s != 2 {
		t.Errorf("%d uploads from %d source reads, want 2 and 2: each attempt opens the recording afresh", u, s)
	}

	uploads.Store(0)
	_, err = stt.Transcribe(context.Background(), Audio{Body: bytes.NewReader([]byte("fake-audio-bytes")), ContentType: "audio/wav"})
	if code := statusCode(t, err); code != 529 {
		t.Errorf("Transcribe(Body) = %v, want the 529 unretried", err)
	}
	if u := uploads.Load(); u != 1 {
		t.Errorf("a Body source was uploaded %d times, want 1: it cannot be read again", u)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for header, want := range map[string]time.Duration{
		"":     0,
		"3":    3 * time.Second,
		"-1":   0,
		"soon": 0,
		now.Add(time.Minute).Format(http.TimeFormat):  time.Minute,
		now.Add(-time.Minute).Format(http.TimeFormat): 0,
	} {
		if got := parseRetryAfter(header, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", header, got, want)
		}
	}
}
