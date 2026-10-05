package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The local wiring, booted on a loopback port, takes one capture the way the
// app sends it — the create, the presigned-equivalent PUT with its headers —
// through every stage over the fakes to a note, with the static token as the
// only credential and the hosted-UI stand-in answering the sign-in.
func TestLocalRunsACaptureThroughEveryStageOverTheFakes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	base := "http://" + ln.Addr().String()
	h, err := build(config{token: "t0k", origin: "http://localhost:5173", transcript: "x", model: "MiniMax-M3"}, base)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = http.Serve(ln, h) }()

	do := func(method, path, body string, headers map[string]string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	authed := map[string]string{"Authorization": "Bearer t0k", "Content-Type": "application/json"}

	if code, _ := do(http.MethodGet, "/v1/notes", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/notes without a token = %d, want 401", code)
	}
	if code, _ := do(http.MethodGet, "/v1/notes", "", map[string]string{"Authorization": "Bearer wrong"}); code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/notes with the wrong token = %d, want 401", code)
	}

	// The sign-in the app performs: authorize redirects back with a code and
	// the state, and the token endpoint hands out the accepted bearer.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(base + "/oauth2/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A5173%2F&state=s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.Contains(loc, "code=local") || !strings.Contains(loc, "state=s1") {
		t.Fatalf("authorize = %d %q, want a redirect back with the code and the state", resp.StatusCode, loc)
	}
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if code, body := do(http.MethodPost, "/oauth2/token", "grant_type=authorization_code", nil); code != http.StatusOK || json.Unmarshal(body, &tokens) != nil || tokens.IDToken != "t0k" {
		t.Fatalf("token = %d %s, want the accepted bearer", code, body)
	}

	var created struct {
		Capture struct {
			ID string `json:"id"`
		} `json:"capture"`
		Upload struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	code, body := do(http.MethodPost, "/v1/captures", `{"content_type":"audio/webm","size_bytes":3,"duration_ms":3000}`, authed)
	if code != http.StatusCreated || json.Unmarshal(body, &created) != nil {
		t.Fatalf("POST /v1/captures = %d %s", code, body)
	}
	if !strings.HasPrefix(created.Upload.URL, base+"/objects/") {
		t.Fatalf("upload url %q is not this process's bucket endpoint", created.Upload.URL)
	}
	if code, body := do(http.MethodPut, strings.TrimPrefix(created.Upload.URL, base)+"?text=buy+milk+and+eggs", "abc", created.Upload.Headers); code != http.StatusOK {
		t.Fatalf("PUT the recording = %d %s", code, body)
	}

	var capture struct {
		Status string  `json:"status"`
		NoteID *string `json:"note_id"`
		Error  *string `json:"error"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, body := do(http.MethodGet, "/v1/captures/"+created.Capture.ID, "", authed); json.Unmarshal(body, &capture) == nil && (capture.Status == "appended" || capture.Status == "failed" || capture.Status == "needs_target") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capture did not finish: %+v", capture)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if capture.Status != "appended" || capture.NoteID == nil {
		t.Fatalf("capture ended %+v, want appended to a note", capture)
	}
	if code, body := do(http.MethodGet, "/v1/notes/"+*capture.NoteID, "", authed); code != http.StatusOK || !strings.Contains(strings.ToLower(string(body)), "buy milk and eggs") {
		t.Fatalf("GET the note = %d %s, want the transcript the upload asked for", code, body)
	}
	// The recording is readable back from the same endpoint, as a playback would.
	if code, body := do(http.MethodGet, strings.TrimPrefix(created.Upload.URL, base), "", nil); code != http.StatusOK || string(body) != "abc" {
		t.Fatalf("GET the recording = %d %q", code, body)
	}
}

// The process refuses to be anything but a deliberate local run.
func TestLocalRefusesWithoutTheFlagOrOffLoopback(t *testing.T) {
	t.Setenv("CHINTAN_LOCAL", "")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("started without CHINTAN_LOCAL=1")
	}
	t.Setenv("CHINTAN_LOCAL", "1")
	for _, addr := range []string{"0.0.0.0:8787", ":8787", "192.168.1.2:8787", "example.com:80"} {
		t.Setenv("CHINTAN_LOCAL_ADDR", addr)
		if _, err := configFromEnv(); err == nil {
			t.Errorf("started on %q, which is not loopback", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8787", "localhost:0", "[::1]:8787"} {
		t.Setenv("CHINTAN_LOCAL_ADDR", addr)
		if _, err := configFromEnv(); err != nil {
			t.Errorf("refused %q: %v", addr, err)
		}
	}
}
