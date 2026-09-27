package handler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

// seedRegenerable is seedAppended with the transcript the regeneration
// resumes from, which is what makes the recording qualify.
func (h *harness) seedRegenerable(t *testing.T, userID string, note handler.Note, captureID, text string) model.CaptureIndex {
	t.Helper()
	c := h.seedAppended(t, userID, note, captureID, model.Now(), text)
	c.RawKey = "tenants/" + userID + "/captures/" + captureID + "/raw.txt"
	return h.putCapture(t, c)
}

// POST /v1/notes/{id}/regenerate is a hand-off: the recordings go back to
// transcribed with their clean artefact and append claim cleared, the worker
// is invoked once with their ids, and the 202 says how many.
func TestRegenerateResetsTheRecordingsAndQueuesTheWorker(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Roof", nil)
	h.seedRegenerable(t, "user1", note, "c_1", "the gutter leaks")
	h.seedRegenerable(t, "user1", note, "c_2", "call the roofer")
	// A recording that never landed has no words in the note to replace.
	h.putCapture(t, model.CaptureIndex{
		ID: "c_failed", UserID: "user1", NoteID: note.ID, Status: model.StatusFailed,
		CreatedAt: model.Now(), RawKey: "tenants/user1/captures/c_failed/raw.txt",
	})

	w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var queued handler.NoteRegenerateQueued
	decodeInto(t, w, &queued)
	if queued.Status != "queued" || queued.Captures != 2 {
		t.Errorf("body = %+v, want queued with 2 captures", queued)
	}
	if got := strings.Join(h.worker.calls, ","); got != "regenerate-note/user1/"+note.ID+"/c_1+c_2" {
		t.Errorf("worker calls = %v", h.worker.calls)
	}
	for _, id := range []string{"c_1", "c_2"} {
		c, err := h.store.GetCapture(context.Background(), "user1", id)
		if err != nil {
			t.Fatalf("GetCapture(%s): %v", id, err)
		}
		if c.Status != model.StatusTranscribed || c.CleanKey != "" || c.AppendedAt != 0 || c.AppendToken != "" || c.RawKey == "" {
			t.Errorf("%s after the request = status %s clean %q appended %d token %q raw %q; want transcribed with the claim cleared and the transcript kept",
				id, c.Status, c.CleanKey, c.AppendedAt, c.AppendToken, c.RawKey)
		}
	}
	// The body is untouched until the worker replaces each paragraph.
	if body := h.noteBody(t, "user1", note.ID); !strings.Contains(body, "the gutter leaks") || !strings.Contains(body, "call the roofer") {
		t.Errorf("body after the request = %q; the words stay until the worker writes the new ones", body)
	}

	// While those recordings are in flight a second request is refused.
	w = h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("second request: status = %d body = %s", w.Code, w.Body.String())
	}
	if p := problemOf(t, w); !strings.Contains(p["detail"].(string), "being regenerated") {
		t.Errorf("detail = %v, want the in-flight sentence", p["detail"])
	}
	if len(h.worker.calls) != 1 {
		t.Errorf("worker calls = %v, want the one hand-off", h.worker.calls)
	}
}

func TestRegenerateRefusals(t *testing.T) {
	t.Run("a note with nothing from a prompt is 202 with zero and no hand-off", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Typed", map[string]any{"body": "typed by hand"})
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var queued handler.NoteRegenerateQueued
		decodeInto(t, w, &queued)
		if queued.Captures != 0 || len(h.worker.calls) != 0 {
			t.Errorf("captures = %d, worker calls = %v; want zero and none", queued.Captures, h.worker.calls)
		}
	})
	t.Run("a recording still being filed is 409", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Busy", nil)
		h.seedRegenerable(t, "user1", note, "c_done", "done")
		h.putCapture(t, model.CaptureIndex{
			ID: "c_moving", UserID: "user1", NoteID: note.ID, Status: model.StatusCleaning,
			CreatedAt: model.Now(), LastProgressAt: model.Now(),
		})
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if c, _ := h.store.GetCapture(context.Background(), "user1", "c_done"); c.Status != model.StatusAppended {
			t.Errorf("a refused request reset c_done to %s", c.Status)
		}
	})
	t.Run("archived note is 409", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "user1", nil)
		if w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil); w.Code != http.StatusConflict {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})
	t.Run("missing note is 404, another tenant's too", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		if w := h.do(t, http.MethodPost, "/v1/notes/missing/regenerate", "user1", nil); w.Code != http.StatusNotFound {
			t.Fatalf("status = %d", w.Code)
		}
		if w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user2", nil); w.Code != http.StatusNotFound {
			t.Fatalf("another tenant: status = %d", w.Code)
		}
	})
	t.Run("spend cap is 429 before anything is reset", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.seedRegenerable(t, "user1", note, "c_1", "the gutter leaks")
		h.spend.capped = true
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if c, _ := h.store.GetCapture(context.Background(), "user1", "c_1"); c.Status != model.StatusAppended {
			t.Errorf("a capped request reset c_1 to %s", c.Status)
		}
	})
	t.Run("a hand-off that fails is a 500 and the recordings are put back", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.seedRegenerable(t, "user1", note, "c_1", "the gutter leaks")
		h.worker.cleanErr = errors.New("lambda: AccessDeniedException")
		w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "AccessDenied") {
			t.Error("the infrastructure error text reached the client")
		}
		c, err := h.store.GetCapture(context.Background(), "user1", "c_1")
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != model.StatusAppended || c.AppendedAt == 0 {
			t.Errorf("after a failed hand-off c_1 = %s appended_at %d; want appended as it was", c.Status, c.AppendedAt)
		}
		// So the person's retry a moment later is not met with "in flight".
		h.worker.cleanErr = nil
		if w := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil); w.Code != http.StatusAccepted {
			t.Errorf("retry after a failed hand-off: status = %d body = %s", w.Code, w.Body.String())
		}
	})
	t.Run("idempotency key replays the 202", func(t *testing.T) {
		h := newHarness(t)
		note := h.createNote(t, "user1", "Roof", nil)
		h.seedRegenerable(t, "user1", note, "c_1", "the gutter leaks")
		key := [2]string{"Idempotency-Key", "regenerate-once-12345"}
		first := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil, key)
		second := h.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/regenerate", "user1", nil, key)
		if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
			t.Fatalf("statuses = %d, %d", first.Code, second.Code)
		}
		if second.Header().Get("Idempotency-Replayed") != "true" {
			t.Error("the second request was not a replay")
		}
		if len(h.worker.calls) != 1 {
			t.Errorf("worker calls = %v, want exactly one for a replayed key", h.worker.calls)
		}
	})
}
