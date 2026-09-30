package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// callCounts tallies the store and object-store calls one capture makes, by
// method name.
type callCounts struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *callCounts) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[name]++
}

func (c *callCounts) reset() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.n
	c.n = map[string]int{}
	return was
}

type countingStore struct {
	repository.Store
	c *callCounts
}

func (s countingStore) GetCapture(ctx context.Context, t, id string) (model.CaptureIndex, error) {
	s.c.add("GetCapture")
	return s.Store.GetCapture(ctx, t, id)
}

func (s countingStore) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	s.c.add("PutCapture")
	return s.Store.PutCapture(ctx, c)
}

func (s countingStore) GetNote(ctx context.Context, t, id string) (model.NoteIndex, error) {
	s.c.add("GetNote")
	return s.Store.GetNote(ctx, t, id)
}

type countingObjects struct {
	repository.Objects
	c *callCounts
}

func (o countingObjects) Get(ctx context.Context, key string) ([]byte, error) {
	o.c.add("Get")
	return o.Objects.Get(ctx, key)
}

func (o countingObjects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	o.c.add("Put")
	return o.Objects.Put(ctx, key, body, contentType)
}

// One capture's AWS calls, counted with the fakes (R7-16a). Before the
// in-memory hand-off a routed recording made 7 PutCapture, 4 GetNote and 4
// object Gets, and a targeted one 5 PutCapture, 4 GetNote (5 once the
// spelling hints read the note too) and 4 Gets: each
// stage re-read the text the stage before it had just written, the
// destination row was read again right after routing or transcription had
// read it, and two stage-done writes were followed at once by the next
// stage's own. The transcript is long enough to be cleaned by the model, so
// the short-dictation tidy (R7-15) does not change the count.
func TestOneCaptureMakesTheFewestStoreAndObjectCalls(t *testing.T) {
	cases := []struct {
		name   string
		noteID string
		want   map[string]int
	}{
		// PutCapture for transcribing, routing, routed, cleaning and
		// appending; GetNote by routing (reused by run), the append stamp and
		// the index refresh; the one object Get is the note body the append
		// merges into.
		{"routed", "", map[string]int{"GetCapture": 1, "PutCapture": 5, "GetNote": 3, "Get": 1, "Put": 4}},
		// Targeted: transcribed is still written, because the instruction
		// strip — a model call — runs before the cleanup's own status write.
		{"targeted", "note1", map[string]int{"GetCapture": 1, "PutCapture": 4, "GetNote": 3, "Get": 1, "Put": 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			counts := &callCounts{n: map[string]int{}}
			objects := countingObjects{Objects: memory.NewObjects(), c: counts}
			h := newHarnessWrapping(t, objects, func(s repository.Store) repository.Store {
				return countingStore{Store: s, c: counts}
			}, harnessOpts{})
			h.stt.Response = handoffTranscript
			h.llm.Response = "The gutter over the back door is leaking again after last night's storm."
			seedNote(t, h.store, h.objects, "note1")
			h.router.Decision = provider.RouteDecision{Action: provider.RouteAppend, NoteID: "note1", Content: handoffTranscript, Confidence: 0.95}
			if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
				ID: "c_1", UserID: "user1", NoteID: tc.noteID, Status: model.StatusUploaded,
				AudioKey: "tenants/user1/captures/c_1/audio.webm", DurationMS: 12_000, CreatedAt: model.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			counts.reset()

			final, err := h.pipeline.Run(ctx, "user1", "c_1")
			if err != nil || final.Status != model.StatusAppended {
				t.Fatalf("Run = %s, %v (%s)", final.Status, err, final.Error)
			}
			got := counts.reset()
			for name, want := range tc.want {
				if got[name] != want {
					t.Errorf("%s = %d, want %d (all: %v)", name, got[name], want, got)
				}
			}
		})
	}
}

// handoffTranscript is long enough for the model cleanup, so these tests
// exercise the write the tidy path shares with it.
const handoffTranscript = "the gutter over the back door is leaking again after the storm last night"

// newHandoffHarness is a routed capture c_1 over a store wrap decorates,
// with note1 as the router's confident answer.
func newHandoffHarness(t *testing.T, wrap func(repository.Store) repository.Store) *harness {
	t.Helper()
	ctx := context.Background()
	h := newHarnessWrapping(t, memory.NewObjects(), wrap, harnessOpts{})
	h.stt.Response = handoffTranscript
	h.llm.Response = "The gutter is leaking again."
	seedNote(t, h.store, h.objects, "note1")
	h.router.Decision = provider.RouteDecision{Action: provider.RouteAppend, NoteID: "note1", Content: handoffTranscript, Confidence: 0.95}
	if err := h.objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", Status: model.StatusUploaded,
		AudioKey: "tenants/user1/captures/c_1/audio.webm", DurationMS: 12_000, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

// failPutOnce fails the first PutCapture that writes status, as a DynamoDB
// fault would, and passes every other write through.
type failPutOnce struct {
	repository.Store
	status model.CaptureStatus
	mu     sync.Mutex
	failed bool
}

func (s *failPutOnce) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	s.mu.Lock()
	fail := !s.failed && c.Status == s.status
	s.failed = s.failed || fail
	s.mu.Unlock()
	if fail {
		return model.CaptureIndex{}, errors.New("injected store fault")
	}
	return s.Store.PutCapture(ctx, c)
}

func noteBody(t *testing.T, h *harness) string {
	t.Helper()
	body, err := h.objects.Get(context.Background(), "tenants/user1/notes/note1/note.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The transcription's and the cleanup's own status writes are folded into
// the next stage's (deferPersist). A fault on that combined write loses what
// a fault on the stage's own write used to: the retry finds no key on the row,
// runs the stage again from the artefact's absence, and the note gets the
// paragraph once.
func TestAFaultOnTheFoldedWriteResumesAtTheStageItRecorded(t *testing.T) {
	cases := []struct {
		name          string
		status        model.CaptureStatus
		stt, cleanups int
	}{
		{"routing write after the transcription", model.StatusRouting, 2, 1},
		{"appending write after the cleanup", model.StatusAppending, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newHandoffHarness(t, func(s repository.Store) repository.Store {
				return &failPutOnce{Store: s, status: tc.status}
			})
			if _, err := h.pipeline.Run(ctx, "user1", "c_1"); err == nil {
				t.Fatal("the first run survived the injected fault")
			}
			final, err := h.pipeline.Run(ctx, "user1", "c_1")
			if err != nil || final.Status != model.StatusAppended {
				t.Fatalf("retry = %s, %v", final.Status, err)
			}
			if got := h.stt.Calls(); got != tc.stt {
				t.Errorf("transcriptions = %d, want %d", got, tc.stt)
			}
			if got := h.llm.Calls(); got != tc.cleanups {
				t.Errorf("cleanups = %d, want %d", got, tc.cleanups)
			}
			if n := strings.Count(noteBody(t, h), "The gutter is leaking again."); n != 1 {
				t.Errorf("paragraph appended %d times, want 1", n)
			}
		})
	}
}

// A delivery that loses the row while a provider call runs concedes at the
// next stage's status write, which is now also the write that would have
// recorded the finished stage: it routes nothing, appends nothing and exits
// cleanly, as it did when the stage's own write was the one it lost.
func TestADeliveryThatLosesTheRowMidStageConcedesAtTheNextWrite(t *testing.T) {
	bump := func(t *testing.T, h *harness) func() {
		return func() {
			ctx := context.Background()
			current, err := h.store.GetCapture(ctx, "user1", "c_1")
			if err != nil {
				t.Errorf("foreign read: %v", err)
				return
			}
			if _, err := h.store.PutCapture(ctx, current); err != nil {
				t.Errorf("foreign write: %v", err)
			}
		}
	}
	t.Run("during the transcription", func(t *testing.T) {
		h := newHandoffHarness(t, func(s repository.Store) repository.Store { return s })
		h.stt.OnCall = bump(t, h)
		final, err := h.pipeline.Run(context.Background(), "user1", "c_1")
		if err != nil {
			t.Fatalf("a conceded delivery failed the invocation: %v", err)
		}
		if final.Status == model.StatusAppended || h.router.CallCount() != 0 || h.llm.Calls() != 0 {
			t.Errorf("status %s, %d routes, %d cleanups after conceding", final.Status, h.router.CallCount(), h.llm.Calls())
		}
	})
	t.Run("during the cleanup", func(t *testing.T) {
		h := newHandoffHarness(t, func(s repository.Store) repository.Store { return s })
		h.llm.OnCall = bump(t, h)
		final, err := h.pipeline.Run(context.Background(), "user1", "c_1")
		if err != nil {
			t.Fatalf("a conceded delivery failed the invocation: %v", err)
		}
		if final.Status == model.StatusAppended || final.AppendToken != "" {
			t.Errorf("status %s, append token %q after conceding", final.Status, final.AppendToken)
		}
		if body := noteBody(t, h); body != "" {
			t.Errorf("a conceded delivery appended: %q", body)
		}
	})
}

// run() checks the destination on the row the language check read before the
// transcription (Pipeline.destination), so a note trashed while Whisper runs
// is only caught by the append stamp's own fresh read. The capture fails with
// the archived verdict and nothing is written into the trashed note.
func TestANoteTrashedDuringTheTranscriptionIsNotAppendedTo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, harnessOpts{})
	h.stt.Response = handoffTranscript
	seedUploadedCapture(t, h, "note1")
	h.stt.OnCall = func() {
		if _, err := service.NewNotesService(h.store, h.objects).ArchiveNote(ctx, "user1", "note1"); err != nil {
			t.Errorf("ArchiveNote: %v", err)
		}
	}
	final, err := h.pipeline.Run(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != model.StatusFailed || final.Error != service.ErrNoteArchived.Error() {
		t.Fatalf("capture = %s %q, want failed with %q", final.Status, final.Error, service.ErrNoteArchived)
	}
	if final.AppendToken != "" || final.AppendedAt != 0 {
		t.Errorf("the append claim was kept: token %q, appended at %d", final.AppendToken, final.AppendedAt)
	}
	if body := noteBody(t, h); body != "" {
		t.Errorf("the trashed note was appended to: %q", body)
	}
}
