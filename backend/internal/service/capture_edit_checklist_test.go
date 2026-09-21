package service

import (
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// A checklist body is one item per line with the worker's marker on the line
// before each recorded item. Deleting and moving a recording cut along the
// marker exactly as they do for a paragraph, so what leaves the body is that
// recording's item and nothing else — including a typed item with no marker
// and an item the person has since ticked.

func (h *editHarness) checklist(userID, title, body string) model.NoteIndex {
	h.t.Helper()
	n := h.note(userID, title, body)
	n.Kind = model.NoteKindChecklist
	stored, err := h.store.PutNote(h.ctx, userID, n)
	if err != nil {
		h.t.Fatalf("PutNote: %v", err)
	}
	return stored
}

func TestDeleteCaptureFromAChecklistRemovesExactlyItsItem(t *testing.T) {
	h := newEditHarness(t)
	body := "- [ ] typed first\n\n" + CaptureMarker("c_1") + "\n- [x] passport\n\n" + CaptureMarker("c_2") + "\n- [ ] charger"
	note := h.checklist("u1", "Packing", body)
	h.appended("u1", note.ID, "c_1", t1000)
	h.appended("u1", note.ID, "c_2", t1100)

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture(c_1): %v", err)
	}
	if got, want := h.body(note), "- [ ] typed first\n\n"+CaptureMarker("c_2")+"\n- [ ] charger"; got != want {
		t.Errorf("after deleting the ticked item: %q, want %q", got, want)
	}
	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_2"); err != nil {
		t.Fatalf("DeleteCapture(c_2): %v", err)
	}
	if got := h.body(note); got != "- [ ] typed first" {
		t.Errorf("after deleting both recordings: %q, want the typed item alone", got)
	}
	if after := h.get("u1", note.ID); after.Kind != model.NoteKindChecklist {
		t.Errorf("the index refresh lost the kind: %q", after.Kind)
	}
}

func TestMoveCaptureBetweenChecklistsMovesExactlyItsItem(t *testing.T) {
	h := newEditHarness(t)
	source := h.checklist("u1", "Packing", CaptureMarker("c_1")+"\n- [ ] passport\n\n"+CaptureMarker("c_2")+"\n- [x] charger")
	target := h.checklist("u1", "Groceries", "- [ ] milk\n\n"+CaptureMarker("g_1")+"\n- [ ] eggs")
	h.appended("u1", source.ID, "c_1", t1000)
	h.appended("u1", source.ID, "c_2", t1200)
	h.appended("u1", target.ID, "g_1", t0900)

	if _, moved, err := h.captures.MoveCapture(h.ctx, "u1", "c_2", target.ID); err != nil || !moved {
		t.Fatalf("MoveCapture = (%v, %v)", moved, err)
	}
	if got, want := h.body(source), CaptureMarker("c_1")+"\n- [ ] passport"; got != want {
		t.Errorf("source = %q, want %q", got, want)
	}
	// The ticked item lands after the older recording, still ticked, still one
	// line; the target's typed item is untouched.
	wantTarget := "- [ ] milk\n\n" + CaptureMarker("g_1") + "\n- [ ] eggs\n\n" + CaptureMarker("c_2") + "\n- [x] charger"
	if got := h.body(target); got != wantTarget {
		t.Errorf("target = %q, want %q", got, wantTarget)
	}
	if got := StripCaptureMarkers(h.body(target)); got != "- [ ] milk\n\n- [ ] eggs\n\n- [x] charger" {
		t.Errorf("the person sees %q", got)
	}
}
