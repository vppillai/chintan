package service

import (
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

func TestEffectiveCleanModeFollowsTheKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		note model.NoteIndex
		want model.NoteCleanMode
	}{
		{"checklist ignores a stored preference", model.NoteIndex{Kind: model.NoteKindChecklist, CleanMode: model.NoteCleanPolished}, model.NoteCleanTasks},
		{"checklist with none", model.NoteIndex{Kind: model.NoteKindChecklist}, model.NoteCleanTasks},
		{"plain note's own", model.NoteIndex{CleanMode: model.NoteCleanPolished}, model.NoteCleanPolished},
		{"plain note default", model.NoteIndex{}, model.DefaultNoteCleanMode},
		{"plain note left with tasks runs the default", model.NoteIndex{CleanMode: model.NoteCleanTasks}, model.DefaultNoteCleanMode},
	} {
		if got := EffectiveCleanMode(tc.note); got != tc.want {
			t.Errorf("%s: EffectiveCleanMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCheckCleanModeHoldsEachKindToItsModes(t *testing.T) {
	checklist := model.NoteIndex{Kind: model.NoteKindChecklist}
	plain := model.NoteIndex{}
	for _, tc := range []struct {
		name string
		note model.NoteIndex
		mode model.NoteCleanMode
		want error
	}{
		{"checklist tasks", checklist, model.NoteCleanTasks, nil},
		{"checklist structured", checklist, model.NoteCleanStructured, ErrChecklistCleanMode},
		{"checklist polished", checklist, model.NoteCleanPolished, ErrChecklistCleanMode},
		{"checklist unknown", checklist, "faithful", ErrChecklistCleanMode},
		{"plain polished", plain, model.NoteCleanPolished, nil},
		{"plain structured", plain, model.NoteCleanStructured, nil},
		{"plain tasks", plain, model.NoteCleanTasks, ErrInvalidNoteCleanMode},
		{"plain unknown", plain, "faithful", ErrInvalidNoteCleanMode},
	} {
		if got := CheckCleanMode(tc.note, tc.mode); !errors.Is(got, tc.want) {
			t.Errorf("%s: CheckCleanMode = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// RequestClean on a checklist hands tasks to the worker when no mode is
// named and refuses any other mode before touching the row.
func TestRequestCleanOnAChecklistIsTasksOnly(t *testing.T) {
	h := newEditHarness(t)
	worker := &stubInvoker{}
	h.notes.WithInvoker(worker)
	n := h.checklist("u", "Packing", "- [ ] passport")

	mode, err := h.notes.RequestClean(h.ctx, "u", n.ID, "")
	if err != nil || mode != model.NoteCleanTasks {
		t.Fatalf("RequestClean(\"\") = %q, %v; want tasks", mode, err)
	}
	if got := worker.calls; len(got) != 1 || got[0] != "clean-note/u/"+n.ID+"/tasks" {
		t.Errorf("worker calls = %v", got)
	}
	if _, err := h.notes.RequestClean(h.ctx, "u", n.ID, model.NoteCleanStructured); !errors.Is(err, ErrChecklistCleanMode) {
		t.Errorf("RequestClean(structured) on a checklist = %v, want ErrChecklistCleanMode", err)
	}
	if len(worker.calls) != 1 {
		t.Errorf("a refused mode reached the worker: %v", worker.calls)
	}
	if after := h.get("u", n.ID); after.CleanedRequestedMode != model.NoteCleanTasks {
		t.Errorf("the row's stamp = %q, want the tasks request", after.CleanedRequestedMode)
	}
}
