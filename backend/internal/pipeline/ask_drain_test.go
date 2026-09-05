package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// countingNoteReads records how the pipeline reads the notes: as one drain, or
// as pages that each cost a drain of the partition on the real store.
type countingNoteReads struct {
	repository.Store
	mu     sync.Mutex
	lists  int
	drains int
}

func (c *countingNoteReads) ListNotes(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Store.ListNotes(ctx, tenantID, opts)
}

func (c *countingNoteReads) DrainNotes(ctx context.Context, tenantID string, opts repository.DrainOptions) ([]model.NoteIndex, bool, error) {
	c.mu.Lock()
	c.drains++
	c.mu.Unlock()
	return c.Store.DrainNotes(ctx, tenantID, opts)
}

// Ask reads every note once, with its search text, to rank it. On DynamoDB a
// paged walk costs one drain of the partition per page — 2,000 notes with
// 32 KB of search text each is 640 MB before the model is asked — so the
// corpus is one drain, and the router's candidate window is read the same way.
func TestAskAndRoutingReadTheNotesInOneDrainNotPerPage(t *testing.T) {
	var counting *countingNoteReads
	h := newHarnessWrapping(t, nil, func(s repository.Store) repository.Store {
		counting = &countingNoteReads{Store: s}
		return counting
	}, harnessOpts{})
	for i := 0; i < 5; i++ {
		seedSearchableNote(t, h, "n"+string(rune('a'+i)), "Note", "the roof and the gutter", model.Now())
	}
	seedAsk(t, h, "a1", "what about the gutter", nil)

	if err := NewWorker(h.pipeline).Handle(context.Background(), askTask("user1", "a1")); err != nil {
		t.Fatalf("Handle(ask): %v", err)
	}
	if counting.drains != 1 || counting.lists != 0 {
		t.Fatalf("ask read the notes with %d drains and %d paged lists; want exactly one drain", counting.drains, counting.lists)
	}
	if got := getAsk(t, h, "a1"); got.NotesConsidered != 5 {
		t.Fatalf("notes considered = %d, want the 5 seeded; the drain must carry what the paged walk did", got.NotesConsidered)
	}

	// Routing candidates: the same one read.
	counting.drains, counting.lists = 0, 0
	if _, err := h.pipeline.decideTarget(context.Background(), "user1", "c1", "a transcript to route"); err != nil {
		t.Fatalf("decideTarget: %v", err)
	}
	if counting.drains != 1 || counting.lists != 0 {
		t.Fatalf("routing read the notes with %d drains and %d paged lists; want exactly one drain", counting.drains, counting.lists)
	}
}
