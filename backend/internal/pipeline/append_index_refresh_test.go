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

// failOnceOnGet fails the first plain Get of one key and passes everything
// else through. The append itself reads the body through GetWithETag, so the
// failure lands on the index refresh that follows the write — which is where
// an S3 5xx or a timeout at that moment lands too.
type failOnceOnGet struct {
	repository.Objects
	key    string
	mu     sync.Mutex
	failed bool
}

var errInducedObjectFault = errors.New("induced object store fault")

func (o *failOnceOnGet) Get(ctx context.Context, key string) ([]byte, error) {
	if key == o.key {
		o.mu.Lock()
		first := !o.failed
		o.failed = true
		o.mu.Unlock()
		if first {
			return nil, errInducedObjectFault
		}
	}
	return o.Objects.Get(ctx, key)
}

func (o *failOnceOnGet) didFail() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.failed
}

// The index row follows the body. When the body cannot be read after an
// append, the row must not be rebuilt from the one paragraph just appended —
// that made a transient S3 fault shrink the note's search text and snippet to
// that paragraph, silently and for good, because the capture was then marked
// appended and nothing retried. The invocation fails instead, and the retry
// finishes the append from the marker without writing the text again.
func TestAnUnreadableBodyAfterTheAppendFailsTheInvocationInsteadOfShrinkingTheIndex(t *testing.T) {
	objects := &failOnceOnGet{Objects: memory.NewObjects(), key: appendNoteKey}
	f := newAppendFixture(t, objects, nil)
	ctx := context.Background()

	// A note with a paragraph already in it, and indexed as such.
	const existing = "the paragraph that was already in the note"
	if err := objects.Put(ctx, appendNoteKey, []byte(existing+"\n"), "text/markdown"); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	note, err := f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	note.Snippet = service.Snippet(existing)
	note.SearchText = service.SearchText(existing)
	if _, err := f.store.PutNote(ctx, "user1", note); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	_, runErr := f.run(ctx)
	if !objects.didFail() {
		t.Fatal("the run never read the body for the index refresh; the test no longer reproduces the fault")
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the appended text appears %d times after the first attempt, want 1:\n%s", got, f.body(t))
	}
	if runErr == nil {
		t.Fatal("the run finished although the body could not be read for the index refresh; nothing would retry it")
	}
	if !errors.Is(runErr, errInducedObjectFault) {
		t.Fatalf("error = %v, want the induced fault to propagate so the invocation is retried", runErr)
	}

	after, err := f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if after.SearchText != service.SearchText(existing) || after.Snippet != service.Snippet(existing) {
		t.Fatalf("index after the failed read: search_text=%q snippet=%q; want the previous index untouched, not one rebuilt from the appended paragraph",
			after.SearchText, after.Snippet)
	}
	capture, err := f.store.GetCapture(ctx, "user1", "capture1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if capture.Status == model.StatusAppended {
		t.Fatal("the capture was marked appended over a stale index; nothing would ever refresh it")
	}

	// Lambda's retry: the marker is in the body, so the attempt is finished
	// from the index refresh on, and the text is not written again.
	final, err := f.run(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if final.Status != model.StatusAppended {
		t.Fatalf("status after the retry = %s, want appended", final.Status)
	}
	if got := strings.Count(f.body(t), appendedText); got != 1 {
		t.Fatalf("the appended text appears %d times after the retry, want exactly 1", got)
	}
	after, err = f.store.GetNote(ctx, "user1", "note1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if !strings.Contains(after.SearchText, "already in the note") || !strings.Contains(after.SearchText, appendedText) {
		t.Fatalf("search_text after the retry = %q; want both paragraphs", after.SearchText)
	}
}
