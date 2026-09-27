package service

import (
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// The mode a clean runs in is the note's own or the default; a "tasks" value
// left on a row from before 2026-09-27 is not a mode and runs as the default.
func TestEffectiveCleanModeIsTheNotesOwnOrTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		note model.NoteIndex
		want model.NoteCleanMode
	}{
		{"note's own", model.NoteIndex{CleanMode: model.NoteCleanPolished}, model.NoteCleanPolished},
		{"none stored", model.NoteIndex{}, model.DefaultNoteCleanMode},
		{"a stale tasks preference runs the default", model.NoteIndex{CleanMode: "tasks"}, model.DefaultNoteCleanMode},
	} {
		if got := EffectiveCleanMode(tc.note); got != tc.want {
			t.Errorf("%s: EffectiveCleanMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A checklist has no cleaned view, whatever mode is asked; a plain note
// takes polished or structured and nothing else.
func TestCheckCleanModeRefusesAChecklistAndUnknownModes(t *testing.T) {
	checklist := model.NoteIndex{Kind: model.NoteKindChecklist}
	plain := model.NoteIndex{}
	for _, tc := range []struct {
		name string
		note model.NoteIndex
		mode model.NoteCleanMode
		want error
	}{
		{"checklist structured", checklist, model.NoteCleanStructured, ErrChecklistCleanMode},
		{"checklist polished", checklist, model.NoteCleanPolished, ErrChecklistCleanMode},
		{"checklist tasks", checklist, "tasks", ErrChecklistCleanMode},
		{"plain polished", plain, model.NoteCleanPolished, nil},
		{"plain structured", plain, model.NoteCleanStructured, nil},
		{"plain tasks", plain, "tasks", ErrInvalidNoteCleanMode},
		{"plain unknown", plain, "faithful", ErrInvalidNoteCleanMode},
	} {
		if got := CheckCleanMode(tc.note, tc.mode); !errors.Is(got, tc.want) {
			t.Errorf("%s: CheckCleanMode = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// RequestClean on a checklist is refused before the row is stamped or the
// worker invoked, in every mode and with none.
func TestRequestCleanOnAChecklistIsRefused(t *testing.T) {
	h := newEditHarness(t)
	worker := &stubInvoker{}
	h.notes.WithInvoker(worker)
	n := h.checklist("u", "Packing", "- [ ] passport")

	for _, mode := range []model.NoteCleanMode{"", model.NoteCleanStructured, "tasks"} {
		if _, err := h.notes.RequestClean(h.ctx, "u", n.ID, mode); !errors.Is(err, ErrChecklistCleanMode) {
			t.Errorf("RequestClean(%q) on a checklist = %v, want ErrChecklistCleanMode", mode, err)
		}
	}
	if len(worker.calls) != 0 {
		t.Errorf("a refused clean reached the worker: %v", worker.calls)
	}
	if after := h.get("u", n.ID); after.CleanedRequestedMode != "" || after.CleanedRequestedAt != "" {
		t.Errorf("the row was stamped for a refused clean: %+v", after)
	}
}
