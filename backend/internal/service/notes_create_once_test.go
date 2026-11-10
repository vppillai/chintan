package service

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// staleStore answers the first GetNote as if the note were not there yet.
type staleStore struct {
	repository.Store
	stale bool
}

func (s *staleStore) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if s.stale {
		s.stale = false
		return model.NoteIndex{}, repository.ErrNotFound
	}
	return s.Store.GetNote(ctx, tenantID, noteID)
}

// CreateNoteOnce's body write was unconditional, so a caller whose read of
// the row lost the race blanked a body another recording was already in.
func TestCreateNoteOnceLeavesAnExistingBodyAloneWhenItsReadRaced(t *testing.T) {
	h := newEditHarness(t)
	spec := model.NoteIndex{ID: "note_0000000000000001_aa", Title: "Shopping"}
	first, err := h.notes.CreateNoteOnce(h.ctx, "u1", spec)
	if err != nil {
		t.Fatal(err)
	}
	want := CaptureMarker("c_1") + "\nMilk and eggs."
	if err := h.objects.Put(h.ctx, first.S3MarkdownKey, []byte(want), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	racing := NewNotesService(&staleStore{Store: h.store, stale: true}, h.objects)
	got, err := racing.CreateNoteOnce(h.ctx, "u1", spec)
	if err != nil {
		t.Fatalf("CreateNoteOnce: %v", err)
	}
	if got.ID != first.ID {
		t.Fatalf("got another note")
	}
	if b := h.body(first); b != want {
		t.Fatalf("the losing create wrote over the body: %q, want %q", b, want)
	}
}
