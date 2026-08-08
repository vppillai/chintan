package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

const (
	appendNoteKey  = "tenants/user1/notes/note1/note.md"
	appendCleanKey = "tenants/user1/captures/capture1/clean.txt"
	appendedText   = "the one and only dictated sentence"
)

// appendFixture is a capture parked at `cleaned`, exactly where the v1 defect
// left it: the text is cleaned and ready, and the append has not happened.
type appendFixture struct {
	store   *memory.Store
	objects repository.Objects
	h       *harness
}

// newAppendFixture builds the fixture. wrap, when non-nil, sits between the
// pipeline and the store so a test can induce a failure at a chosen step.
func newAppendFixture(t *testing.T, objects repository.Objects, wrap func(repository.Store) repository.Store) *appendFixture {
	t.Helper()
	ctx := context.Background()

	var h *harness
	if wrap == nil {
		h = newHarness(t, harnessOpts{objects: objects})
	} else {
		h = newHarnessWrapping(t, objects, wrap, harnessOpts{})
	}

	if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
		ID:            "note1",
		Title:         "Destination",
		UpdatedAt:     model.Now(),
		S3MarkdownKey: appendNoteKey,
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID:        "capture1",
		UserID:    "user1",
		NoteID:    "note1",
		Status:    model.StatusCleaned,
		Mode:      model.CleanupFaithful,
		AudioKey:  "tenants/user1/captures/capture1/audio.webm",
		RawKey:    "tenants/user1/captures/capture1/raw.txt",
		CleanKey:  appendCleanKey,
		CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if err := objects.Put(ctx, appendCleanKey, []byte(appendedText), "text/plain"); err != nil {
		t.Fatalf("seed clean text: %v", err)
	}
	if err := objects.Put(ctx, appendNoteKey, []byte(""), "text/markdown"); err != nil {
		t.Fatalf("seed note body: %v", err)
	}

	return &appendFixture{store: h.store, objects: objects, h: h}
}

func (f *appendFixture) run(ctx context.Context) (model.CaptureIndex, error) {
	return f.h.pipeline.Run(ctx, "user1", "capture1")
}

func (f *appendFixture) body(t *testing.T) string {
	t.Helper()
	body, err := f.objects.Get(context.Background(), appendNoteKey)
	if err != nil {
		t.Fatalf("read note body: %v", err)
	}
	return string(body)
}

// failOnceOnPutNote interrupts the pipeline between the append and the status
// flip — the exact window the v1 retry path re-entered and re-appended through.
type failOnceOnPutNote struct {
	repository.Store
	mu     sync.Mutex
	failed bool
}

func (s *failOnceOnPutNote) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	s.mu.Unlock()
	if first {
		return model.NoteIndex{}, errors.New("induced index write failure")
	}
	return s.Store.PutNote(ctx, tenantID, n)
}

func (s *failOnceOnPutNote) didFail() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// This is the defect the audit calls B3, and which handler/captures.go called
// "idempotent". A gateway timeout makes the client retry; without a guard the
// retry appends the same text a second time.
func TestRetryAfterAFailedFinishAppendsExactlyOnce(t *testing.T) {
	var interrupt *failOnceOnPutNote
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		interrupt = &failOnceOnPutNote{Store: s}
		return interrupt
	})
	ctx := context.Background()

	// The index write happens after the text is already in the note body but
	// before the capture is marked appended.
	if _, err := f.run(ctx); err == nil {
		t.Fatal("expected the induced failure to surface")
	}
	if !interrupt.didFail() {
		t.Fatal("the test did not actually interrupt the index write")
	}

	// The text landed, but the capture is not `appended`. This is precisely the
	// state the client retries from.
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("after the interrupted attempt the text appears %d times, want 1", got)
	}
	interrupted, err := f.store.GetCapture(ctx, "user1", "capture1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if interrupted.Status == model.StatusAppended {
		t.Fatal("capture was marked appended despite the failure; the test no longer reproduces the retry window")
	}

	// The retry. It must recognise its own claim and skip the append.
	if _, err := f.run(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("note body contains the dictated text %d times after a retry, want exactly 1:\n%s", got, f.body(t))
	}
}

// A plain second run — a redelivered SQS message, or the frontend polling twice
// — must also be a no-op rather than a second append.
func TestCompletingTwiceAppendsExactlyOnce(t *testing.T) {
	f := newAppendFixture(t, memory.NewObjects(), nil)
	ctx := context.Background()

	first, err := f.run(ctx)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", first.Status)
	}
	if _, err := f.run(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("text appears %d times, want 1:\n%s", got, f.body(t))
	}
}

// Two deliveries racing against one note. Run under -race.
func TestConcurrentCompleteCaptureAppendsExactlyOnce(t *testing.T) {
	f := newAppendFixture(t, memory.NewObjects(), nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.run(ctx)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("completion %d failed: %v", i, err)
		}
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("two concurrent completions wrote the text %d times, want 1:\n%s", got, f.body(t))
	}
}

// A concurrent editor save and voice append must both survive. v1's bare
// read-concat-write dropped one of them.
func TestConcurrentEditAndVoiceAppendBothSurvive(t *testing.T) {
	objects := memory.NewObjects()
	f := newAppendFixture(t, objects, nil)
	ctx := context.Background()

	const editorText = "a paragraph typed in the editor"
	if err := objects.Put(ctx, appendNoteKey, []byte(editorText), "text/markdown"); err != nil {
		t.Fatalf("seed editor text: %v", err)
	}

	if _, err := f.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	body := f.body(t)
	if !strings.Contains(body, editorText) {
		t.Fatalf("the editor's text was discarded by the voice append:\n%s", body)
	}
	if !strings.Contains(body, appendedText) {
		t.Fatalf("the voice append is missing:\n%s", body)
	}
}

// appendToNote must retry rather than overwrite when it loses the ETag race.
func TestAppendToNoteRetriesOnAConcurrentWrite(t *testing.T) {
	objects := &raceOnceObjects{Objects: memory.NewObjects(), key: appendNoteKey}
	f := newAppendFixture(t, objects, nil)
	ctx := context.Background()

	if _, err := f.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !objects.didRace() {
		t.Fatal("the test did not actually induce a lost ETag race")
	}

	body := f.body(t)
	if !strings.Contains(body, "written by somebody else") {
		t.Fatalf("the concurrent writer's content was lost:\n%s", body)
	}
	if got := strings.Count(body, appendedText); got != 1 {
		t.Fatalf("appended text appears %d times, want 1:\n%s", got, body)
	}
}

// raceOnceObjects slips a foreign write in between the first GetWithETag and
// its PutIfMatch, so the conditional write fails once.
type raceOnceObjects struct {
	repository.Objects
	key   string
	mu    sync.Mutex
	raced bool
}

func (o *raceOnceObjects) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	body, etag, err := o.Objects.GetWithETag(ctx, key)
	o.mu.Lock()
	race := err == nil && key == o.key && !o.raced
	if race {
		o.raced = true
	}
	o.mu.Unlock()
	if race {
		_ = o.Objects.Put(ctx, key, []byte("written by somebody else"), "text/markdown")
	}
	return body, etag, err
}

func (o *raceOnceObjects) didRace() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.raced
}

// Note recency decides which fifty notes the router is shown. v1 compared
// RFC3339Nano strings, and Go trims trailing fractional zeros, so a whole
// second sorted above a fraction of it because 'Z' > '.'.
func TestNoteRecencyOrderingIsChronological(t *testing.T) {
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	older := model.FormatTime(base)                             // exactly on the second
	newer := model.FormatTime(base.Add(100 * time.Millisecond)) // a tenth later

	if !(older < newer) {
		t.Fatalf("stored form does not sort chronologically: %q should sort below %q", older, newer)
	}
	// The v1 layout got this backwards, which is the bug being pinned.
	if legacyOlder, legacyNewer := base.Format(time.RFC3339Nano), base.Add(100*time.Millisecond).Format(time.RFC3339Nano); legacyOlder < legacyNewer {
		t.Fatalf("RFC3339Nano unexpectedly sorted correctly (%q < %q); the fixed-width layout is no longer motivated", legacyOlder, legacyNewer)
	}

	notes := []model.NoteIndex{{ID: "older", UpdatedAt: older}, {ID: "newer", UpdatedAt: newer}}
	if !noteTouchedAt(notes[1]).After(noteTouchedAt(notes[0])) {
		t.Fatal("noteTouchedAt does not order the newer note after the older one")
	}
}

// The router must be handed the most recently touched notes, whatever order the
// store returns them in.
func TestRouterSeesTheMostRecentlyTouchedNotes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOpts{router: &fake.Router{}})

	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	// note_a is a whole second; note_b is a fraction later. Under string
	// comparison of RFC3339Nano, note_a wrongly wins.
	for id, at := range map[string]time.Time{
		"note_a": base,
		"note_b": base.Add(100 * time.Millisecond),
	} {
		if _, err := h.store.PutNote(ctx, "user1", model.NoteIndex{
			ID: id, Title: id, UpdatedAt: model.FormatTime(at),
			S3MarkdownKey: fmt.Sprintf("tenants/user1/notes/%s/note.md", id),
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	if _, err := h.pipeline.decideTarget(ctx, "user1", "some words"); err != nil {
		t.Fatalf("decideTarget: %v", err)
	}
	if len(h.router.LastCandidates) != 2 {
		t.Fatalf("router saw %d candidates, want 2", len(h.router.LastCandidates))
	}
	if h.router.LastCandidates[0].NoteID != "note_b" {
		t.Fatalf("router's first candidate = %s, want the most recently touched note_b",
			h.router.LastCandidates[0].NoteID)
	}
}
