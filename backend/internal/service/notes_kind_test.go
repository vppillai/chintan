package service

import (
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// Kind is stored as sent — "" or checklist — and anything else is refused
// here as well as at the handler, the way language is.
func TestUpdateNoteStoresTheKindAndRefusesAnUnknownOne(t *testing.T) {
	h := newEditHarness(t)
	n := h.note("u", "Packing", "passport\n\ncharger")

	version := n.Version
	kind := model.NoteKindChecklist
	body := "- [ ] passport\n- [ ] charger"
	updated, err := h.notes.UpdateNote(h.ctx, "u", n.ID, NoteUpdates{Kind: &kind, Body: &body, ExpectedVersion: &version})
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if updated.Kind != model.NoteKindChecklist {
		t.Errorf("kind = %q, want checklist", updated.Kind)
	}
	if got := h.body(updated); got != body {
		t.Errorf("the body was converted by the server: %q", got)
	}

	version = updated.Version
	bad := "todo"
	if _, err := h.notes.UpdateNote(h.ctx, "u", n.ID, NoteUpdates{Kind: &bad, ExpectedVersion: &version}); !errors.Is(err, ErrInvalidNoteKind) {
		t.Errorf("UpdateNote(kind=todo) = %v, want ErrInvalidNoteKind", err)
	}
	if h.get("u", n.ID).Kind != model.NoteKindChecklist {
		t.Error("a refused kind was stored")
	}
}
