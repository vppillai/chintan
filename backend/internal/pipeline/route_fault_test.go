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
)

// throttleGetNote fails the reads of one note id and passes everything else
// through. A DynamoDB throttle or 5xx looks exactly like this.
type throttleGetNote struct {
	repository.Store
	noteID string

	mu    sync.Mutex
	calls int
}

var errInducedThrottle = errors.New("induced dynamodb throttle")

func (s *throttleGetNote) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if noteID == s.noteID {
		s.mu.Lock()
		s.calls++
		s.mu.Unlock()
		return model.NoteIndex{}, errInducedThrottle
	}
	return s.Store.GetNote(ctx, tenantID, noteID)
}

func (s *throttleGetNote) throttled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// The router said "append to the note the user has been dictating into all
// week". If the read of that note fails transiently, the only safe answer is to
// let SQS redeliver. Treating the fault as "no such note" silently starts a
// second note with the same subject, and the user's week is now split across
// two notes with nothing recording that it happened.
func TestATransientNoteReadFaultDuringRoutingIsRetriedNotRerouted(t *testing.T) {
	f := newRoutingFixture(t, "more about the roof",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "n1",
			Confidence: 0.95,
			Content:    "more about the roof",
		}, false)

	// The same pipeline over a store that throttles reads of n1 only. It is
	// assembled here rather than through newHarnessWrapping so the note creator
	// still writes into the store the pipeline reads.
	throttle := &throttleGetNote{Store: f.store, noteID: "n1"}
	p, err := New(Config{
		Store:       throttle,
		Objects:     f.objects,
		STT:         f.h.stt,
		LLM:         f.h.llm,
		Router:      f.router,
		Notes:       f.h.creator,
		Breaker:     newBreaker(0),
		STTProvider: "groq",
		STTModel:    "whisper-large-v3-turbo",
		LLMProvider: "openai",
		LLMModel:    "test-model",
		Now:         f.h.clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	capture, runErr := p.Run(ctx, "user1", "c_1")

	if throttle.throttled() == 0 {
		t.Fatal("the test never reached the routed note read; it no longer reproduces the defect")
	}
	if titles := f.h.creator.createdTitles(); len(titles) != 0 {
		t.Fatalf("a transient fault created %d new note(s) %v; the dictation was filed away from the note it belongs to",
			len(titles), titles)
	}
	if runErr == nil {
		t.Fatalf("routing swallowed a transient note read fault and returned status %q with note_id %q; "+
			"SQS will never redeliver this capture", capture.Status, capture.NoteID)
	}
	if !errors.Is(runErr, errInducedThrottle) {
		t.Fatalf("error = %v, want the induced throttle to propagate so the message is redelivered", runErr)
	}

	stored, err := f.store.GetCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if stored.NoteID != "" {
		t.Fatalf("capture was bound to note %q despite the read failing", stored.NoteID)
	}
}

// The other half of the same branch, which must keep working: a note the router
// picked that genuinely no longer exists is not a fault, and the dictation
// still has to land somewhere.
func TestRoutingToAMissingNoteStillCreatesOne(t *testing.T) {
	f := newRoutingFixture(t, "a brand new subject",
		provider.RouteDecision{
			Action:     provider.RouteAppend,
			NoteID:     "deleted_note",
			Confidence: 0.95,
			Title:      "A brand new subject",
			Content:    "a brand new subject",
		}, false)

	ctx := context.Background()
	capture, err := f.run(ctx, "c_1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if capture.NoteID == "" || capture.NoteID == "deleted_note" {
		t.Fatalf("note_id = %q, want a freshly created note", capture.NoteID)
	}
	if titles := f.h.creator.createdTitles(); len(titles) != 1 {
		t.Fatalf("created titles = %v, want exactly one new note", titles)
	}
	body, err := f.objects.Get(ctx, "tenants/user1/notes/"+capture.NoteID+"/note.md")
	if err != nil {
		t.Fatalf("read new note body: %v", err)
	}
	if !strings.Contains(string(body), "brand new subject") {
		t.Fatalf("new note body = %q, want the dictation in it", body)
	}
}
