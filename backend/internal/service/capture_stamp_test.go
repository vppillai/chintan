package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// The Recordings tab deletes a selection as one request per recording, all
// at once. Each delete stamps the note, so a sibling arriving inside the
// first one's stamp has to wait its turn: refused outright, a selection of
// N came back as one deleted and N-1 answered 409.
func TestASecondDeleteArrivingInsideTheFirstWaitsItsTurn(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nFirst dictation.\n\n" + CaptureMarker("c_2") + "\nSecond dictation."
	note := h.note("u1", "Kitchen", body)
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	h.appended("u1", note.ID, "c_2", "2026-01-01T11:00:00.000000000Z")

	// The sibling's request starts right after the first one's cut, while
	// its stamp stands, and is still running when the first one refreshes.
	second := make(chan error, 1)
	hooked := &hookObjects{Objects: h.objects}
	hooked.after = func() {
		go func() { second <- h.captures.DeleteCapture(h.ctx, "u1", "c_2") }()
		time.Sleep(50 * time.Millisecond) // long enough for the sibling to meet the stamp
	}
	first := NewCaptureService(h.store, hooked).WithNoteCreator(h.notes)
	if err := first.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second delete: %v, want it to wait out the first one's stamp", err)
	}
	if got := h.body(note); got != "" {
		t.Errorf("body = %q, want both paragraphs cut", got)
	}
	h.unstamped("u1", note.ID)
}

// slowRow adds a round trip's delay to the two calls of the stamp loop, so
// that siblings woken by one cleared stamp read the free row together and
// all but one lose the race for it, as they do against a real table.
type slowRow struct {
	repository.Store
	lag time.Duration
}

func (s slowRow) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	time.Sleep(s.lag)
	return s.Store.GetNote(ctx, tenantID, noteID)
}

func (s slowRow) StampNoteAppend(ctx context.Context, tenantID, noteID, captureID string, version int64, at time.Time) (model.NoteIndex, error) {
	time.Sleep(s.lag)
	return s.Store.StampNoteAppend(ctx, tenantID, noteID, captureID, version, at)
}

// A whole selection at once: every delete queues behind the others' stamps
// and each loses a stamp race most times one clears. The loop counted those
// losses across its waits and gave up at five, so of ten deletes several
// answered a version conflict while they were only waiting their turn.
func TestDeletingASelectionOfOneNotesRecordingsAtOnceDeletesThemAll(t *testing.T) {
	h := newEditHarness(t)
	ids := make([]string, 12)
	var body []string
	for i := range ids {
		ids[i] = fmt.Sprintf("c_%02d", i)
		body = append(body, CaptureMarker(ids[i])+"\nDictation "+ids[i]+".")
	}
	note := h.note("u1", "Kitchen", "typed intro\n\n"+strings.Join(body, "\n\n"))
	for i, id := range ids {
		h.appended("u1", note.ID, id, fmt.Sprintf("2026-01-01T10:%02d:00.000000000Z", i))
	}

	captures := NewCaptureService(slowRow{h.store, 5 * time.Millisecond}, h.objects).WithNoteCreator(h.notes)
	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() { errs <- captures.DeleteCapture(h.ctx, "u1", id) }()
	}
	for range ids {
		if err := <-errs; err != nil {
			t.Errorf("a delete of the selection: %v", err)
		}
	}
	if got := h.body(note); got != "typed intro" {
		t.Errorf("body = %q, want the typed line alone", got)
	}
	if after := h.unstamped("u1", note.ID); strings.Contains(after.SearchText, "dictation") {
		t.Errorf("search text still holds deleted words: %q", after.SearchText)
	}
}

// conflictingStamps answers the first n stamps with a version conflict.
type conflictingStamps struct {
	repository.Store
	n int
}

func (s *conflictingStamps) StampNoteAppend(ctx context.Context, tenantID, noteID, captureID string, version int64, at time.Time) (model.NoteIndex, error) {
	if s.n > 0 {
		s.n--
		return model.NoteIndex{}, repository.ErrVersionConflict
	}
	return s.Store.StampNoteAppend(ctx, tenantID, noteID, captureID, version, at)
}

// The loop's bound is on races lost in a row: a wait in between means the
// writer is queueing behind others, not failing, and starts the count again.
func TestStampNoteForBodyWriteCountsLostRacesBetweenWaitsNotAcrossThem(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Kitchen", "typed")
	lost := 2 * (MaxIndexRefreshAttempts - 1)
	store := &conflictingStamps{Store: h.store, n: lost}
	reads := 0
	err := StampNoteForBodyWrite(h.ctx, store, "u1", note.ID, "c_1", time.Now, func(model.NoteIndex) (bool, error) {
		reads++
		return reads == MaxIndexRefreshAttempts, nil // one wait, half way through the losses
	})
	if err != nil {
		t.Fatalf("StampNoteForBodyWrite = %v after %d lost races with a wait between the halves, want the stamp", err, lost)
	}
	if got := h.get("u1", note.ID).AppendingCapture; got != "c_1" {
		t.Errorf("stamp = %q, want c_1", got)
	}

	// With no wait between them the same bound still ends the attempt.
	store.n = MaxIndexRefreshAttempts
	if err := StampNoteForBodyWrite(h.ctx, store, "u1", note.ID, "c_1", time.Now, func(model.NoteIndex) (bool, error) { return false, nil }); !errors.Is(err, repository.ErrVersionConflict) {
		t.Errorf("StampNoteForBodyWrite = %v after %d races lost in a row, want the conflict", err, MaxIndexRefreshAttempts)
	}
}

// The wait is for a write in flight. A stamp that has stood past it was left
// by a writer that died before its refresh; the row holds one stamp, and a
// delete or a move that stamped over it would leave that write unguarded, so
// both are refused, at once, and change nothing.
func TestDeleteAndMoveAreRefusedUnderAnotherRecordingsStandingStamp(t *testing.T) {
	h := newEditHarness(t)
	body := CaptureMarker("c_1") + "\nFirst dictation."
	note := h.note("u1", "Kitchen", body)
	dst := h.note("u1", "Garden", "")
	h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	if _, err := h.store.StampNoteAppend(h.ctx, "u1", note.ID, "c_other", note.Version, h.captures.now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()

	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); !errors.Is(err, ErrAppendInProgress) {
		t.Errorf("DeleteCapture = %v, want ErrAppendInProgress", err)
	}
	if _, _, err := h.captures.MoveCapture(h.ctx, "u1", "c_1", dst.ID); !errors.Is(err, ErrMoveIncomplete) {
		t.Errorf("MoveCapture = %v, want ErrMoveIncomplete", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("the refusals took %s; a stamp that old is not waited on", waited)
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

// A recording whose retranscription failed keeps its paragraph, so its delete
// stamps and cuts like any other. When that delete's refresh failed, the
// repeat found no marker and a status that is not appended, skipped the
// refresh, and left the stamp refusing saves on the note for the lease.
func TestDeleteRetryOfAFailedRecordingClearsTheStampItsFirstAttemptLeft(t *testing.T) {
	h := newEditHarness(t)
	body := "typed intro\n\n" + CaptureMarker("c_1") + "\nFirst dictation."
	note := h.note("u1", "Kitchen", body)
	c := h.appended("u1", note.ID, "c_1", "2026-01-01T10:00:00.000000000Z")
	c.Status, c.AppendedAt = model.StatusFailed, 0
	if _, err := h.store.PutCapture(h.ctx, c); err != nil {
		t.Fatal(err)
	}

	flaky := NewCaptureService(&failPutNoteOnce{Store: h.store}, h.objects).WithNoteCreator(h.notes)
	if err := flaky.DeleteCapture(h.ctx, "u1", "c_1"); err == nil {
		t.Fatal("want the first delete to fail at the refresh")
	}
	if err := h.captures.DeleteCapture(h.ctx, "u1", "c_1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if after := h.unstamped("u1", note.ID); strings.Contains(after.SearchText, "first dictation") {
		t.Errorf("search text still holds the deleted words: %q", after.SearchText)
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
