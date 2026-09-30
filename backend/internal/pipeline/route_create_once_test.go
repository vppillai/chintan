package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// failAfterNoteCreate fails the first store write after a note row is
// created, once: the worker dying between making the note and saying so.
type failAfterNoteCreate struct {
	repository.Store
	mu           sync.Mutex
	armed, fired bool
}

var errInducedCrash = errors.New("induced crash after the note was created")

func (s *failAfterNoteCreate) trip() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed && !s.fired {
		s.fired = true
		return errInducedCrash
	}
	return nil
}

func (s *failAfterNoteCreate) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	if err := s.trip(); err != nil {
		return model.NoteIndex{}, err
	}
	out, err := s.Store.PutNote(ctx, tenantID, n)
	if err == nil && n.Version == 0 {
		s.mu.Lock()
		s.armed = true
		s.mu.Unlock()
	}
	return out, err
}

func (s *failAfterNoteCreate) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	if err := s.trip(); err != nil {
		return model.CaptureIndex{}, err
	}
	return s.Store.PutCapture(ctx, c)
}

// R7-21: a routed capture that starts a checklist, and a worker that dies
// right after the note is created. The retry asks the model again, which
// may title the note differently; it must still end with one note, a
// checklist, holding the item. Before the fix the create and the kind were
// two writes under a random id, so the retry either made a second note or
// filed the item as prose into a plain note titled like a list.
func TestARoutedCreateInterruptedByACrashLeavesOneNoteOfTheRightKind(t *testing.T) {
	ctx := context.Background()
	base := dynamofake.NewStore()
	objects := memory.NewObjects()
	store := &failAfterNoteCreate{Store: base}
	router := &fake.Router{Decision: provider.RouteDecision{Action: provider.RouteNew, Title: "Groceries list", Checklist: true, Confidence: 0.9}}
	p, err := New(Config{
		Store:       store,
		Objects:     objects,
		STT:         &fake.STT{Response: "add milk to my groceries list"},
		LLM:         &fake.LLM{ItemsResponse: []cleanup.Item{{Text: "Milk"}}},
		Router:      router,
		Notes:       service.NewNotesService(store, objects),
		Breaker:     newBreaker(0),
		STTProvider: "groq",
		STTModel:    "whisper-large-v3-turbo",
		LLMProvider: "openai",
		LLMModel:    "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", Status: model.StatusUploaded,
		AudioKey: "tenants/user1/captures/c_1/audio.webm", CreatedAt: model.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Run(ctx, "user1", "c_1"); !errors.Is(err, errInducedCrash) {
		t.Fatalf("first run = %v, want the induced crash", err)
	}
	router.Decision.Title = "Shopping for the week"
	capture, err := p.Run(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	notes, _, err := base.DrainNotes(ctx, "user1", repository.DrainOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("%d notes after the retry, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Kind != model.NoteKindChecklist || capture.NoteID != n.ID || capture.Status != model.StatusAppended {
		t.Fatalf("note kind %q id %q, capture %s in %q; want the capture appended into the one checklist", n.Kind, n.ID, capture.Status, capture.NoteID)
	}
	body, err := objects.Get(ctx, n.S3MarkdownKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(body), "\n- [ ] Milk") || strings.Count(string(body), "Milk") != 1 {
		t.Errorf("body = %q, want the one item", body)
	}
}
