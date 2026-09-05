package repository_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// seedNotesOldestTouchedLast writes n notes whose creation order (the id, and
// so the base table's sort key) and touch order disagree completely: note_0000
// is the oldest created and the most recently touched, and every later note
// was touched one second earlier than the one before it.
func seedNotesOldestTouchedLast(t *testing.T, store *repository.DynamoStore, tenantID string, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		if _, err := store.PutNote(ctx, tenantID, model.NoteIndex{
			ID:        fmt.Sprintf("note_%04d", i),
			Title:     fmt.Sprintf("Note %d", i),
			UpdatedAt: model.FormatTime(base.Add(time.Duration(n-i) * time.Second)),
		}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}
}

// TestListNotesSeesEveryNoteNotOnlyTheMostRecentlyCreated is the bug the drain
// ceiling replaced a window with. The drain walks the base table newest
// CREATED first; with a bound of 500 a tenant with 600 notes saw the 500
// youngest ordered by touch, and the oldest note — the one dictated into this
// morning — was not in the library at all. The list has to read every note
// before it can order any of them.
func TestListNotesSeesEveryNoteNotOnlyTheMostRecentlyCreated(t *testing.T) {
	store, api := newTestStore(t)
	const total = 600
	seedNotesOldestTouchedLast(t, store, "tenant-a", total)
	// Stand in for the 1 MB response cap, so the drain has to follow
	// LastEvaluatedKey across several queries to see everything.
	api.pageSize = 128

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) == 0 || page.Items[0].ID != "note_0000" {
		t.Fatalf("the first listed note is %s, want note_0000: the oldest created note, touched most recently, "+
			"must lead the list however many younger notes there are", firstID(page.Items))
	}
	if page.Truncated {
		t.Fatalf("a %d-note tenant was reported truncated; the ceiling is %d", total, repository.MaxNotesDrained)
	}

	order, _ := pageThroughNotes(t, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return store.ListNotes(ctx, "tenant-a", opts)
	}, repository.MaxListLimit)
	if len(order) != total {
		t.Fatalf("paging through the list served %d notes, want all %d", len(order), total)
	}
	for i, id := range order {
		if want := fmt.Sprintf("note_%04d", i); id != want {
			t.Fatalf("position %d holds %s, want %s (most recently touched first, across every page)", i, id, want)
		}
	}
}

// TestListNotesReportsTheDrainCeiling pins the one degradation left: a tenant
// with more notes than MaxNotesDrained gets an ordered list over an incomplete
// set, and the page says so, so the service can log it and an operator can
// see the metric rather than hear about a missing note.
func TestListNotesReportsTheDrainCeiling(t *testing.T) {
	store, _ := newTestStore(t)
	seedNotesOldestTouchedLast(t, store, "tenant-a", repository.MaxNotesDrained+1)

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if !page.Truncated {
		t.Fatalf("%d notes were listed without Truncated being set", repository.MaxNotesDrained+1)
	}
	// Newest CREATED first is the drain's own order, so the one note the
	// ceiling dropped is note_0000 — the most recently touched. This is the
	// exact loss the warning exists to surface.
	if len(page.Items) == 0 || page.Items[0].ID != "note_0001" {
		t.Fatalf("the first listed note is %s, want note_0001 (note_0000 is past the ceiling)", firstID(page.Items))
	}
}

func firstID(notes []model.NoteIndex) string {
	if len(notes) == 0 {
		return "nothing"
	}
	return notes[0].ID
}
