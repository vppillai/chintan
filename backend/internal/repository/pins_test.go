package repository_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The pinned tier on the real store's ordering: pin_rank ascending, a tie
// broken by the more recently pinned note, above the touch order, with the
// cursor carrying a page boundary inside the tier and across it.
func TestPinnedNotesLeadTheListInRankOrderAcrossPages(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	// note_0000 is the most recently touched, note_0005 the least.
	seedNotesOldestTouchedLast(t, store, "tenant-a", 6)

	base := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	pin := func(id string, rank int64, at time.Time) {
		t.Helper()
		n, err := store.GetNote(ctx, "tenant-a", id)
		if err != nil {
			t.Fatal(err)
		}
		n.PinnedAt = model.FormatTime(at)
		n.PinRank = rank
		if _, err := store.PutNote(ctx, "tenant-a", n); err != nil {
			t.Fatal(err)
		}
	}
	pin("note_0004", 1000, base)
	pin("note_0002", 0, base.Add(time.Minute))
	pin("note_0005", 1000, base.Add(2*time.Minute)) // same rank as note_0004, pinned later

	want := []string{"note_0002", "note_0005", "note_0004", "note_0000", "note_0001", "note_0003"}
	list := func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return store.ListNotes(ctx, "tenant-a", opts)
	}
	order, pages := pageThroughNotes(t, list, 2)
	if fmt.Sprint(order) != fmt.Sprint(want) || pages != 3 {
		t.Fatalf("order = %v over %d pages, want %v over 3", order, pages, want)
	}

	// The list projection carries the pin: a listed note says it is pinned
	// without a second read.
	page, err := list(ctx, repository.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Items[0].Pinned() || page.Items[0].PinRank != 0 {
		t.Fatalf("listed note = %+v, want pinned at rank 0", page.Items[0])
	}

	// A cursor minted before the tier existed — a touch instant and an id —
	// still resumes in the unpinned tier rather than being refused.
	legacy, err := json.Marshal(map[string]string{
		"pk": "USER#tenant-a", "shelf": "ACTIVE", "dir": "desc",
		"at": model.FormatTime(time.Date(2026, 8, 1, 0, 0, 5, 0, time.UTC)), "id": "note_0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err = list(ctx, repository.ListOptions{Cursor: base64.RawURLEncoding.EncodeToString(legacy), Limit: 10})
	if err != nil {
		t.Fatalf("legacy cursor: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "note_0003" {
		t.Fatalf("legacy cursor resumed at %s, want note_0003 alone", firstID(page.Items))
	}
}
