package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// harness is a whole API over in-memory storage.
type harness struct {
	router   http.Handler
	store    *memory.Store
	objects  *memory.Objects
	notes    *service.NotesService
	captures *service.CaptureService
	queue    *recordingQueue
	spend    *fakeSpend
	webauthn *fakeWebAuthn
	// remoteAddr overrides the connecting address for subsequent do() calls.
	// Empty leaves httptest's default in place.
	remoteAddr string
}

type harnessOption func(*handler.Deps, *harness)

// withoutWebAuthn builds an instance with biometrics unconfigured, which is what
// an operator whose TOKEN_VAULT_KEY_PATH parameter is missing or unreadable
// gets. Fail closed: no vault key, no biometric unlock, no plaintext fallback.
func withoutWebAuthn() harnessOption {
	return func(d *handler.Deps, _ *harness) { d.WebAuthn = nil }
}

// withBrokenStore makes every settings read fail, so readiness reports degraded.
func withBrokenStore() harnessOption {
	return func(d *handler.Deps, h *harness) {
		d.Readiness = service.NewReadinessService(brokenStore{h.store}, h.objects)
	}
}

// withBrokenObjects makes deletes fail, so a purge cascade cannot complete.
func withBrokenObjects() harnessOption {
	return func(d *handler.Deps, h *harness) {
		notes := service.NewNotesService(h.store, brokenObjects{h.objects})
		d.Notes = notes
	}
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()

	h := &harness{
		store:    memory.NewStore(),
		objects:  memory.NewObjects(),
		queue:    &recordingQueue{},
		spend:    &fakeSpend{},
		webauthn: newFakeWebAuthn(),
	}
	h.notes = service.NewNotesService(h.store, h.objects)
	h.captures = service.NewCaptureService(h.store, h.objects).
		WithQueue(h.queue).
		WithNoteCreator(h.notes)
	settings := service.NewSettingsService(h.store)

	deps := handler.Deps{
		Notes:         h.notes,
		Settings:      settings,
		Captures:      h.captures,
		Search:        service.NewSearchService(h.notes),
		Tags:          service.NewTagsService(h.notes),
		Export:        service.NewExportService(h.notes, h.captures, settings, h.objects),
		Readiness:     service.NewReadinessService(h.store, h.objects),
		WebAuthn:      h.webauthn,
		Spend:         h.spend,
		Store:         h.store,
		AllowedOrigin: "http://localhost:3000",
	}
	for _, opt := range opts {
		opt(&deps, h)
	}
	h.router = handler.New(deps)
	return h
}

// do issues a request as userID. An empty userID means unauthenticated.
func (h *harness) do(t *testing.T, method, path, userID string, body any, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		raw, ok := body.([]byte)
		if !ok {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}
			raw = encoded
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	}
	for _, kv := range headers {
		req.Header.Set(kv[0], kv[1])
	}
	// httptest.NewRequest hardcodes RemoteAddr to 192.0.2.1:1234, so every
	// request a test makes is the same caller unless it says otherwise. That
	// matters for anything keyed on the connecting address — the rate limiter
	// is, deliberately, because X-Forwarded-For is caller-supplied.
	if addr := h.remoteAddr; addr != "" {
		req.RemoteAddr = addr
	}
	if userID != "" {
		req = req.WithContext(middleware.WithUserID(req.Context(), userID))
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// createNote makes a note and returns it.
func (h *harness) createNote(t *testing.T, userID, title string, extra map[string]any) handler.Note {
	t.Helper()
	payload := map[string]any{"title": title}
	for k, v := range extra {
		payload[k] = v
	}
	w := h.do(t, http.MethodPost, "/v1/notes", userID, payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("create note: status=%d body=%s", w.Code, w.Body.String())
	}
	var note handler.Note
	decodeInto(t, w, &note)
	return note
}

// putCapture writes a capture row straight to the store, which is how a test
// reaches a pipeline state the API alone cannot produce.
func (h *harness) putCapture(t *testing.T, c model.CaptureIndex) model.CaptureIndex {
	t.Helper()
	stored, err := h.store.PutCapture(context.Background(), c)
	if err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	return stored
}

func decodeInto(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
}

// problemOf decodes a problem+json body, asserting the envelope on the way.
func problemOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body=%s)", got, w.Body.String())
	}
	var p map[string]any
	decodeInto(t, w, &p)
	for _, field := range []string{"type", "title", "status"} {
		if _, ok := p[field]; !ok {
			t.Errorf("problem is missing required field %q: %v", field, p)
		}
	}
	return p
}

// ---------------------------------------------------------------- doubles

type recordingQueue struct{ calls []string }

func (q *recordingQueue) EnqueueCapture(_ context.Context, tenantID, captureID, reason string) error {
	q.calls = append(q.calls, tenantID+"/"+captureID+"/"+reason)
	return nil
}

type fakeSpend struct {
	capped bool
	err    error
}

func (f *fakeSpend) Capped(context.Context, string) (bool, error) { return f.capped, f.err }

// brokenStore fails the one read readiness makes. Embedding the interface keeps
// every other method the real one.
type brokenStore struct{ repository.Store }

func (brokenStore) GetSettings(context.Context, string) (model.Settings, error) {
	return model.Settings{}, errors.New("dynamodb: dial tcp: connection refused")
}

// brokenObjects fails deletes, so a purge cascade stops half-done.
type brokenObjects struct{ repository.Objects }

func (brokenObjects) Delete(context.Context, string) error {
	return errors.New("s3: AccessDenied on chintan-content-bucket")
}

// fakeWebAuthn stands in for the real ceremony. The biometric routes cannot be
// exercised end to end without an authenticator, and a contract test that
// skipped them would leave four declared statuses unproven.
type fakeWebAuthn struct {
	enrolled     bool
	beginErr     error
	finishRegErr error
	finishLogErr error
	statusErr    error
}

func newFakeWebAuthn() *fakeWebAuthn { return &fakeWebAuthn{} }

func (f *fakeWebAuthn) Status(context.Context, string) (bool, error) {
	return f.enrolled, f.statusErr
}

func (f *fakeWebAuthn) Disable(context.Context, string) error { return nil }

func (f *fakeWebAuthn) BeginRegistration(context.Context, string) (*model.WebAuthnOptionsResponse, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &model.WebAuthnOptionsResponse{ChallengeID: "ch_1", Options: json.RawMessage(`{"challenge":"x"}`)}, nil
}

func (f *fakeWebAuthn) FinishRegistration(context.Context, string, *model.WebAuthnVerifyRequest) error {
	return f.finishRegErr
}

func (f *fakeWebAuthn) BeginLogin(context.Context) (*model.WebAuthnOptionsResponse, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &model.WebAuthnOptionsResponse{ChallengeID: "ch_2", Options: json.RawMessage(`{"challenge":"y"}`)}, nil
}

func (f *fakeWebAuthn) FinishLogin(context.Context, *model.WebAuthnVerifyRequest) (*model.CognitoTokenSet, error) {
	if f.finishLogErr != nil {
		return nil, f.finishLogErr
	}
	return &model.CognitoTokenSet{
		IDToken: "id", AccessToken: "access", ExpiresIn: 3600, TokenType: "Bearer",
	}, nil
}

// TestMain silences the access log and the EMF stream. Both are asserted on
// elsewhere; here they would bury the failures.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	restore := obs.SetMetricOutput(io.Discard)
	defer restore()
	os.Exit(m.Run())
}
