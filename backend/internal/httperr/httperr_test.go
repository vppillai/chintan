package httperr

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/obs"
)

func decode(t *testing.T, w *httptest.ResponseRecorder) Problem {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != ContentType {
		t.Fatalf("Content-Type = %q, want %q", got, ContentType)
	}
	var p Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (body=%s)", err, w.Body.String())
	}
	return p
}

func TestProblemCarriesTheContractFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/notes/n1", nil)
	r = r.WithContext(obs.WithCorrelationID(r.Context(), "corr-1"))
	w := httptest.NewRecorder()

	NotFound(w, r, "no such note")

	p := decode(t, w)
	if p.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", p.Status)
	}
	if p.Type == "" {
		t.Error("type is empty; RFC 9457 requires about:blank at minimum")
	}
	if p.Title == "" {
		t.Error("title is empty")
	}
	if p.Detail != "no such note" {
		t.Errorf("detail = %q", p.Detail)
	}
	if p.Instance != "/v1/notes/n1" {
		t.Errorf("instance = %q, want the request path", p.Instance)
	}
	if p.CorrelationID != "corr-1" {
		t.Errorf("correlation_id = %q, want the request's", p.CorrelationID)
	}
}

func TestConflictCarriesCurrentVersion(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/v1/notes/n1", nil)
	w := httptest.NewRecorder()

	v := int64(7)
	Conflict(w, r, "the note moved on", &v)

	p := decode(t, w)
	if p.CurrentVersion == nil || *p.CurrentVersion != 7 {
		t.Fatalf("current_version = %v, want 7", p.CurrentVersion)
	}
}

// The v1 defect: WriteJSON serialised err.Error(), which put wrapped DynamoDB
// and S3 messages — table names included — in front of anyone who could
// provoke a failure.
func TestInternalServerErrorDoesNotSerialiseTheError(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	w := httptest.NewRecorder()

	InternalServerError(w, r, errors.New("dynamo query chintan-prod-table: AccessDenied"))

	body := w.Body.String()
	for _, leak := range []string{"chintan-prod-table", "AccessDenied", "dynamo"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaked infrastructure detail %q: %s", leak, body)
		}
	}
	if decode(t, w).Status != http.StatusInternalServerError {
		t.Error("status is not 500")
	}
}

// The other half of the v1 defect: InternalServerError discarded its error
// argument entirely, so the most serious class of failure produced no log line.
func TestInternalServerErrorLogsTheErrorWithTheCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	r = r.WithContext(obs.WithCorrelationID(r.Context(), "corr-42"))
	w := httptest.NewRecorder()

	InternalServerError(w, r, errors.New("s3 get object: NoSuchBucket"))

	logged := buf.String()
	if !strings.Contains(logged, "NoSuchBucket") {
		t.Fatalf("the error was not logged: %s", logged)
	}
	if !strings.Contains(logged, "corr-42") {
		t.Fatalf("the log line has no correlation id: %s", logged)
	}
	if p := decode(t, w); p.CorrelationID != "corr-42" {
		t.Errorf("response correlation_id = %q, want the logged one", p.CorrelationID)
	}
}

func TestMethodNotAllowedSetsAllow(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/v1/notes", nil)
	w := httptest.NewRecorder()

	MethodNotAllowed(w, r, "GET, POST")

	if got := w.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want the permitted methods", got)
	}
	if decode(t, w).Status != http.StatusMethodNotAllowed {
		t.Error("status is not 405")
	}
}

func TestTooManyRequestsSetsRetryAfter(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/webauthn/login", nil)
	w := httptest.NewRecorder()

	TooManyRequests(w, r, "slow down", 30)

	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want 30", got)
	}
}
