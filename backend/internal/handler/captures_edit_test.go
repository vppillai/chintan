package handler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

// seedAppended files an appended capture against note with its paragraph in
// the body, the way the worker leaves it, and returns the row.
func (h *harness) seedAppended(t *testing.T, userID string, note handler.Note, captureID, createdAt, text string) model.CaptureIndex {
	t.Helper()
	ctx := context.Background()
	stored, err := h.store.GetNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	existing, _ := h.objects.Get(ctx, stored.S3MarkdownKey)
	body := service.CaptureMarker(captureID) + "\n" + text
	if len(existing) > 0 {
		body = string(existing) + "\n\n" + body
	}
	if err := h.objects.Put(ctx, stored.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		t.Fatalf("Put body: %v", err)
	}
	audioKey := "tenants/" + userID + "/captures/" + captureID + "/audio.webm"
	if err := h.objects.Put(ctx, audioKey, []byte("opus"), "audio/webm"); err != nil {
		t.Fatalf("Put audio: %v", err)
	}
	return h.putCapture(t, model.CaptureIndex{
		ID: captureID, UserID: userID, NoteID: note.ID, Status: model.StatusAppended,
		CreatedAt: createdAt, AppendedAt: 1, AudioKey: audioKey,
	})
}

func (h *harness) noteBody(t *testing.T, userID, noteID string) string {
	t.Helper()
	w := h.do(t, http.MethodGet, "/v1/notes/"+noteID, userID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET note: %d %s", w.Code, w.Body.String())
	}
	var detail handler.NoteDetail
	decodeInto(t, w, &detail)
	return detail.Body
}

func TestDeleteCaptureRemovesTheParagraphAndThenIs404(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Kitchen", nil)
	h.seedAppended(t, "user1", note, "c_1", "2026-01-01T10:00:00.000000000Z", "First dictation.")
	c2 := h.seedAppended(t, "user1", note, "c_2", "2026-01-01T11:00:00.000000000Z", "Second dictation.")

	w := h.do(t, http.MethodDelete, "/v1/captures/c_1", "user1", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if got := h.noteBody(t, "user1", note.ID); got != "Second dictation." {
		t.Errorf("body = %q, want only the surviving paragraph", got)
	}
	if w := h.do(t, http.MethodGet, "/v1/captures/c_1", "user1", nil); w.Code != http.StatusNotFound {
		t.Errorf("GET deleted capture = %d", w.Code)
	}
	if w := h.do(t, http.MethodDelete, "/v1/captures/c_1", "user1", nil); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", w.Code)
	}
	if w := h.do(t, http.MethodGet, "/v1/captures/"+c2.ID, "user1", nil); w.Code != http.StatusOK {
		t.Errorf("the other capture = %d", w.Code)
	}
	// The list the note screen renders no longer offers the recording.
	w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	var detail handler.NoteDetail
	decodeInto(t, w, &detail)
	if len(detail.Captures) != 1 || detail.Captures[0].ID != "c_2" {
		t.Errorf("captures after delete = %+v", detail.Captures)
	}
}

func TestDeleteCaptureInFlightIsAConflict(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Busy", nil)
	h.putCapture(t, model.CaptureIndex{
		ID: "c_busy", UserID: "user1", NoteID: note.ID, Status: model.StatusTranscribing, CreatedAt: model.Now(),
	})
	w := h.do(t, http.MethodDelete, "/v1/captures/c_busy", "user1", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	p := problemOf(t, w)
	if detail, _ := p["detail"].(string); !strings.Contains(detail, "still being processed") {
		t.Errorf("detail = %q", detail)
	}
}

func TestDeleteCaptureIsScopedToTheCaller(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "owner", "Mine", nil)
	h.seedAppended(t, "owner", note, "c_1", "2026-01-01T10:00:00.000000000Z", "Mine.")

	if w := h.do(t, http.MethodDelete, "/v1/captures/c_1", "intruder", nil); w.Code != http.StatusNotFound {
		t.Fatalf("another tenant's DELETE = %d, want 404", w.Code)
	}
	if got := h.noteBody(t, "owner", note.ID); got != "Mine." {
		t.Fatalf("another tenant's delete changed the body: %q", got)
	}
	if w := h.do(t, http.MethodDelete, "/v1/captures/c_1", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated DELETE = %d", w.Code)
	}
}
