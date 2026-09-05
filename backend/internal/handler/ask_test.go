package handler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// POST /v1/ask writes a pending row, hands it to the worker and answers 202
// with every member present; GET reads it back, and only for its owner.
func TestAskIsAcceptedNotAnswered(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{
		"question": "  what did I decide about the roof?  ",
		"history":  []map[string]string{{"question": "roof?", "answer": "Replace it."}},
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got handler.Ask
	decodeInto(t, w, &got)
	if got.Status != "pending" || got.Question != "what did I decide about the roof?" || got.ID == "" {
		t.Errorf("ask = %+v", got)
	}
	if got.Answer != nil || got.Error != nil || got.AnsweredAt != nil || got.Grounded || len(got.Sources) != 0 {
		t.Errorf("a pending row carries an answer: %+v", got)
	}
	// Present-and-null is the contract: the client never guesses.
	for _, want := range []string{`"answer":null`, `"error":null`, `"answered_at":null`, `"sources":[]`, `"notes_considered":0`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("body lacks %s: %s", want, w.Body.String())
		}
	}
	if len(h.worker.calls) != 1 || h.worker.calls[0] != "ask/user1/"+got.ID {
		t.Errorf("worker hand-offs = %v, want exactly the ask", h.worker.calls)
	}

	stored, err := h.store.GetAsk(context.Background(), "user1", got.ID)
	if err != nil {
		t.Fatalf("GetAsk: %v", err)
	}
	if len(stored.History) != 1 || stored.History[0].Answer != "Replace it." {
		t.Errorf("history not stored: %+v", stored.History)
	}
	if stored.ExpiresAt < time.Now().Add(23*time.Hour).Unix() {
		t.Errorf("expires_at = %d, want about a day out", stored.ExpiresAt)
	}

	// Once the worker has written the answer, GET carries it whole.
	stored.Status = model.AskAnswered
	stored.Answer = "You decided to replace it."
	stored.Grounded = true
	stored.Sources = []model.AskSource{{NoteID: "n1", Title: "Roof"}}
	stored.NotesConsidered = 4
	stored.AnsweredAt = model.Now()
	if err := h.store.PutAsk(context.Background(), "user1", stored); err != nil {
		t.Fatalf("PutAsk: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/ask/"+got.ID, "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s", w.Code, w.Body.String())
	}
	var answered handler.Ask
	decodeInto(t, w, &answered)
	if answered.Status != "answered" || answered.Answer == nil || *answered.Answer != "You decided to replace it." ||
		!answered.Grounded || len(answered.Sources) != 1 || answered.Sources[0].Title != "Roof" ||
		answered.NotesConsidered != 4 || answered.AnsweredAt == nil || answered.Error != nil {
		t.Errorf("answered = %+v", answered)
	}

	// A failed row: the fixed sentence, and no answer.
	stored.Status = model.AskFailed
	stored.Answer = ""
	stored.Error = "daily provider spend cap reached"
	stored.Sources = nil
	if err := h.store.PutAsk(context.Background(), "user1", stored); err != nil {
		t.Fatalf("PutAsk: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/ask/"+got.ID, "user1", nil)
	var failed handler.Ask
	decodeInto(t, w, &failed)
	if failed.Status != "failed" || failed.Error == nil || *failed.Error != "daily provider spend cap reached" || failed.Answer != nil || failed.Sources == nil {
		t.Errorf("failed = %+v (body %s)", failed, w.Body.String())
	}

	// Another tenant's id is absent, not forbidden.
	if w := h.do(t, http.MethodGet, "/v1/ask/"+got.ID, "user2", nil); w.Code != http.StatusNotFound {
		t.Errorf("another tenant: status = %d, want 404", w.Code)
	}
	if w := h.do(t, http.MethodGet, "/v1/ask/never", "user1", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", w.Code)
	}
}

func TestAskValidatesTheQuestionAndTheHistory(t *testing.T) {
	h := newHarness(t)
	turn := func(q, a string) map[string]string { return map[string]string{"question": q, "answer": a} }
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"empty question", map[string]any{"question": "   "}},
		{"missing question", map[string]any{}},
		{"question over 1000 runes", map[string]any{"question": strings.Repeat("é", 1001)}},
		{"seven turns", map[string]any{"question": "x?", "history": []map[string]string{
			turn("a", "b"), turn("a", "b"), turn("a", "b"), turn("a", "b"), turn("a", "b"), turn("a", "b"), turn("a", "b")}}},
		{"turn with an empty question", map[string]any{"question": "x?", "history": []map[string]string{turn("", "b")}}},
		{"turn with an over-long answer", map[string]any{"question": "x?", "history": []map[string]string{turn("a", strings.Repeat("b", 4001))}}},
		{"unknown field", map[string]any{"question": "x?", "context": "no"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, http.MethodPost, "/v1/ask", "user1", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			problemOf(t, w)
		})
	}
	if len(h.worker.calls) != 0 {
		t.Errorf("an invalid question reached the worker: %v", h.worker.calls)
	}

	// The whole body is capped: six turns carried with long answers is the
	// case that exceeds it.
	big := make([]map[string]string, 6)
	for i := range big {
		big[i] = turn("q", strings.Repeat("a", 3500))
	}
	w := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "x?", "history": big})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body: status = %d, want 413", w.Code)
	}
}

// The gate answers first, and a replayed key answers the same 202 without a
// second row or a second hand-off.
func TestAskIsGatedAndIdempotent(t *testing.T) {
	h := newHarness(t)
	h.spend.capped = true
	if w := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}); w.Code != http.StatusTooManyRequests {
		t.Errorf("capped: status = %d, want 429", w.Code)
	}
	h.spend.capped = false

	key := [2]string{"Idempotency-Key", "ask-key-00001"}
	first := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}, key)
	again := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}, key)
	if first.Code != http.StatusAccepted || again.Code != http.StatusAccepted {
		t.Fatalf("statuses = %d, %d", first.Code, again.Code)
	}
	if first.Body.String() != again.Body.String() || again.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("the replay did not return the original response")
	}
	if len(h.worker.calls) != 1 {
		t.Errorf("worker hand-offs = %v, want one", h.worker.calls)
	}
	if w := h.do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "garden?"}, key); w.Code != http.StatusConflict {
		t.Errorf("same key, other question: status = %d, want 409", w.Code)
	}

	if w := newHarness(t, withoutAsk()).do(t, http.MethodPost, "/v1/ask", "user1", map[string]any{"question": "roof?"}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no worker: status = %d, want 503", w.Code)
	}
	if w := h.do(t, http.MethodPost, "/v1/ask", "", map[string]any{"question": "roof?"}); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401", w.Code)
	}
}

// targeted on the wire: true when the client named the note or a person chose
// it, false when the router decided or nothing has yet — and false for a row
// written before the field existed.
func TestCaptureTargetedSaysWhoChoseTheNote(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", nil)

	w := h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm", "note_id": note.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("begin: status = %d body = %s", w.Code, w.Body.String())
	}
	var created handler.CaptureCreated
	decodeInto(t, w, &created)
	if !created.Capture.Targeted {
		t.Error("a capture begun with note_id is not targeted")
	}

	w = h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"})
	var unrouted handler.CaptureCreated
	decodeInto(t, w, &unrouted)
	if unrouted.Capture.Targeted {
		t.Error("a capture begun without a note is targeted before anything decided")
	}

	// A person picks the note for a needs_target capture.
	c := h.putCapture(t, model.CaptureIndex{ID: "c_pick", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now()})
	w = h.do(t, http.MethodPost, "/v1/captures/"+c.ID+"/target", "user1", map[string]any{"note_id": note.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("target: status = %d body = %s", w.Code, w.Body.String())
	}
	var picked handler.Capture
	decodeInto(t, w, &picked)
	if !picked.Targeted {
		t.Error("a capture whose note a person chose is not targeted")
	}

	// The router's choice, and a row from before the field existed.
	for _, tc := range []struct {
		name   string
		source model.TargetSource
	}{{"router", model.TargetSourceRouter}, {"legacy row", ""}} {
		c := h.putCapture(t, model.CaptureIndex{ID: "c_" + strings.ReplaceAll(tc.name, " ", "_"), UserID: "user1", NoteID: note.ID,
			TargetSource: tc.source, Status: model.StatusAppended, CreatedAt: model.Now()})
		w := h.do(t, http.MethodGet, "/v1/captures/"+c.ID, "user1", nil)
		var got handler.Capture
		decodeInto(t, w, &got)
		if got.Targeted {
			t.Errorf("%s: targeted = true, want false", tc.name)
		}
		if !strings.Contains(w.Body.String(), `"targeted":false`) {
			t.Errorf("%s: targeted is not on the wire: %s", tc.name, w.Body.String())
		}
	}
}

// Every authenticated request adds one to the caller's usage; health probes
// and unauthenticated requests add nothing, and a failed count is not a
// failed request.
func TestAuthenticatedRequestsAreCountedAgainstUsage(t *testing.T) {
	h := newHarness(t)
	day := time.Now().UTC().Format("2006-01-02")

	h.do(t, http.MethodGet, "/v1/notes", "user1", nil)
	h.do(t, http.MethodGet, "/v1/notes/missing", "user1", nil) // a 404 is still a request
	h.do(t, http.MethodGet, "/v1/settings", "user2", nil)
	h.do(t, http.MethodGet, "/v1/health", "", nil)
	h.do(t, http.MethodGet, "/v1/health/ready", "", nil)
	h.do(t, http.MethodGet, "/v1/notes", "", nil) // 401

	if got := h.usage.Requests("user1", day); got != 2 {
		t.Errorf("user1 requests = %d, want 2", got)
	}
	if got := h.usage.Requests("user2", day); got != 1 {
		t.Errorf("user2 requests = %d, want 1", got)
	}
	if got := h.usage.Requests("", day); got != 0 {
		t.Errorf("anonymous requests = %d, want none", got)
	}

	w := h.do(t, http.MethodGet, "/v1/usage?month="+day[:7], "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("usage: status = %d body = %s", w.Code, w.Body.String())
	}
	var got handler.Usage
	decodeInto(t, w, &got)
	// The GET /v1/usage itself was counted before its response was read? No:
	// the count lands after the handler answers, so the response shows the
	// two earlier requests and this one is on the row for the next reader.
	if got.API.Requests != 2 || len(got.Days) != 1 || got.Days[0].Date != day || got.Days[0].APIRequests != 2 {
		t.Errorf("api = %+v days = %+v", got.API, got.Days)
	}
}

// The extended usage response: providers, storage, and the caller's share of
// the AWS bill, all present on every response.
func TestUsageCarriesProvidersStorageAndTheAWSShare(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, rec := range []usage.Record{
		{TenantID: "user1", Day: "2026-03-02", Provider: "groq", Op: meter.OpTranscribe, CostMicros: 300, Usage: meter.Quantities{meter.UnitAudioSeconds: 30}},
		{TenantID: "user1", Day: "2026-03-09", Provider: "openai", Op: meter.OpAsk, CostMicros: 700, Usage: meter.Quantities{meter.UnitInputTokens: 1000, meter.UnitOutputTokens: 200}},
		{TenantID: "user2", Day: "2026-03-09", Provider: "openai", Op: meter.OpCleanNote, CostMicros: 3000},
	} {
		if err := h.usage.Record(ctx, rec); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
	}
	note := h.createNote(t, "user1", "Roof", nil)
	h.putCapture(t, model.CaptureIndex{ID: "c_1", UserID: "user1", NoteID: note.ID, Status: model.StatusAppended, CreatedAt: model.Now(), DurationMS: 90_500, AudioBytes: 1_000_000})
	h.putCapture(t, model.CaptureIndex{ID: "c_2", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now(), DurationMS: 9_500, AudioBytes: 123_456})
	h.putCapture(t, model.CaptureIndex{ID: "c_other", UserID: "user2", Status: model.StatusAppended, CreatedAt: model.Now(), DurationMS: 5_000})

	w := h.do(t, http.MethodGet, "/v1/usage?month=2026-03", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var got handler.Usage
	decodeInto(t, w, &got)
	if got.Ops["ask"].CostMicros != 700 || got.Ops["transcribe"].AudioSeconds != 30 {
		t.Errorf("ops = %+v; ask must appear like any other op", got.Ops)
	}
	if got.Providers["groq"].CostMicros != 300 || got.Providers["openai"].CostMicros != 700 || got.Providers["openai"].InputTokens != 1000 || len(got.Providers) != 2 {
		t.Errorf("providers = %+v", got.Providers)
	}
	if got.Storage.Recordings != 2 || got.Storage.AudioSeconds != 100 || got.Storage.AudioBytes != 1_123_456 || got.Storage.Notes != 1 || got.Storage.Approximate {
		t.Errorf("storage = %+v", got.Storage)
	}
	// No AWS reading: aws is null, and so there is no share.
	if got.AWS != nil || !strings.Contains(w.Body.String(), `"aws":null`) {
		t.Errorf("aws = %+v, want null", got.AWS)
	}

	// With a reading, the share is aws × 1000 ÷ (1000 + 3000).
	if err := h.usage.PutAWSCost(ctx, usage.AWSCost{Month: "2026-03", MonthMicros: 4_000_000, AsOf: time.Date(2026, 3, 9, 6, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("seed aws cost: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/usage?month=2026-03", "user1", nil)
	decodeInto(t, w, &got)
	if got.AWS == nil || got.AWS.MonthMicros != 4_000_000 || got.AWS.ShareMicros == nil || *got.AWS.ShareMicros != 1_000_000 ||
		got.AWS.ShareBasis == nil || *got.AWS.ShareBasis != "provider_cost" {
		t.Errorf("aws = %+v (body %s)", got.AWS, w.Body.String())
	}
	// A tenant with no provider spend has a zero share; a month the instance
	// spent nothing in has a null one.
	w = h.do(t, http.MethodGet, "/v1/usage?month=2026-03", "user3", nil)
	decodeInto(t, w, &got)
	if got.AWS == nil || got.AWS.ShareMicros == nil || *got.AWS.ShareMicros != 0 {
		t.Errorf("no-spend tenant: aws = %+v", got.AWS)
	}
	if err := h.usage.PutAWSCost(ctx, usage.AWSCost{Month: "2026-02", MonthMicros: 1_000_000, AsOf: time.Date(2026, 2, 9, 6, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("seed aws cost: %v", err)
	}
	w = h.do(t, http.MethodGet, "/v1/usage?month=2026-02", "user1", nil)
	decodeInto(t, w, &got)
	if got.AWS == nil || got.AWS.ShareMicros != nil || got.AWS.ShareBasis != nil || !strings.Contains(w.Body.String(), `"share_micros":null`) {
		t.Errorf("idle month: aws = %+v (body %s)", got.AWS, w.Body.String())
	}

	// Empty month: every member present, empty rather than null.
	w = h.do(t, http.MethodGet, "/v1/usage?month=2020-01", "user1", nil)
	for _, want := range []string{`"providers":{}`, `"api":{"requests":0}`, `"storage":{`, `"approximate":false`, `"days":[]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("empty month lacks %s: %s", want, w.Body.String())
		}
	}
}
