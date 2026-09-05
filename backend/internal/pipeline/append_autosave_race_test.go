package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// aroundBodyWrite runs hooks around the append's one conditional PUT of the
// note body — before it (between the stamp and the write) and after it
// (between the write and the index refresh). Those are the two instants an
// editor save could land at and, until the stamp existed, delete the
// paragraph: the row's version had not moved yet, and the body's ETag was
// read after the write.
type aroundBodyWrite struct {
	repository.Objects
	key    string
	before func()
	after  func()
	mu     sync.Mutex
	fired  bool
}

func (o *aroundBodyWrite) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	o.mu.Lock()
	first := key == o.key && !o.fired
	if first {
		o.fired = true
	}
	o.mu.Unlock()
	if first && o.before != nil {
		o.before()
	}
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	err := o.Objects.PutIfMatch(ctx, key, body, contentType, etag)
	if first && o.after != nil {
		o.after()
	}
	return err
}

func (o *aroundBodyWrite) didFire() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.fired
}

// The client's half: an editor holding the note at the version it read before
// the append began, saving text of its own.
func editorSave(ctx context.Context, notes *service.NotesService, version int64, text string) (model.NoteIndex, error) {
	v := version
	return notes.UpdateNote(ctx, "user1", "note1", service.NoteUpdates{Body: &text, ExpectedVersion: &v})
}

// An autosave that read the row before the append and lands between the
// worker's body write and its index refresh passed both of the editor's checks
// — the version (not yet bumped by the refresh) and the ETag (read after the
// write) — and rewrote the body from text that predates the paragraph, with the
// marker carried so nothing ever re-appended it. The stamp the append now
// leaves on the row before writing is what the save meets instead: a 409 that
// says an append is in progress, and the paragraph is in the note when the run
// finishes.
func TestAnAutosaveLandingBetweenTheBodyWriteAndTheIndexRefreshIsRefusedNotApplied(t *testing.T) {
	base := memory.NewObjects()
	hook := &aroundBodyWrite{Objects: base, key: appendNoteKey}
	f := newAppendFixture(t, hook, nil)
	ctx := context.Background()
	// The stamp's age is judged against the clock the pipeline stamped with.
	notes := service.NewNotesService(f.store, base).WithClock(f.h.clock.Now)

	const typed = "typed in the editor before the recording landed"
	seeded, err := editorSave(ctx, notes, 1, typed)
	if err != nil {
		t.Fatalf("seed body: %v", err)
	}

	var saveErr error
	var saved model.NoteIndex
	hook.after = func() {
		// Read the row before the append (version as seeded), body after the
		// write: the interleaving the version check cannot see.
		saved, saveErr = editorSave(ctx, notes, seeded.Version, typed+" and a little more")
	}

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hook.didFire() {
		t.Fatal("the run never wrote the body through the hook; the test no longer interleaves anything")
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("capture status = %s, want appended", final.Status)
	}

	if !errors.Is(saveErr, service.ErrAppendInProgress) {
		t.Fatalf("the interleaved save returned %v; want ErrAppendInProgress so the client waits and repeats it", saveErr)
	}
	if saved.Version != seeded.Version+1 {
		t.Fatalf("the 409 carries current_version %d, want the stamped version %d", saved.Version, seeded.Version+1)
	}
	body := f.body(t)
	if !strings.Contains(body, appendedText) {
		t.Fatalf("the dictated paragraph is gone from the body:\n%s", body)
	}
	if !strings.Contains(body, typed) {
		t.Fatalf("the editor's earlier text is gone from the body:\n%s", body)
	}
	if strings.Contains(body, "a little more") {
		t.Fatalf("the refused save was applied to the body anyway:\n%s", body)
	}

	// The stamp is gone with the refresh, so the repeated save goes through as
	// an ordinary conflict — the version has moved for a body that now differs
	// — and the client's normal conflict handling takes over.
	after, err := f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if after.AppendingCapture != "" {
		t.Fatalf("the stamp %q outlived the index refresh", after.AppendingCapture)
	}
	if _, err := editorSave(ctx, notes, seeded.Version, typed+" and a little more"); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("the repeated save after the append returned %v; want a plain ErrVersionConflict to reconcile", err)
	}
}

// The client's reaction to a version-only conflict is to re-read and, finding
// the same text at a newer version, re-send at that version at once. Against
// the stamp's bump that re-send arrives with the CURRENT version and, having
// read its ETag after the body write, would pass every check the version and
// the ETag can offer. Only the stamp says no. Without it, the pre-bump fix
// would have turned a rare race into a deterministic retry into the window.
func TestASameTextResaveAtTheStampedVersionIsRefusedNotWritten(t *testing.T) {
	base := memory.NewObjects()
	hook := &aroundBodyWrite{Objects: base, key: appendNoteKey}
	f := newAppendFixture(t, hook, nil)
	ctx := context.Background()
	// The stamp's age is judged against the clock the pipeline stamped with.
	notes := service.NewNotesService(f.store, base).WithClock(f.h.clock.Now)

	const typed = "the same text the editor already holds"
	if _, err := editorSave(ctx, notes, 1, typed); err != nil {
		t.Fatalf("seed body: %v", err)
	}

	// The re-read lands after the stamp and before the body write: the body is
	// unchanged, the version is one higher.
	var reread service.NoteDetail
	var rereadErr error
	hook.before = func() {
		reread, rereadErr = notes.GetNoteDetail(ctx, "user1", "note1")
	}
	// The re-send lands after the body write and before the index refresh.
	var saveErr error
	hook.after = func() {
		_, saveErr = editorSave(ctx, notes, reread.Version, reread.Body)
	}

	if _, err := f.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rereadErr != nil {
		t.Fatalf("re-read: %v", rereadErr)
	}
	if reread.Body != typed {
		t.Fatalf("the re-read saw %q; the test meant to catch the body before the write", reread.Body)
	}
	if reread.AppendingCapture != "capture1" {
		t.Fatalf("the re-read row carries stamp %q, want capture1: the stamp was not on the row before the body write", reread.AppendingCapture)
	}
	if !errors.Is(saveErr, service.ErrAppendInProgress) {
		t.Fatalf("the same-text re-save at the stamped version returned %v; want ErrAppendInProgress — the version matched and only the stamp could refuse it", saveErr)
	}
	if body := f.body(t); !strings.Contains(body, appendedText) {
		t.Fatalf("the re-save erased the dictated paragraph:\n%s", body)
	}
}

// bumpNoteOnClaim moves the note's version between run()'s read of the row and
// the append's stamp, as a title edit during transcription does. The stamp
// must re-read and land, not fail the capture on a stale version.
type bumpNoteOnClaim struct {
	repository.Store
	bumped bool
}

func (s *bumpNoteOnClaim) ClaimCaptureAppend(ctx context.Context, tenantID, captureID, token string) (bool, model.CaptureIndex, error) {
	if !s.bumped {
		s.bumped = true
		note, err := s.Store.GetNote(ctx, tenantID, "note1")
		if err != nil {
			return false, model.CaptureIndex{}, err
		}
		note.Title = "Retitled while the recording was transcribing"
		if _, err := s.Store.PutNote(ctx, tenantID, note); err != nil {
			return false, model.CaptureIndex{}, err
		}
	}
	return s.Store.ClaimCaptureAppend(ctx, tenantID, captureID, token)
}

func TestTheAppendStampReReadsWhenTheRowMovedSinceTheRunBegan(t *testing.T) {
	var bump *bumpNoteOnClaim
	f := newAppendFixture(t, memory.NewObjects(), func(s repository.Store) repository.Store {
		bump = &bumpNoteOnClaim{Store: s}
		return bump
	})
	ctx := context.Background()

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !bump.bumped {
		t.Fatal("the row was never moved; the test proves nothing")
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status = %s, want appended", final.Status)
	}
	note, err := f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if note.Title != "Retitled while the recording was transcribing" {
		t.Fatalf("title = %q; the append overwrote the concurrent edit", note.Title)
	}
	if note.AppendingCapture != "" {
		t.Fatalf("stamp %q left on the row after a finished append", note.AppendingCapture)
	}
}

// failBodyWrite makes the append's conditional PUT fail outright, so the append
// hands its claim back. The stamp has to go back with it, or the editor is
// locked out of the body for the length of the claim lease over an append that
// is not coming.
type failBodyWrite struct {
	repository.Objects
	key string
}

var errInducedBodyWriteFault = errors.New("induced body write fault")

func (o *failBodyWrite) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if key == o.key {
		return errInducedBodyWriteFault
	}
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	return o.Objects.PutIfMatch(ctx, key, body, contentType, etag)
}

func TestAnAppendThatHandsItsClaimBackTakesItsStampWithIt(t *testing.T) {
	base := memory.NewObjects()
	f := newAppendFixture(t, &failBodyWrite{Objects: base, key: appendNoteKey}, nil)
	ctx := context.Background()
	notes := service.NewNotesService(f.store, base).WithClock(f.h.clock.Now)

	if _, err := f.run(ctx); !errors.Is(err, errInducedBodyWriteFault) {
		t.Fatalf("run err = %v, want the induced fault to propagate", err)
	}
	note, err := f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if note.AppendingCapture != "" {
		t.Fatalf("stamp %q left on the row after the claim was released", note.AppendingCapture)
	}
	if _, err := editorSave(ctx, notes, note.Version, "the editor carries on"); err != nil {
		t.Fatalf("a save after the released append was refused: %v", err)
	}
}

// The row holds one stamp. Two captures appending to one note must not stamp
// over each other while both are writing, or the second's refresh would clear
// the stamp the first still relies on; so an append waits for a fresh stamp
// that is not its own, and does not wait for one whose holder is long gone.
func TestAnAppendWaitsForAnotherCapturesFreshStampAndNotForAStaleOne(t *testing.T) {
	t.Run("fresh stamp: waits until it clears", func(t *testing.T) {
		f := newAppendFixture(t, memory.NewObjects(), nil)
		ctx := context.Background()
		note := mustGetNote(t, f.store, "user1", "note1")
		if _, err := f.store.StampNoteAppend(ctx, "user1", "note1", "capture0", note.Version, f.h.clock.Now()); err != nil {
			t.Fatalf("stamp for capture0: %v", err)
		}

		released := make(chan struct{})
		go func() {
			// capture0's index refresh, some time later.
			<-time.After(3 * appendStampPoll)
			if err := f.store.ClearNoteAppend(ctx, "user1", "note1", "capture0"); err != nil {
				t.Errorf("ClearNoteAppend: %v", err)
			}
			close(released)
		}()

		final, err := f.run(ctx)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		select {
		case <-released:
		default:
			t.Fatal("the append finished before capture0's stamp was cleared; it stamped over an append in flight")
		}
		if final.Status != model.StatusAppended {
			t.Fatalf("status = %s, want appended", final.Status)
		}
		after := mustGetNote(t, f.store, "user1", "note1")
		if after.AppendingCapture != "" {
			t.Fatalf("stamp %q left on the row", after.AppendingCapture)
		}
	})

	t.Run("stale stamp: stamps over it at once", func(t *testing.T) {
		f := newAppendFixture(t, memory.NewObjects(), nil)
		ctx := context.Background()
		note := mustGetNote(t, f.store, "user1", "note1")
		dead := f.h.clock.Now().Add(-appendStampWait - time.Second)
		if _, err := f.store.StampNoteAppend(ctx, "user1", "note1", "capture0", note.Version, dead); err != nil {
			t.Fatalf("stamp for capture0: %v", err)
		}

		began := time.Now()
		final, err := f.run(ctx)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if final.Status != model.StatusAppended {
			t.Fatalf("status = %s, want appended", final.Status)
		}
		if waited := time.Since(began); waited > appendStampWait/2 {
			t.Fatalf("the append waited %s on a stamp whose holder died long ago", waited)
		}
	})
}

// A capture whose destination was purged before the worker reached it is not
// an infrastructure fault: retrying cannot bring the note back, and
// dead-lettering it raised the alarm for a question only the person can
// answer. It becomes needs_target, unfiled, with its transcript and cleaned
// text kept, and the person's choice of a new note resumes the pipeline.
func TestACaptureWhoseDestinationWasPurgedAsksForANewNoteInsteadOfDeadLettering(t *testing.T) {
	f := newAppendFixture(t, memory.NewObjects(), nil)
	ctx := context.Background()
	if err := f.store.DeleteNote(ctx, "user1", "note1"); err != nil {
		t.Fatalf("purge the note: %v", err)
	}

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("run returned %v; a purged destination must not fail the invocation into the dead-letter queue", err)
	}
	if final.Status != model.StatusNeedsTarget || final.NoteID != "" || final.TargetSource != "" {
		t.Fatalf("capture = status %s note %q source %q; want needs_target, unfiled", final.Status, final.NoteID, final.TargetSource)
	}
	if final.CleanKey == "" {
		t.Fatal("the cleaned text was dropped; the person's answer has nothing to resume from")
	}
	// It is where the "which note?" screen looks: the unrouted list.
	unrouted, err := f.store.ListCapturesByNote(ctx, "user1", "", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(unrouted.Items) != 1 || unrouted.Items[0].ID != "capture1" {
		t.Fatalf("unrouted captures = %v; want the capture waiting for a note", unrouted.Items)
	}
}
