package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// editHarness is a notes service and a capture service over one memory store,
// plus the seeding a recording edit needs: a note whose body the worker has
// appended to, with a marker ahead of each paragraph, and the capture rows
// those markers name.
type editHarness struct {
	t        *testing.T
	ctx      context.Context
	store    *memory.Store
	objects  *memory.Objects
	notes    *NotesService
	captures *CaptureService
}

func newEditHarness(t *testing.T) *editHarness {
	t.Helper()
	store := memory.NewStore()
	objects := memory.NewObjects()
	return &editHarness{
		t: t, ctx: context.Background(), store: store, objects: objects,
		notes:    NewNotesService(store, objects),
		captures: NewCaptureService(store, objects),
	}
}

// note creates a note and gives it body verbatim, markers included.
func (h *editHarness) note(userID, title, body string) model.NoteIndex {
	h.t.Helper()
	n, err := h.notes.CreateNote(h.ctx, userID, title, nil)
	if err != nil {
		h.t.Fatalf("CreateNote: %v", err)
	}
	if err := h.objects.Put(h.ctx, n.S3MarkdownKey, []byte(body), "text/markdown"); err != nil {
		h.t.Fatalf("Put body: %v", err)
	}
	if err := refreshNoteIndex(h.ctx, h.store, h.objects, userID, n.ID); err != nil {
		h.t.Fatalf("refreshNoteIndex: %v", err)
	}
	n, err = h.store.GetNote(h.ctx, userID, n.ID)
	if err != nil {
		h.t.Fatalf("GetNote: %v", err)
	}
	return n
}

// appended writes an appended capture row with every object the pipeline
// leaves behind, so a delete has something to unlink.
func (h *editHarness) appended(userID, noteID, captureID, createdAt string) model.CaptureIndex {
	h.t.Helper()
	c := model.CaptureIndex{
		ID: captureID, UserID: userID, NoteID: noteID, Status: model.StatusAppended,
		CreatedAt:   createdAt,
		AudioKey:    "tenants/" + userID + "/captures/" + captureID + "/audio.webm",
		RawKey:      "tenants/" + userID + "/captures/" + captureID + "/raw.txt",
		RoutedKey:   "tenants/" + userID + "/captures/" + captureID + "/routed.txt",
		CleanKey:    "tenants/" + userID + "/captures/" + captureID + "/clean.txt",
		SegmentsKey: "tenants/" + userID + "/captures/" + captureID + "/segments.json",
		PeaksKey:    "tenants/" + userID + "/captures/" + captureID + "/peaks.json",
		AppendedAt:  1,
	}
	for _, key := range []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey, c.SegmentsKey, c.PeaksKey} {
		if err := h.objects.Put(h.ctx, key, []byte("x"), "application/octet-stream"); err != nil {
			h.t.Fatalf("Put %s: %v", key, err)
		}
	}
	stored, err := h.store.PutCapture(h.ctx, c)
	if err != nil {
		h.t.Fatalf("PutCapture: %v", err)
	}
	return stored
}

func (h *editHarness) body(n model.NoteIndex) string {
	h.t.Helper()
	b, err := h.objects.Get(h.ctx, n.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.t.Fatalf("Get body: %v", err)
	}
	return string(b)
}

func (h *editHarness) objectExists(key string) bool {
	h.t.Helper()
	ok, err := h.objects.Exists(h.ctx, key)
	if err != nil {
		h.t.Fatalf("Exists: %v", err)
	}
	return ok
}

// ---------------------------------------------------------------- delete

func TestDeleteCaptureCutsItsParagraphAndUnlinksEverything(t *testing.T) {
	h := newEditHarness(t)
	body := "typed intro\n\n" + CaptureMarker("c_1") + "\nFirst dictation.\n\n" + CaptureMarker("c_2") + "\nSecond dictation."
	note := h.note("u1", "Kitchen", body)
	c1 := h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	h.appended("u1", note.ID, "c_2", "2026-01-01T11:00:00.000000000Z")

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}

	if got, want := h.body(note), "typed intro\n\n"+CaptureMarker("c_2")+"\nSecond dictation."; got != want {
		t.Errorf("body after delete = %q, want %q", got, want)
	}
	after, err := h.store.GetNote(h.ctx, "u1", note.ID)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if strings.Contains(after.Snippet, "First dictation") || strings.Contains(after.SearchText, "first dictation") {
		t.Errorf("index still carries the deleted paragraph: snippet=%q search=%q", after.Snippet, after.SearchText)
	}
	if !strings.Contains(after.SearchText, "second dictation") {
		t.Errorf("index lost the surviving paragraph: %q", after.SearchText)
	}
	if after.Version <= note.Version || after.UpdatedAt <= note.UpdatedAt {
		t.Errorf("index was not refreshed: version %d→%d, updated %s→%s", note.Version, after.Version, note.UpdatedAt, after.UpdatedAt)
	}
	for _, key := range []string{c1.AudioKey, c1.RawKey, c1.RoutedKey, c1.CleanKey, c1.SegmentsKey, c1.PeaksKey} {
		if h.objectExists(key) {
			t.Errorf("object %s survived the delete", key)
		}
	}
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("capture row survived: err=%v", err)
	}
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_2"); err != nil {
		t.Errorf("the other capture was touched: %v", err)
	}

	// Idempotent in effect: the second call finds nothing.
	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteCaptureOfTheFirstAndOnlyParagraph(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Solo", CaptureMarker("c_1")+"\nOnly paragraph.")
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if got := h.body(note); got != "" {
		t.Errorf("body = %q, want empty", got)
	}
	after, _ := h.store.GetNote(h.ctx, "u1", note.ID)
	if after.Snippet != "" || after.SearchText != "" {
		t.Errorf("index not emptied: snippet=%q search=%q", after.Snippet, after.SearchText)
	}
}

// After the user has saved the note, the markers are a trailer and the words
// are theirs. Deleting the recording removes the bookkeeping and not the text.
func TestDeleteCaptureAfterAnEditKeepsTheUsersText(t *testing.T) {
	h := newEditHarness(t)
	stored := "typed\n\n" + CaptureMarker("c_1") + "\nwhat was said"
	edited := "the user rewrote everything"
	note := h.note("u1", "Edited", CarryCaptureMarkers(stored, edited))
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if got := h.body(note); got != edited {
		t.Errorf("body = %q, want the user's text %q", got, edited)
	}
}

func TestDeleteCaptureThatNeverAppendedRemovesRowAndObjectsOnly(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Target", "untouched body")
	for _, status := range []model.CaptureStatus{model.StatusFailed, model.StatusNeedsTarget, model.StatusNoContent, model.StatusSpendCapped} {
		id := "c_" + string(status)
		c := h.appended("u1", note.ID, id, "2026-01-01T10:00:00.000000000Z")
		c.Status = status
		if status == model.StatusNeedsTarget {
			c.NoteID = ""
		}
		if _, err := h.store.PutCapture(h.ctx, c); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
		if err := h.captures.DeleteCapture(h.ctx, "u1", id); err != nil {
			t.Fatalf("DeleteCapture(%s): %v", status, err)
		}
		if h.objectExists(c.AudioKey) {
			t.Errorf("%s: audio survived", status)
		}
		if _, err := h.store.GetCapture(h.ctx, "u1", id); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("%s: row survived: %v", status, err)
		}
	}
	if got := h.body(note); got != "untouched body" {
		t.Errorf("a capture with no paragraph changed the body: %q", got)
	}
}

func TestDeleteCaptureRefusesOneStillInFlight(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Busy", "")
	c := h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	for _, status := range []model.CaptureStatus{model.StatusUploaded, model.StatusTranscribing, model.StatusAppending} {
		c.Status = status
		updated, err := h.store.PutCapture(h.ctx, c)
		if err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
		c = updated
		if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); !errors.Is(err, ErrCaptureInFlight) {
			t.Errorf("%s: DeleteCapture = %v, want ErrCaptureInFlight", status, err)
		}
	}
	if !h.objectExists(c.AudioKey) {
		t.Error("a refused delete removed the audio")
	}
}

func TestDeleteCaptureIsScopedToTheTenant(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("owner", "Mine", CaptureMarker("c_1")+"\nMine.")
	c := h.appended("owner", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	if err := h.captures.DeleteCapture(h.ctx, "intruder", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("another tenant's delete = %v, want ErrNotFound", err)
	}
	if !h.objectExists(c.AudioKey) || h.body(note) != CaptureMarker("c_1")+"\nMine." {
		t.Fatal("another tenant's delete changed something")
	}
}

// A delete that fails after cutting the paragraph leaves the row, and the
// retry finishes without cutting anything else.
func TestDeleteCaptureRetryFinishesAPartialDelete(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nOne.\n\n" + CaptureMarker("c_2") + "\nTwo."
	note := h.note("u1", "Partial", body)
	c := h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	h.appended("u1", note.ID, "c_2", "2026-01-01T11:00:00.000000000Z")

	broken := NewCaptureService(h.store, failingDeletes{Objects: h.objects, key: c.AudioKey})
	if err := broken.DeleteCapture(h.ctx, "u1", "c_1"); err == nil {
		t.Fatal("a delete whose object unlink failed reported success")
	}
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("the row must survive a failed cascade: %v", err)
	}
	if got, want := h.body(note), CaptureMarker("c_2")+"\nTwo."; got != want {
		t.Fatalf("body after the failed attempt = %q, want %q", got, want)
	}

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got, want := h.body(note), CaptureMarker("c_2")+"\nTwo."; got != want {
		t.Fatalf("the retry cut something else: %q", got)
	}
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("row survived the retry: %v", err)
	}
}

// failingDeletes fails Delete for one key and is the memory store otherwise.
type failingDeletes struct {
	repository.Objects
	key string
}

func (f failingDeletes) Delete(ctx context.Context, key string) error {
	if key == f.key {
		return errors.New("s3: AccessDenied")
	}
	return f.Objects.Delete(ctx, key)
}
