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

// ---------------------------------------------------------------- move

func TestMoveCaptureRelocatesTheParagraph(t *testing.T) {
	h := newHarness(t)
	source := h.createNote(t, "user1", "Source", nil)
	target := h.createNote(t, "user1", "Target", nil)
	h.seedAppended(t, "user1", target, "t_1", "2026-01-01T09:00:00.000000000Z", "Nine.")
	h.seedAppended(t, "user1", target, "t_2", "2026-01-01T11:00:00.000000000Z", "Eleven.")
	h.seedAppended(t, "user1", source, "c_1", "2026-01-01T10:00:00.000000000Z", "Ten, from elsewhere.")

	w := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID},
		[2]string{"Idempotency-Key", "move-c1-once"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var moved handler.Capture
	decodeInto(t, w, &moved)
	if moved.NoteID == nil || *moved.NoteID != target.ID {
		t.Errorf("note_id = %v, want the target", moved.NoteID)
	}
	if got := h.noteBody(t, "user1", source.ID); got != "" {
		t.Errorf("source body = %q, want empty", got)
	}
	if got, want := h.noteBody(t, "user1", target.ID), "Nine.\n\nTen, from elsewhere.\n\nEleven."; got != want {
		t.Errorf("target body = %q, want %q", got, want)
	}

	// The same key replays the same answer rather than moving twice.
	again := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID},
		[2]string{"Idempotency-Key", "move-c1-once"})
	if again.Code != http.StatusOK {
		t.Errorf("replay = %d", again.Code)
	}
	// And without a key, the capture is already there: a no-op.
	if w := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID}); w.Code != http.StatusNoContent {
		t.Errorf("moving to where it is = %d, want 204", w.Code)
	}
}

func TestMoveCaptureRefusalsOverHTTP(t *testing.T) {
	h := newHarness(t)
	source := h.createNote(t, "user1", "Source", nil)
	archived := h.createNote(t, "user1", "Archived", nil)
	h.do(t, http.MethodDelete, "/v1/notes/"+archived.ID, "user1", nil)
	h.seedAppended(t, "user1", source, "c_1", "2026-01-01T10:00:00.000000000Z", "Dictated.")
	h.putCapture(t, model.CaptureIndex{ID: "c_unfiled", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now()})

	cases := map[string]struct {
		path string
		body any
		want int
	}{
		"archived target":      {"/v1/captures/c_1/move", map[string]any{"note_id": archived.ID}, http.StatusConflict},
		"missing target":       {"/v1/captures/c_1/move", map[string]any{"note_id": "note_missing"}, http.StatusNotFound},
		"missing capture":      {"/v1/captures/missing/move", map[string]any{"note_id": source.ID}, http.StatusNotFound},
		"no note_id":           {"/v1/captures/c_1/move", map[string]any{}, http.StatusBadRequest},
		"unknown field":        {"/v1/captures/c_1/move", map[string]any{"note_id": source.ID, "title": "x"}, http.StatusBadRequest},
		"capture with no note": {"/v1/captures/c_unfiled/move", map[string]any{"note_id": source.ID}, http.StatusConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := h.do(t, http.MethodPost, tc.path, "user1", tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d body = %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			problemOf(t, w)
		})
	}
	if got := h.noteBody(t, "user1", source.ID); got != "Dictated." {
		t.Fatalf("a refused move changed the source: %q", got)
	}
}

func TestMoveCaptureIsScopedToTheCaller(t *testing.T) {
	h := newHarness(t)
	theirs := h.createNote(t, "owner", "Theirs", nil)
	h.seedAppended(t, "owner", theirs, "c_1", "2026-01-01T10:00:00.000000000Z", "Theirs.")
	mine := h.createNote(t, "intruder", "Mine", nil)
	h.seedAppended(t, "intruder", mine, "c_2", "2026-01-01T10:00:00.000000000Z", "Mine.")

	if w := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "intruder", map[string]any{"note_id": mine.ID}); w.Code != http.StatusNotFound {
		t.Fatalf("another tenant's capture = %d, want 404", w.Code)
	}
	if w := h.do(t, http.MethodPost, "/v1/captures/c_2/move", "intruder", map[string]any{"note_id": theirs.ID}); w.Code != http.StatusNotFound {
		t.Fatalf("another tenant's note as target = %d, want 404", w.Code)
	}
	if h.noteBody(t, "owner", theirs.ID) != "Theirs." || h.noteBody(t, "intruder", mine.ID) != "Mine." {
		t.Fatal("a cross-tenant move changed a body")
	}
}

// A move whose second write fails is rolled back and answered with a 503 the
// client may repeat, marked as such in the problem's type.
func TestMoveCaptureThatFailsHalfWayIsRolledBackAndRetryable(t *testing.T) {
	var failKey string
	h := newHarness(t, withFailingBodyWrite(&failKey))
	source := h.createNote(t, "user1", "Source", nil)
	target := h.createNote(t, "user1", "Target", nil)
	h.seedAppended(t, "user1", source, "c_1", "2026-01-01T10:00:00.000000000Z", "Dictated.")
	stored, err := h.store.GetNote(context.Background(), "user1", target.ID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	failKey = stored.S3MarkdownKey

	w := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	p := problemOf(t, w)
	if p["type"] != "https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#retryable" {
		t.Errorf("type = %v, want the retryable URI", p["type"])
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a retryable 503")
	}
	if got := h.noteBody(t, "user1", source.ID); got != "Dictated." {
		t.Errorf("source after rollback = %q", got)
	}
	if got := h.noteBody(t, "user1", target.ID); got != "" {
		t.Errorf("target after a failed move = %q", got)
	}
	w = h.do(t, http.MethodGet, "/v1/captures/c_1", "user1", nil)
	var c handler.Capture
	decodeInto(t, w, &c)
	if c.NoteID == nil || *c.NoteID != source.ID {
		t.Errorf("capture points at %v after a failed move", c.NoteID)
	}

	failKey = ""
	if w := h.do(t, http.MethodPost, "/v1/captures/c_1/move", "user1", map[string]any{"note_id": target.ID}); w.Code != http.StatusOK {
		t.Fatalf("retry = %d body = %s", w.Code, w.Body.String())
	}
	if got := h.noteBody(t, "user1", target.ID); got != "Dictated." {
		t.Errorf("target after retry = %q", got)
	}
}

// ---------------------------------------------------------------- manifest

func TestNoteRecordingURLsIsOneManifestOldestFirst(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Kitchen rebuild", nil)
	h.seedAppended(t, "user1", note, "c_new", "2026-03-04T15:06:00.000000000Z", "Later.")
	h.seedAppended(t, "user1", note, "c_old", "2026-03-04T09:30:00.000000000Z", "Earlier.")
	// No audio: never in the manifest.
	h.putCapture(t, model.CaptureIndex{ID: "c_failed", UserID: "user1", NoteID: note.ID, Status: model.StatusFailed, CreatedAt: model.Now()})

	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/recordings/urls", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var manifest handler.RecordingURLs
	decodeInto(t, w, &manifest)
	if len(manifest.Items) != 2 {
		t.Fatalf("items = %+v, want two", manifest.Items)
	}
	if manifest.Items[0].CaptureID != "c_old" || manifest.Items[1].CaptureID != "c_new" {
		t.Errorf("order = %s, %s; want oldest first", manifest.Items[0].CaptureID, manifest.Items[1].CaptureID)
	}
	if got := manifest.Items[0].Filename; got != "kitchen-rebuild-20260304-0930.webm" {
		t.Errorf("filename = %q", got)
	}
	for _, item := range manifest.Items {
		if item.URL == "" || item.ExpiresAt == "" {
			t.Errorf("%s: url=%q expires_at=%q", item.CaptureID, item.URL, item.ExpiresAt)
		}
	}
	// An empty manifest is an empty list, never null.
	empty := h.createNote(t, "user1", "Nothing yet", nil)
	w = h.do(t, http.MethodGet, "/v1/notes/"+empty.ID+"/recordings/urls", "user1", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("empty manifest: %d %s", w.Code, w.Body.String())
	}
}

func TestNoteRecordingURLsIsScopedToTheCaller(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "owner", "Mine", nil)
	h.seedAppended(t, "owner", note, "c_1", "2026-03-04T09:30:00.000000000Z", "Mine.")
	if w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/recordings/urls", "intruder", nil); w.Code != http.StatusNotFound {
		t.Fatalf("another tenant's manifest = %d, want 404", w.Code)
	}
	if w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/recordings/urls", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated manifest = %d", w.Code)
	}
}
