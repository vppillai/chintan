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

// ---------------------------------------------------------------- move

const (
	t0900 = "2026-01-01T09:00:00.000000000Z"
	t1000 = "2026-01-01T10:00:00.000000000Z"
	t1100 = "2026-01-01T11:00:00.000000000Z"
	t1200 = "2026-01-01T12:00:00.000000000Z"
	t1300 = "2026-01-01T13:00:00.000000000Z"
)

// A target with recordings before and after the moved one: the paragraph
// lands between them, by created_at, not at the end.
func TestMoveCapturePlacesTheParagraphChronologically(t *testing.T) {
	h := newEditHarness(t)
	source := h.note("u1", "Source", "typed\n\n"+CaptureMarker("c_1")+"\nMoved.\n\n"+CaptureMarker("c_3")+"\nStays.")
	target := h.note("u1", "Target", CaptureMarker("t_1")+"\nNine.\n\n"+CaptureMarker("t_2")+"\nEleven.\n\n"+CaptureMarker("t_3")+"\nThirteen.")
	h.appended("u1", source.ID, "c_1", t1000)
	h.appended("u1", source.ID, "c_3", t1200)
	h.appended("u1", target.ID, "t_1", t0900)
	h.appended("u1", target.ID, "t_2", t1100)
	h.appended("u1", target.ID, "t_3", t1300)

	moved, ok, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", target.ID)
	if err != nil || !ok {
		t.Fatalf("MoveCapture = (%v, %v)", ok, err)
	}
	if moved.NoteID != target.ID {
		t.Errorf("capture points at %q, want the target", moved.NoteID)
	}
	if got, want := h.body(source), "typed\n\n"+CaptureMarker("c_3")+"\nStays."; got != want {
		t.Errorf("source body = %q, want %q", got, want)
	}
	wantTarget := CaptureMarker("t_1") + "\nNine.\n\n" + CaptureMarker("c_1") + "\nMoved.\n\n" + CaptureMarker("t_2") + "\nEleven.\n\n" + CaptureMarker("t_3") + "\nThirteen."
	if got := h.body(target); got != wantTarget {
		t.Errorf("target body = %q, want %q", got, wantTarget)
	}
	if got := CaptureMarkerIDs(h.body(target)); strings.Join(got, ",") != "t_1,c_1,t_2,t_3" {
		t.Errorf("target order = %v", got)
	}

	// Both indexes follow their bodies.
	src, _ := h.store.GetNote(h.ctx, "u1", source.ID)
	dst, _ := h.store.GetNote(h.ctx, "u1", target.ID)
	if strings.Contains(src.SearchText, "moved") || !strings.Contains(dst.SearchText, "moved") {
		t.Errorf("indexes not refreshed: source=%q target=%q", src.SearchText, dst.SearchText)
	}
	if src.Version <= source.Version || dst.Version <= target.Version {
		t.Errorf("versions did not advance: source %d→%d target %d→%d", source.Version, src.Version, target.Version, dst.Version)
	}
	// The recording now lists under the target.
	page, _ := h.store.ListCapturesByNote(h.ctx, "u1", target.ID, repository.ListOptions{})
	if len(page.Items) != 4 {
		t.Errorf("target has %d captures, want 4", len(page.Items))
	}
}

func TestMoveCaptureToTheFirstAndLastPosition(t *testing.T) {
	h := newEditHarness(t)
	source := h.note("u1", "Source", CaptureMarker("old")+"\nOldest.\n\n"+CaptureMarker("new")+"\nNewest.")
	target := h.note("u1", "Target", CaptureMarker("t_1")+"\nMiddle.")
	h.appended("u1", source.ID, "old", t0900)
	h.appended("u1", source.ID, "new", t1300)
	h.appended("u1", target.ID, "t_1", t1100)

	if _, _, err := h.captures.MoveCapture(h.ctx, "u1", "old", target.ID); err != nil {
		t.Fatalf("move oldest: %v", err)
	}
	if got, want := h.body(target), CaptureMarker("old")+"\nOldest.\n\n"+CaptureMarker("t_1")+"\nMiddle."; got != want {
		t.Errorf("after moving the oldest: %q, want %q", got, want)
	}
	if _, _, err := h.captures.MoveCapture(h.ctx, "u1", "new", target.ID); err != nil {
		t.Fatalf("move newest: %v", err)
	}
	if got, want := h.body(target), CaptureMarker("old")+"\nOldest.\n\n"+CaptureMarker("t_1")+"\nMiddle.\n\n"+CaptureMarker("new")+"\nNewest."; got != want {
		t.Errorf("after moving the newest: %q, want %q", got, want)
	}
	if got := h.body(source); got != "" {
		t.Errorf("source not emptied: %q", got)
	}
}

func TestMoveCaptureIntoANoteWithNoRecordingsAppends(t *testing.T) {
	h := newEditHarness(t)
	source := h.note("u1", "Source", CaptureMarker("c_1")+"\nDictated.")
	target := h.note("u1", "Typed only", "Some typed text.")
	h.appended("u1", source.ID, "c_1", t1000)

	if _, _, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", target.ID); err != nil {
		t.Fatalf("MoveCapture: %v", err)
	}
	if got, want := h.body(target), "Some typed text.\n\n"+CaptureMarker("c_1")+"\nDictated."; got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	if got := StripCaptureMarkers(h.body(target)); got != "Some typed text.\n\nDictated." {
		t.Errorf("the user sees %q", got)
	}
}

func TestMoveCaptureToItsOwnNoteIsANoOp(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nDictated."
	note := h.note("u1", "Same", body)
	h.appended("u1", note.ID, "c_1", t1000)

	c, moved, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", note.ID)
	if err != nil || moved || c == nil || c.NoteID != note.ID {
		t.Fatalf("MoveCapture = (%+v, %v, %v), want the capture unchanged and moved=false", c, moved, err)
	}
	after, _ := h.store.GetNote(h.ctx, "u1", note.ID)
	if h.body(note) != body || after.Version != note.Version {
		t.Fatal("a no-op move changed the note")
	}
}

func TestMoveCaptureRefusals(t *testing.T) {
	h := newEditHarness(t)
	source := h.note("u1", "Source", CaptureMarker("c_1")+"\nDictated.")
	h.appended("u1", source.ID, "c_1", t1000)
	archived := h.note("u1", "Archived", "")
	if _, err := h.notes.ArchiveNote(h.ctx, "u1", archived.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}
	unfiled := h.appended("u1", "", "c_unfiled", t1000)
	unfiled.Status = model.StatusNeedsTarget
	if _, err := h.store.PutCapture(h.ctx, unfiled); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	busy := h.appended("u1", source.ID, "c_busy", t1000)
	busy.Status = model.StatusCleaning
	if _, err := h.store.PutCapture(h.ctx, busy); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	other := h.note("u1", "Other", "")

	cases := map[string]struct {
		capture, target string
		want            error
	}{
		"archived target":       {"c_1", archived.ID, ErrNoteArchived},
		"missing target":        {"c_1", "note_missing", repository.ErrNotFound},
		"missing capture":       {"c_missing", other.ID, repository.ErrNotFound},
		"capture with no note":  {"c_unfiled", other.ID, ErrCaptureUnfiled},
		"capture still running": {"c_busy", other.ID, ErrCaptureInFlight},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := h.captures.MoveCapture(h.ctx, "u1", tc.capture, tc.target)
			if !errors.Is(err, tc.want) {
				t.Fatalf("MoveCapture = %v, want %v", err, tc.want)
			}
		})
	}
	if got := h.body(source); got != CaptureMarker("c_1")+"\nDictated." {
		t.Fatalf("a refused move changed the source: %q", got)
	}
}

// Another tenant's capture id, and another tenant's note as the target, are
// both simply absent.
func TestMoveCaptureIsScopedToTheTenant(t *testing.T) {
	h := newEditHarness(t)
	theirs := h.note("owner", "Theirs", CaptureMarker("c_1")+"\nTheirs.")
	h.appended("owner", theirs.ID, "c_1", t1000)
	mine := h.note("intruder", "Mine", CaptureMarker("c_2")+"\nMine.")
	h.appended("intruder", mine.ID, "c_2", t1000)

	if _, _, err := h.captures.MoveCapture(h.ctx, "intruder", "c_1", mine.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("moving another tenant's capture = %v, want ErrNotFound", err)
	}
	if _, _, err := h.captures.MoveCapture(h.ctx, "intruder", "c_2", theirs.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("moving into another tenant's note = %v, want ErrNotFound", err)
	}
	if h.body(theirs) != CaptureMarker("c_1")+"\nTheirs." || h.body(mine) != CaptureMarker("c_2")+"\nMine." {
		t.Fatal("a cross-tenant move changed a body")
	}
}

// The target write fails after the source was cut. The paragraph goes back
// exactly where it was, the row still points at the source, and the error
// says the request can be repeated.
func TestMoveCaptureRestoresTheSourceWhenTheTargetWriteFails(t *testing.T) {
	h := newEditHarness(t)
	sourceBody := "typed\n\n" + CaptureMarker("c_1") + "\nFirst.\n\n" + CaptureMarker("c_2") + "\nSecond."
	source := h.note("u1", "Source", sourceBody)
	targetBody := CaptureMarker("t_1") + "\nTheirs."
	target := h.note("u1", "Target", targetBody)
	h.appended("u1", source.ID, "c_1", t1000)
	h.appended("u1", source.ID, "c_2", t1200)
	h.appended("u1", target.ID, "t_1", t1100)

	broken := NewCaptureService(h.store, failingPutIfMatch{Objects: h.objects, key: target.S3MarkdownKey})
	_, moved, err := broken.MoveCapture(h.ctx, "u1", "c_1", target.ID)
	if moved || !errors.Is(err, ErrMoveIncomplete) {
		t.Fatalf("MoveCapture = (%v, %v), want ErrMoveIncomplete", moved, err)
	}
	if got := h.body(source); got != sourceBody {
		t.Errorf("source after rollback = %q, want the original %q", got, sourceBody)
	}
	if got := h.body(target); got != targetBody {
		t.Errorf("target changed by a failed move: %q", got)
	}
	c, _ := h.store.GetCapture(h.ctx, "u1", "c_1")
	if c.NoteID != source.ID {
		t.Errorf("capture points at %q after a failed move", c.NoteID)
	}

	// And the same request, with the fault gone, goes through.
	if _, moved, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", target.ID); err != nil || !moved {
		t.Fatalf("retry = (%v, %v)", moved, err)
	}
	if got, want := h.body(target), CaptureMarker("c_1")+"\nFirst.\n\n"+CaptureMarker("t_1")+"\nTheirs."; got != want {
		t.Errorf("target after retry = %q, want %q", got, want)
	}
}

// The first write failing is also a rollback, trivially: nothing was written.
func TestMoveCaptureThatCannotCutChangesNothing(t *testing.T) {
	h := newEditHarness(t)
	source := h.note("u1", "Source", CaptureMarker("c_1")+"\nFirst.")
	target := h.note("u1", "Target", "")
	h.appended("u1", source.ID, "c_1", t1000)

	broken := NewCaptureService(h.store, failingPutIfMatch{Objects: h.objects, key: source.S3MarkdownKey})
	if _, _, err := broken.MoveCapture(h.ctx, "u1", "c_1", target.ID); !errors.Is(err, ErrMoveIncomplete) {
		t.Fatalf("MoveCapture = %v, want ErrMoveIncomplete", err)
	}
	if h.body(source) != CaptureMarker("c_1")+"\nFirst." || h.body(target) != "" {
		t.Fatal("a move whose first write failed changed a body")
	}
}

// failingPutIfMatch fails the conditional write for one key.
type failingPutIfMatch struct {
	repository.Objects
	key string
}

func (f failingPutIfMatch) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if key == f.key {
		return errors.New("s3: 500 InternalError")
	}
	return f.Objects.PutIfMatch(ctx, key, body, contentType, etag)
}
