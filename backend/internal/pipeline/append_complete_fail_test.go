package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// failOnceOnComplete fails the first CompleteCaptureAppend: the paragraph is
// in the body and the index refreshed, the capture is not yet appended.
type failOnceOnComplete struct {
	repository.Store
	mu     sync.Mutex
	failed bool
}

func (s *failOnceOnComplete) CompleteCaptureAppend(ctx context.Context, tenantID, captureID, token string) (model.CaptureIndex, error) {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	s.mu.Unlock()
	if first {
		return model.CaptureIndex{}, errors.New("induced completion failure")
	}
	return s.Store.CompleteCaptureAppend(ctx, tenantID, captureID, token)
}

// The note's stamp is what keeps an editor save off a paragraph whose capture
// is not yet marked appended. When the index refresh cleared it before the
// completion, a failed completion left the paragraph open to an edit; the
// edit moved the words away from the marker, both Lambda retries then failed
// with errAppendClaimHeld (a page), and the takeover after the lease wrote
// the dictation a second time. The stamp now outlives the completion, so the
// edit waits and the retry finishes the capture with the text once.
func TestAFailedCompletionKeepsTheStampSoAnEditCannotForceASecondAppend(t *testing.T) {
	base := memory.NewObjects()
	f := newAppendFixture(t, base, func(s repository.Store) repository.Store {
		return &failOnceOnComplete{Store: s}
	})
	ctx := context.Background()
	notes := service.NewNotesService(f.store, base).WithClock(f.h.clock.Now)

	if _, err := f.run(ctx); err == nil {
		t.Fatal("expected the induced completion failure to surface")
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("after the interrupted attempt the text appears %d times, want 1", got)
	}

	// The person rewrites the paragraph at the row's current version.
	current := mustGetNote(t, f.store, "user1", "note1")
	if _, err := editorSave(ctx, notes, current.Version, "my own words instead"); !errors.Is(err, service.ErrAppendInProgress) {
		t.Fatalf("an edit while the capture is not yet appended returned %v; want ErrAppendInProgress", err)
	}

	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status after the retry = %s, want appended", final.Status)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the dictated text appears %d times after the retry, want 1:\n%s", got, f.body(t))
	}
	if note := mustGetNote(t, f.store, "user1", "note1"); note.AppendingCapture != "" {
		t.Fatalf("stamp %q left on the note after the capture was appended", note.AppendingCapture)
	}
}
