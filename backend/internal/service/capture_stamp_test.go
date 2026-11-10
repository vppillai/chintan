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

// A delete and a move write a note body outside the editor, so each stamps
// the row first and its refresh clears the stamp. These tests put an editor
// save, or a fault, into every gap between those steps.

// hookObjects calls after once, right after the next conditional write of a
// note body has landed: the gap between a cut and the refresh that follows.
type hookObjects struct {
	*memory.Objects
	after func()
}

func (o *hookObjects) PutIfMatch(ctx context.Context, key string, body []byte, ct, etag string) error {
	err := o.Objects.PutIfMatch(ctx, key, body, ct, etag)
	if err == nil && o.after != nil && strings.HasSuffix(key, "note.md") {
		f := o.after
		o.after = nil
		f()
	}
	return err
}

func (h *editHarness) unstamped(userID, noteID string) model.NoteIndex {
	h.t.Helper()
	n := h.get(userID, noteID)
	if n.AppendingCapture != "" {
		h.t.Fatalf("note %s still carries the stamp of %s; editor saves would wait out the lease", noteID, n.AppendingCapture)
	}
	return n
}

// An editor save that lands between DeleteCapture's cut and its refresh used
// to pass the version check — the cut moves the ETag, not the version — and
// write the deleted paragraph back from the draft, with no marker and no
// capture row.
func TestDeleteCaptureRefusesASaveThatLandsBetweenTheCutAndTheRefresh(t *testing.T) {
	h := newEditHarness(t)
	body := "typed intro\n\n" + CaptureMarker("c_1") + "\nFirst dictation.\n\n" + CaptureMarker("c_2") + "\nSecond dictation."
	note := h.note("u1", "Kitchen", body)
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	h.appended("u1", note.ID, "c_2", "2026-01-01T11:00:00.000000000Z")

	// The editor loaded the note before the delete and typed one more line.
	draft := StripCaptureMarkers(body) + "\n\ntyped afterwards"
	version := note.Version
	var saveErr error
	hooked := &hookObjects{Objects: h.objects}
	hooked.after = func() {
		_, saveErr = h.notes.UpdateNote(h.ctx, "u1", note.ID, NoteUpdates{Body: &draft, ExpectedVersion: &version})
	}
	captures := NewCaptureService(h.store, hooked).WithNoteCreator(h.notes)
	if err := captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if !errors.Is(saveErr, ErrAppendInProgress) {
		t.Errorf("the racing save = %v, want ErrAppendInProgress", saveErr)
	}
	if got := h.body(note); strings.Contains(got, "First dictation.") {
		t.Fatalf("the deleted recording's words are back in the note: %q", got)
	}

	// The stamp is gone with the refresh, and the same draft sent again is an
	// ordinary version conflict for the editor to reconcile.
	after := h.unstamped("u1", note.ID)
	if strings.Contains(after.SearchText, "first dictation") {
		t.Errorf("search text still holds the deleted words: %q", after.SearchText)
	}
	if _, err := h.notes.UpdateNote(h.ctx, "u1", note.ID, NoteUpdates{Body: &draft, ExpectedVersion: &version}); !errors.Is(err, repository.ErrVersionConflict) {
		t.Errorf("the stale draft after the delete = %v, want a version conflict", err)
	}
	fresh := "typed intro\n\nSecond dictation.\n\ntyped afterwards"
	if _, err := h.notes.UpdateNote(h.ctx, "u1", note.ID, NoteUpdates{Body: &fresh, ExpectedVersion: &after.Version}); err != nil {
		t.Errorf("a save on the version the delete left: %v", err)
	}
}

// The same gap on the source of a move left the paragraph in both notes.
func TestMoveCaptureRefusesASaveOnTheSourceBetweenItsCutAndItsRefresh(t *testing.T) {
	h := newEditHarness(t)
	body := "typed intro\n\n" + CaptureMarker("c_1") + "\nFirst dictation."
	src := h.note("u1", "Kitchen", body)
	dst := h.note("u1", "Garden", "")
	h.appended("u1", src.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	draft := StripCaptureMarkers(body) + "\n\ntyped afterwards"
	version := src.Version
	var saveErr error
	hooked := &hookObjects{Objects: h.objects}
	hooked.after = func() { // the target insert; wait for the source cut
		hooked.after = func() {
			_, saveErr = h.notes.UpdateNote(h.ctx, "u1", src.ID, NoteUpdates{Body: &draft, ExpectedVersion: &version})
		}
	}
	captures := NewCaptureService(h.store, hooked).WithNoteCreator(h.notes)
	if _, _, err := captures.MoveCapture(h.ctx, "u1", "c_1", dst.ID); err != nil {
		t.Fatalf("MoveCapture: %v", err)
	}
	if !errors.Is(saveErr, ErrAppendInProgress) {
		t.Errorf("the racing save = %v, want ErrAppendInProgress", saveErr)
	}
	if s := h.body(src); strings.Contains(s, "First dictation.") {
		t.Errorf("the moved paragraph is still in the source: %q", s)
	}
	if d := h.body(dst); !strings.Contains(d, "First dictation.") {
		t.Errorf("the moved paragraph is not in the target: %q", d)
	}
	h.unstamped("u1", src.ID)
	h.unstamped("u1", dst.ID)
}

// failPutNoteOnce fails the first PutNote, as a throttle would.
type failPutNoteOnce struct {
	repository.Store
	failed bool
}

func (s *failPutNoteOnce) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	if !s.failed {
		s.failed = true
		return model.NoteIndex{}, errors.New("throttled")
	}
	return s.Store.PutNote(ctx, tenantID, n)
}

// The refresh used to run only when this call cut the paragraph, so the retry
// of a delete whose refresh failed found nothing to cut and left the row as
// it was: the deleted words stayed searchable. Between the two attempts the
// stamp stands, so a save cannot write the paragraph back either.
func TestDeleteCaptureRetryAfterAFailedRefreshStillRefreshesTheIndex(t *testing.T) {
	h := newEditHarness(t)
	body := "typed intro\n\n" + CaptureMarker("c_1") + "\nFirst dictation."
	note := h.note("u1", "Kitchen", body)
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	flaky := NewCaptureService(&failPutNoteOnce{Store: h.store}, h.objects).WithNoteCreator(h.notes)
	if err := flaky.DeleteCapture(h.ctx, "u1", "c_1"); err == nil {
		t.Fatal("want the first delete to fail at the refresh")
	}
	draft := StripCaptureMarkers(body)
	if _, err := h.notes.UpdateNote(h.ctx, "u1", note.ID, NoteUpdates{Body: &draft, ExpectedVersion: &note.Version}); !errors.Is(err, ErrAppendInProgress) {
		t.Errorf("a save between the failed delete and its retry = %v, want ErrAppendInProgress", err)
	}

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if strings.Contains(h.body(note), "First dictation") {
		t.Fatalf("body still has the paragraph")
	}
	after := h.unstamped("u1", note.ID)
	if strings.Contains(after.SearchText, "first dictation") || after.Version == note.Version {
		t.Fatalf("the row was never refreshed: version %d→%d search=%q", note.Version, after.Version, after.SearchText)
	}
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_1"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("capture row after the retry: %v, want gone", err)
	}
}

// The row holds one stamp. A delete or a move that met another recording's
// live stamp and stamped over it would leave that append unguarded, so both
// are refused and change nothing.
func TestDeleteAndMoveAreRefusedWhileAnotherRecordingIsBeingAppended(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nFirst dictation."
	note := h.note("u1", "Kitchen", body)
	dst := h.note("u1", "Garden", "")
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	if _, err := h.store.StampNoteAppend(h.ctx, "u1", note.ID, "c_other", note.Version, h.captures.now()); err != nil {
		t.Fatal(err)
	}

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); !errors.Is(err, ErrAppendInProgress) {
		t.Errorf("DeleteCapture = %v, want ErrAppendInProgress", err)
	}
	if _, _, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", dst.ID); !errors.Is(err, ErrMoveIncomplete) {
		t.Errorf("MoveCapture = %v, want ErrMoveIncomplete", err)
	}
	if got := h.body(note); got != body {
		t.Errorf("source body = %q, want it untouched", got)
	}
	if got := h.body(dst); got != "" {
		t.Errorf("target body = %q, want it untouched", got)
	}
	if n := h.get("u1", note.ID); n.AppendingCapture != "c_other" {
		t.Errorf("the other recording's stamp = %q, want it standing", n.AppendingCapture)
	}
	h.unstamped("u1", dst.ID)
	if _, err := h.store.GetCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Errorf("capture row: %v, want it kept", err)
	}
}

// failBodyWrites refuses every conditional write of a note body.
type failBodyWrites struct{ *memory.Objects }

func (failBodyWrites) PutIfMatch(context.Context, string, []byte, string, string) error {
	return errors.New("s3 said no")
}

// A cut that fails hands its stamp back: the body is as it was, and a stamp
// left standing would refuse every save on the note for the whole lease.
func TestDeleteCaptureHandsItsStampBackWhenTheCutFails(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nFirst dictation."
	note := h.note("u1", "Kitchen", body)
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")

	broken := NewCaptureService(h.store, failBodyWrites{h.objects}).WithNoteCreator(h.notes)
	if err := broken.DeleteCapture(h.ctx, "u1", "c_1"); err == nil {
		t.Fatal("want the delete to fail at the cut")
	}
	if got := h.body(note); got != body {
		t.Errorf("body = %q, want it untouched", got)
	}
	after := h.unstamped("u1", note.ID)
	draft := "First dictation, edited."
	if _, err := h.notes.UpdateNote(h.ctx, "u1", note.ID, NoteUpdates{Body: &draft, ExpectedVersion: &after.Version}); err != nil {
		t.Errorf("a save after the failed delete: %v", err)
	}
}

// A capture that never reached the body leaves the note alone: no stamp, no
// version bump for an open editor to trip over.
func TestDeleteCaptureOfAnUnappendedRecordingDoesNotTouchItsNote(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Kitchen", "typed")
	c := h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	c.Status, c.AppendedAt = model.StatusFailed, 0
	if _, err := h.store.PutCapture(h.ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if after := h.unstamped("u1", note.ID); after.Version != note.Version {
		t.Errorf("version %d→%d, want the row untouched", note.Version, after.Version)
	}
}
