package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// askTask is the payload the API sends.
func askTask(tenantID, askID string) json.RawMessage {
	raw, err := json.Marshal(Invocation{
		Task: TaskAsk, TenantID: tenantID, AskID: askID,
		CorrelationID: "00000000-0000-4000-8000-000000000002",
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// seedAsk writes a pending question the way service.AskService.Begin does.
func seedAsk(t *testing.T, h *harness, askID, question string, history []model.AskTurn) {
	t.Helper()
	err := h.store.PutAsk(context.Background(), "user1", model.Ask{
		ID: askID, UserID: "user1", Status: model.AskPending, Question: question, History: history,
		CreatedAt: model.Now(), ExpiresAt: time.Now().Add(model.AskTTL).Unix(),
	})
	if err != nil {
		t.Fatalf("seed ask: %v", err)
	}
}

// seedSearchableNote is seedNoteWithBody plus the search text the ranker
// reads, which the API and the worker write on every body write.
func seedSearchableNote(t *testing.T, h *harness, noteID, title, body string, updatedAt string) model.NoteIndex {
	t.Helper()
	return seedNoteWithBody(t, h, noteID, body, func(n *model.NoteIndex) {
		n.Title = title
		n.SearchText = service.SearchText(body)
		if updatedAt != "" {
			n.UpdatedAt = updatedAt
		}
	})
}

func getAsk(t *testing.T, h *harness, askID string) model.Ask {
	t.Helper()
	a, err := h.store.GetAsk(context.Background(), "user1", askID)
	if err != nil {
		t.Fatalf("GetAsk: %v", err)
	}
	return a
}

// The task end to end: the notes that match are read, packed and fenced, the
// model is called once through the breaker, and the row carries the answer
// with only the packed-and-cited notes as sources.
func TestAskTaskAnswersFromThePackedNotesWithSources(t *testing.T) {
	llmFake := &fake.LLM{Answer: &provider.Answer{
		Text:     "You decided to replace the whole roof; the roofer comes on the 14th.",
		Sources:  []string{"roof", "made-up-id"},
		Grounded: true,
	}}
	counter := newMemCounter()
	h := newHarness(t, harnessOpts{llm: llmFake, counter: counter})
	seedSearchableNote(t, h, "roof", "Roof repairs", service.CaptureMarker("c_1")+"\nreplace the whole roof, the roofer comes on the fourteenth", "")
	seedSearchableNote(t, h, "garden", "Garden", "plant the bulbs in october", "")
	seedSearchableNote(t, h, "car", "Car", "service due in november", "")
	seedAsk(t, h, "a1", "what did I decide about the roof?", nil)

	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	calls := llmFake.AskCalls()
	if len(calls) != 1 {
		t.Fatalf("the model was called %d times, want exactly once", len(calls))
	}
	prompt := calls[0]
	if prompt.Question != "what did I decide about the roof?" || prompt.Today == "" {
		t.Errorf("prompt = %+v", prompt)
	}
	if len(prompt.Notes) == 0 || prompt.Notes[0].NoteID != "roof" {
		t.Fatalf("the roof note is not the first packed note: %+v", prompt.Notes)
	}
	for _, n := range prompt.Notes {
		if strings.Contains(n.Text, "<!-- chintan:capture:") {
			t.Errorf("the append markers reached the model: %q", n.Text)
		}
	}
	if counter.total() <= 0 {
		t.Error("the call was not reserved against the day's spend counter; every provider call goes through the breaker")
	}

	a := getAsk(t, h, "a1")
	if a.Status != model.AskAnswered || a.Answer == "" || !a.Grounded || a.Error != "" || a.AnsweredAt == "" {
		t.Errorf("row = %+v", a)
	}
	if a.NotesConsidered != 3 {
		t.Errorf("notes_considered = %d, want 3", a.NotesConsidered)
	}
	if len(a.Sources) != 1 || a.Sources[0].NoteID != "roof" || a.Sources[0].Title != "Roof repairs" {
		t.Errorf("sources = %+v; an id the model made up must be dropped", a.Sources)
	}
}

// Only one strong match: the recency fill adds the newest notes so the model
// has something to read, and the fill is capped.
func TestAskTaskFillsWithRecentNotesWhenFewScore(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seedSearchableNote(t, h, "roof", "Roof", "the roof", model.FormatTime(base))
	for i := 0; i < 10; i++ {
		id := "n" + string(rune('a'+i))
		seedSearchableNote(t, h, id, "Note "+id, "nothing relevant here", model.FormatTime(base.Add(time.Duration(i+1)*time.Hour)))
	}
	seedAsk(t, h, "a1", "roof", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	prompt := llmFake.AskCalls()[0]
	if len(prompt.Notes) != ask.RecentFillNotes {
		t.Fatalf("packed %d notes, want the fill of %d", len(prompt.Notes), ask.RecentFillNotes)
	}
	if prompt.Notes[0].NoteID != "roof" || prompt.Notes[1].NoteID != "nj" {
		t.Errorf("order = %s, %s; the match first, then the newest", prompt.Notes[0].NoteID, prompt.Notes[1].NoteID)
	}
}

// The honest "not in your notes": grounded false, the model's sentence kept,
// no sources.
func TestAskTaskRecordsAnUngroundedAnswer(t *testing.T) {
	llmFake := &fake.LLM{Answer: &provider.Answer{Text: "That is not in your notes.", Grounded: false}}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "garden", "Garden", "plant the bulbs", "")
	seedAsk(t, h, "a1", "what is the capital of peru?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	a := getAsk(t, h, "a1")
	if a.Status != model.AskAnswered || a.Grounded || a.Answer != "That is not in your notes." || len(a.Sources) != 0 {
		t.Errorf("row = %+v", a)
	}
	if a.Sources == nil {
		t.Error("sources is nil on the row; the wire needs []")
	}
}

// A model that says "grounded" but cites nothing that was packed is not
// grounded: a source is a note the person can open.
func TestAskTaskDropsUnpackedSourcesAndUngroundsAnAnswerWithNone(t *testing.T) {
	llmFake := &fake.LLM{Answer: &provider.Answer{Text: "Yes.", Sources: []string{"never-packed"}, Grounded: true}}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "garden", "Garden", "plant the bulbs", "")
	seedAsk(t, h, "a1", "bulbs?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	a := getAsk(t, h, "a1")
	if a.Grounded || len(a.Sources) != 0 {
		t.Errorf("row = %+v; an answer citing nothing that was packed is not grounded", a)
	}
}

// No notes at all is an answer, not a failure, and the model is never called.
func TestAskTaskWithNoNotesAnswersThatThereIsNothingToSearch(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedAsk(t, h, "a1", "anything?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(llmFake.AskCalls()) != 0 {
		t.Error("the model was called for a tenant with no notes")
	}
	a := getAsk(t, h, "a1")
	if a.Status != model.AskAnswered || a.Grounded || a.Answer != ask.NoNotesAnswer || a.NotesConsidered != 0 {
		t.Errorf("row = %+v", a)
	}
}

// The spend cap refuses the call before the provider is contacted, and the row
// says so in words the user can read.
func TestAskTaskIsStoppedByTheSpendCapBeforeTheModelIsCalled(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake, capMicros: 1})
	seedSearchableNote(t, h, "roof", "Roof", strings.Repeat("the roof leaks. ", 300), "")
	seedAsk(t, h, "a1", "roof?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(llmFake.AskCalls()) != 0 {
		t.Fatal("the model was called past the cap; Do must own the call")
	}
	a := getAsk(t, h, "a1")
	if a.Status != model.AskFailed || a.Error != askSpendCapped || a.Answer != "" {
		t.Errorf("row = %+v", a)
	}
}

// A provider failure is a verdict on the row, not a retry of the invocation.
// A 5xx is tried once more; a 4xx is not.
func TestAskTaskRecordsAProviderFailureAndRetriesOnlyServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   string
		wantOK    bool
	}{
		{"5xx then success", []error{&provider.StatusError{Op: "llm request failed", StatusCode: 529}, nil}, 2, "", true},
		{"5xx twice", []error{&provider.StatusError{Op: "llm request failed", StatusCode: 503}, &provider.StatusError{Op: "llm request failed", StatusCode: 503}}, 2, askProviderFail, false},
		{"4xx is not retried", []error{&provider.StatusError{Op: "llm request failed", StatusCode: 400}}, 1, askProviderFail, false},
		{"revoked key names itself", []error{&provider.StatusError{Op: "llm request failed", StatusCode: 401}}, 1, ErrProviderKeyRejected.Error(), false},
		{"nothing usable", []error{ask.ErrNoAnswer}, 1, askProviderFail, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llmFake := &fake.LLM{AskErrs: tc.errs}
			h := newHarness(t, harnessOpts{llm: llmFake})
			seedSearchableNote(t, h, "roof", "Roof", "the roof leaks", "")
			seedAsk(t, h, "a1", "roof?", nil)
			if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
				t.Fatalf("Handle returned %v; a provider failure is recorded, not retried", err)
			}
			if got := len(llmFake.AskCalls()); got != tc.wantCalls {
				t.Errorf("model calls = %d, want %d", got, tc.wantCalls)
			}
			a := getAsk(t, h, "a1")
			if tc.wantOK {
				if a.Status != model.AskAnswered || a.Error != "" {
					t.Errorf("row = %+v, want answered", a)
				}
				return
			}
			if a.Status != model.AskFailed || a.Error != tc.wantErr || a.Answer != "" || len(a.Sources) != 0 {
				t.Errorf("row = %+v, want failed with %q", a, tc.wantErr)
			}
		})
	}
}

// A stalled provider is cut off at the attempt timeout and asked once more.
func TestAskTaskRetriesOnceAfterATimeout(t *testing.T) {
	llmFake := &fake.LLM{AskHang: 1}
	h := newHarness(t, harnessOpts{llm: llmFake})
	h.pipeline.cfg.AskAttemptTimeout = 20 * time.Millisecond
	seedSearchableNote(t, h, "roof", "Roof", "the roof leaks", "")
	seedAsk(t, h, "a1", "roof?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := len(llmFake.AskCalls()); got != 2 {
		t.Errorf("model calls = %d, want the stalled one and the retry", got)
	}
	if a := getAsk(t, h, "a1"); a.Status != model.AskAnswered {
		t.Errorf("row = %+v, want answered by the retry", a)
	}
}

// An answer over the cap is refused whole, never cut.
func TestAskTaskRefusesAnOverlongAnswerWhole(t *testing.T) {
	llmFake := &fake.LLM{Answer: &provider.Answer{Text: strings.Repeat("y", ask.MaxAnswerRunes+1), Grounded: true}}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "roof", "Roof", "the roof leaks", "")
	seedAsk(t, h, "a1", "roof?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	a := getAsk(t, h, "a1")
	if a.Status != model.AskFailed || a.Error != askTooLong || a.Answer != "" {
		t.Errorf("row = %+v", a)
	}
}

// A note purged between the list and the body read is one candidate fewer,
// not a fault: the answer comes from the notes that are still there.
func TestAskTaskSkipsANotePurgedMidRun(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "roof", "Roof", "the roof leaks", "")
	gone := seedSearchableNote(t, h, "gone", "Roof gutter", "the roof gutter too", "")
	if err := h.objects.Delete(context.Background(), gone.S3MarkdownKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	seedAsk(t, h, "a1", "roof?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	prompt := llmFake.AskCalls()[0]
	if len(prompt.Notes) != 1 || prompt.Notes[0].NoteID != "roof" {
		t.Errorf("packed = %+v; the note without a body must be skipped", prompt.Notes)
	}
	a := getAsk(t, h, "a1")
	if a.Status != model.AskAnswered || a.NotesConsidered != 2 {
		t.Errorf("row = %+v", a)
	}
}

// The earlier turns reach the prompt as context; retrieval ignores them.
func TestAskTaskIncludesTheHistoryInThePrompt(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "roof", "Roof", "the roofer comes on the fourteenth", "")
	seedSearchableNote(t, h, "garden", "Garden", "bulbs in october", "")
	history := []model.AskTurn{{Question: "what about the garden?", Answer: "Bulbs go in in October."}}
	seedAsk(t, h, "a1", "and the roof?", history)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	prompt := llmFake.AskCalls()[0]
	if len(prompt.History) != 1 || prompt.History[0].Answer != "Bulbs go in in October." {
		t.Errorf("history = %+v", prompt.History)
	}
	_, user, err := prompt.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(user, "Q: what about the garden?\nA: Bulbs go in in October.") {
		t.Errorf("the history is not in the rendered prompt:\n%s", user)
	}
	if prompt.Notes[0].NoteID != "roof" {
		t.Errorf("the question, not the history, must drive retrieval: first packed = %s", prompt.Notes[0].NoteID)
	}
}

// A row that is gone, or already answered, is nothing to do — and nothing to
// retry. A task that names no question is discarded.
func TestAskTaskOnAMissingOrFinishedRowIsDone(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "roof", "Roof", "the roof leaks", "")
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "expired")); err != nil {
		t.Errorf("Handle(missing) = %v, want nil", err)
	}
	seedAsk(t, h, "done", "roof?", nil)
	done := getAsk(t, h, "done")
	done.Status = model.AskAnswered
	done.Answer = "already"
	if err := h.store.PutAsk(context.Background(), "user1", done); err != nil {
		t.Fatalf("PutAsk: %v", err)
	}
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "done")); err != nil {
		t.Errorf("Handle(answered) = %v, want nil", err)
	}
	if len(llmFake.AskCalls()) != 0 {
		t.Error("the model was called for a question that cannot be answered again")
	}
	if got := getAsk(t, h, "done").Answer; got != "already" {
		t.Errorf("a second delivery rewrote the answer: %q", got)
	}
	if err := NewWorker(h.pipeline).Handle(context.Background(), json.RawMessage(`{"task":"ask","tenant_id":"user1"}`)); err != nil {
		t.Errorf("Handle(no ask id) = %v, want nil", err)
	}
}

// The invoker's ask payload is the one the worker dispatches on.
func TestInvokerSendsAnAskTaskTheWorkerAccepts(t *testing.T) {
	client := &capturingLambda{}
	inv := NewInvoker(client, "arn:aws:lambda:us-west-2:123456789012:function:chintan-worker-dev-prod:live")
	if err := inv.InvokeAsk(context.Background(), "user1", "ask_1"); err != nil {
		t.Fatalf("InvokeAsk: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(client.in.Payload, &sent); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for k, want := range map[string]string{"task": TaskAsk, "tenant_id": "user1", "ask_id": "ask_1"} {
		if sent[k] != want {
			t.Errorf("payload[%s] = %v, want %q", k, sent[k], want)
		}
	}
	task, ok := parseTask(client.in.Payload)
	if !ok || task.Task != TaskAsk || task.AskID != "ask_1" {
		t.Errorf("the worker does not read back what the invoker sent: %+v ok=%v", task, ok)
	}
	if _, isClean := parseCleanNoteTask(client.in.Payload); isClean {
		t.Error("an ask payload was read as a clean-note task")
	}
}

// clientAskPollWindow is how long the app polls an ask row before it tells the
// person the question did not reach the server (frontend/src/api/queries.ts
// ASK_POLL_TIMEOUT_MS). Go cannot read that file, so the number is written
// down here.
const clientAskPollWindow = 60 * time.Second

// The worker's whole Ask budget — both model attempts plus a cold start, the
// list and body reads and the poll cadence — has to fit inside the client's
// window, or an answer still being written is shown as not arriving and "Try
// again" bills the question twice. Twenty seconds is the allowance for
// everything that is not the model.
func TestAskAttemptBudgetFitsInsideTheClientsPollWindow(t *testing.T) {
	p, err := New(Config{Store: memory.NewStore(), Objects: memory.NewObjects(), STT: &fake.STT{}, LLM: &fake.LLM{}, Router: &fake.Router{},
		Breaker: newBreaker(0), STTProvider: "groq", STTModel: "whisper-large-v3-turbo", LLMProvider: "openai", LLMModel: "test-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var model time.Duration
	for attempt := 1; attempt <= askAttempts; attempt++ {
		model += p.askAttemptTimeout(attempt)
	}
	if allowance := clientAskPollWindow - model; allowance < 20*time.Second {
		t.Fatalf("the model attempts take %s of the client's %s; %s is left for a cold start, the reads and the poll cadence, want at least 20s",
			model, clientAskPollWindow, allowance)
	}
	if p.askAttemptTimeout(2) >= p.askAttemptTimeout(1) {
		t.Errorf("retry timeout %s is not shorter than the first attempt's %s", p.askAttemptTimeout(2), p.askAttemptTimeout(1))
	}
}

// A note the app saved from an earlier Ask thread is not a source for a later
// question, however well its words match: the desktop QA run cited the mobile
// run's saved thread as its first source.
func TestAskTaskDoesNotReadNotesSavedFromAnAskThread(t *testing.T) {
	llmFake := &fake.LLM{}
	h := newHarness(t, harnessOpts{llm: llmFake})
	seedSearchableNote(t, h, "roof", "Roof", "the roof leaks near the downpipe", "")
	seedNoteWithBody(t, h, "saved", "**Q: what is leaking?** The roof leaks near the downpipe. roof roof roof", func(n *model.NoteIndex) {
		n.Title = "What is leaking?"
		n.Tags = []string{ask.SavedAnswerTag}
		n.SearchText = service.SearchText("the roof leaks near the downpipe. roof roof roof")
	})
	seedAsk(t, h, "a1", "where does the roof leak?", nil)
	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	prompt := llmFake.AskCalls()[0]
	for _, n := range prompt.Notes {
		if n.NoteID == "saved" {
			t.Fatalf("the saved answer was packed into the prompt: %+v", prompt.Notes)
		}
	}
	if a := getAsk(t, h, "a1"); a.NotesConsidered != 1 {
		t.Errorf("notes_considered = %d, want 1: the saved answer is not a candidate", a.NotesConsidered)
	}
}
