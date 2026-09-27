package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

// The request path's whole job: decide which recordings qualify, reset each,
// hand the note over once with their ids, and answer with the count.
func TestRequestRegenerateResetsWhatQualifiesAndHandsOverOnce(t *testing.T) {
	h := newEditHarness(t)
	worker := &stubInvoker{}
	h.notes.WithInvoker(worker)

	body := CaptureMarker("c_old") + "\nFirst take.\n\n" + CaptureMarker("c_new") + "\nSecond take.\n\nMy own words now.\n" + CaptureMarker("c_edited")
	n := h.note("u", "Roof", body)
	// Newest first in the store; the hand-off is oldest first.
	h.appended("u", n.ID, "c_new", "2026-01-01T10:00:00.000000000Z")
	h.appended("u", n.ID, "c_old", "2026-01-01T09:00:00.000000000Z")
	// Its paragraph was rewritten by hand: the marker is a trailer now.
	h.appended("u", n.ID, "c_edited", "2026-01-01T11:00:00.000000000Z")
	// No words in the note: never landed, or arrived without a transcript.
	if _, err := h.store.PutCapture(h.ctx, model.CaptureIndex{ID: "c_failed", UserID: "u", NoteID: n.ID, Status: model.StatusFailed, CreatedAt: model.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutCapture(h.ctx, model.CaptureIndex{ID: "c_no_raw", UserID: "u", NoteID: n.ID, Status: model.StatusAppended, CreatedAt: model.Now(), AppendedAt: 1}); err != nil {
		t.Fatal(err)
	}

	count, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID)
	if err != nil {
		t.Fatalf("RequestRegenerate: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if got := strings.Join(worker.calls, ","); got != "regenerate-note/u/"+n.ID+"/c_old+c_new" {
		t.Errorf("worker calls = %v, want one hand-off naming the two, oldest first", worker.calls)
	}
	for _, id := range []string{"c_old", "c_new"} {
		c, err := h.store.GetCapture(h.ctx, "u", id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != model.StatusTranscribed || c.CleanKey != "" || c.AppendToken != "" || c.AppendClaimedAt != 0 || c.AppendedAt != 0 {
			t.Errorf("%s = %+v; want transcribed with the clean artefact and the claim cleared", id, c)
		}
		if c.RawKey == "" || c.RoutedKey == "" || c.SegmentsKey == "" {
			t.Errorf("%s lost a transcript key: %+v", id, c)
		}
	}
	for _, id := range []string{"c_edited", "c_no_raw"} {
		if c, _ := h.store.GetCapture(h.ctx, "u", id); c.Status != model.StatusAppended {
			t.Errorf("%s was reset to %s; it had nothing to regenerate", id, c.Status)
		}
	}

	// A second request meets the rows in flight.
	if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); !errors.Is(err, ErrRegenerateInFlight) {
		t.Errorf("second request = %v, want ErrRegenerateInFlight", err)
	}
	if len(worker.calls) != 1 {
		t.Errorf("worker calls = %v, want still one", worker.calls)
	}
}

func TestRequestRegenerateRefusals(t *testing.T) {
	t.Run("a verbatim note has nothing from a prompt", func(t *testing.T) {
		h := newEditHarness(t)
		h.notes.WithInvoker(&stubInvoker{})
		n := h.note("u", "Quote", CaptureMarker("c_1")+"\nexactly as spoken")
		h.appended("u", n.ID, "c_1", "2026-01-01T09:00:00.000000000Z")
		verbatim := true
		version := n.Version
		if _, err := h.notes.UpdateNote(h.ctx, "u", n.ID, NoteUpdates{Verbatim: &verbatim, ExpectedVersion: &version}); err != nil {
			t.Fatal(err)
		}
		count, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID)
		if err != nil || count != 0 {
			t.Errorf("RequestRegenerate = %d, %v; want 0 and no error", count, err)
		}
	})
	t.Run("a checklist qualifies even with every marker carried", func(t *testing.T) {
		h := newEditHarness(t)
		worker := &stubInvoker{}
		h.notes.WithInvoker(worker)
		n := h.note("u", "Shopping", "- [x] Milk\n- [ ] Eggs\n"+CaptureMarker("c_1"))
		kind := model.NoteKindChecklist
		version := n.Version
		if _, err := h.notes.UpdateNote(h.ctx, "u", n.ID, NoteUpdates{Kind: &kind, ExpectedVersion: &version}); err != nil {
			t.Fatal(err)
		}
		h.appended("u", n.ID, "c_1", "2026-01-01T09:00:00.000000000Z")
		count, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID)
		if err != nil || count != 1 {
			t.Errorf("RequestRegenerate = %d, %v; want 1", count, err)
		}
	})
	t.Run("a recording being filed, or an append stamp, is in flight", func(t *testing.T) {
		h := newEditHarness(t)
		h.notes.WithInvoker(&stubInvoker{})
		n := h.note("u", "Roof", CaptureMarker("c_1")+"\nFirst take.")
		h.appended("u", n.ID, "c_1", "2026-01-01T09:00:00.000000000Z")
		if _, err := h.store.PutCapture(h.ctx, model.CaptureIndex{ID: "c_moving", UserID: "u", NoteID: n.ID, Status: model.StatusCleaning, CreatedAt: model.Now(), LastProgressAt: model.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); !errors.Is(err, ErrRegenerateInFlight) {
			t.Errorf("with a recording cleaning: %v, want ErrRegenerateInFlight", err)
		}
		if err := h.store.DeleteCapture(h.ctx, "u", "c_moving"); err != nil {
			t.Fatal(err)
		}
		current := h.get("u", n.ID)
		if _, err := h.store.StampNoteAppend(h.ctx, "u", n.ID, "c_2", current.Version, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); !errors.Is(err, ErrRegenerateInFlight) {
			t.Errorf("with an append stamp: %v, want ErrRegenerateInFlight", err)
		}
		if c, _ := h.store.GetCapture(h.ctx, "u", "c_1"); c.Status != model.StatusAppended {
			t.Errorf("a refused request reset c_1 to %s", c.Status)
		}
	})
	t.Run("archived is refused and no worker is 503's error", func(t *testing.T) {
		h := newEditHarness(t)
		n := h.note("u", "Roof", "")
		if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); !errors.Is(err, ErrCaptureWorkerUnavailable) {
			t.Errorf("no worker: %v, want ErrCaptureWorkerUnavailable", err)
		}
		h.notes.WithInvoker(&stubInvoker{})
		if _, err := h.notes.ArchiveNote(h.ctx, "u", n.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); !errors.Is(err, ErrNoteArchived) {
			t.Errorf("archived: %v, want ErrNoteArchived", err)
		}
	})
	t.Run("a failed hand-off puts the recordings back", func(t *testing.T) {
		h := newEditHarness(t)
		worker := &stubInvoker{regenerateErr: errors.New("lambda refused")}
		h.notes.WithInvoker(worker)
		n := h.note("u", "Roof", CaptureMarker("c_1")+"\nFirst take.")
		before := h.appended("u", n.ID, "c_1", "2026-01-01T09:00:00.000000000Z")
		if _, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); err == nil {
			t.Fatal("RequestRegenerate succeeded with a refused hand-off")
		}
		c, err := h.store.GetCapture(h.ctx, "u", "c_1")
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != model.StatusAppended || c.CleanKey != before.CleanKey || c.AppendedAt != before.AppendedAt {
			t.Errorf("after the failed hand-off c_1 = %+v; want restored to %+v", c, before)
		}
		worker.regenerateErr = nil
		if count, err := h.notes.RequestRegenerate(h.ctx, "u", n.ID); err != nil || count != 1 {
			t.Errorf("retry = %d, %v; want 1 and no error", count, err)
		}
	})
}

// ReplaceChecklistItems and keepTick live in the pipeline; the service's rule
// is which recordings qualify, and it is one rule for both roads.
func TestRegenerableCapturesIsTheOneRuleForBothRoads(t *testing.T) {
	h := newEditHarness(t)
	n := h.note("u", "Roof", CaptureMarker("c_1")+"\nFirst take.\n\n"+CaptureMarker("c_2")+"\nSecond take.")
	h.appended("u", n.ID, "c_2", "2026-01-01T10:00:00.000000000Z")
	h.appended("u", n.ID, "c_1", "2026-01-01T09:00:00.000000000Z")
	got, err := RegenerableCaptures(h.ctx, h.store, h.objects, "u", h.get("u", n.ID), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "c_1" || got[1].ID != "c_2" {
		t.Errorf("RegenerableCaptures = %v, want c_1 then c_2", got)
	}
}
