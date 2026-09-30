package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// detailCountingStore counts the reads the note-detail path makes.
type detailCountingStore struct {
	repository.Store
	mu                  sync.Mutex
	getNote, listByNote int
	listErr             error
}

func (s *detailCountingStore) GetNote(ctx context.Context, t, id string) (model.NoteIndex, error) {
	s.mu.Lock()
	s.getNote++
	s.mu.Unlock()
	return s.Store.GetNote(ctx, t, id)
}

func (s *detailCountingStore) ListCapturesByNote(ctx context.Context, t, id string, o repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	s.mu.Lock()
	s.listByNote++
	s.mu.Unlock()
	if s.listErr != nil {
		return repository.Page[model.CaptureIndex]{}, s.listErr
	}
	return s.Store.ListCapturesByNote(ctx, t, id, o)
}

// GET /v1/notes/{id} reads the note row once, then the body and the captures
// (R7-16b); ListCapturesForNote's own existence check read the row a second
// time. Another tenant's note is refused on that one read, before either of
// the other two is made.
func TestGetNoteDetailReadsTheRowOnceAndKeepsTenantsApart(t *testing.T) {
	ctx := context.Background()
	store := &detailCountingStore{Store: dynamofake.NewStore()}
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)
	note, err := notes.CreateNote(ctx, "user1", "Roof", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: note.ID, Status: model.StatusAppended, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	store.getNote, store.listByNote = 0, 0

	detail, err := notes.GetNoteDetail(ctx, "user1", note.ID)
	if err != nil {
		t.Fatalf("GetNoteDetail: %v", err)
	}
	if len(detail.Captures.Items) != 1 || detail.Captures.Items[0].ID != "c_1" {
		t.Errorf("captures = %+v, want c_1", detail.Captures.Items)
	}
	if store.getNote != 1 || store.listByNote != 1 {
		t.Errorf("GetNote = %d, ListCapturesByNote = %d, want 1 and 1", store.getNote, store.listByNote)
	}

	store.getNote, store.listByNote = 0, 0
	if _, err := notes.GetNoteDetail(ctx, "user2", note.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("another tenant's note: err = %v, want ErrNotFound", err)
	}
	if store.listByNote != 0 {
		t.Errorf("another tenant's note queried its captures %d times", store.listByNote)
	}
}

type failingBody struct{ repository.Objects }

func (failingBody) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("injected object fault")
}

// Either half of the side-by-side fetch failing fails the read: a note shown
// without its body, or without its recordings, would look like one that has
// none.
func TestGetNoteDetailFailsWhenTheBodyOrTheCapturesCannotBeRead(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(*detailCountingStore, repository.Objects) repository.Objects{
		"body": func(_ *detailCountingStore, o repository.Objects) repository.Objects { return failingBody{o} },
		"captures": func(s *detailCountingStore, o repository.Objects) repository.Objects {
			s.listErr = errors.New("injected store fault")
			return o
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &detailCountingStore{Store: dynamofake.NewStore()}
			objects := repository.Objects(memory.NewObjects())
			note, err := service.NewNotesService(store, objects).CreateNote(ctx, "user1", "Roof", nil)
			if err != nil {
				t.Fatal(err)
			}
			objects = setup(store, objects)
			if _, err := service.NewNotesService(store, objects).GetNoteDetail(ctx, "user1", note.ID); err == nil {
				t.Fatal("GetNoteDetail succeeded over a failed read")
			}
		})
	}
}
