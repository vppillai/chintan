package repository_test

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// A note's colour is promoted and in the list projection, so Home's rows
// carry it without a second read; cleared, the attribute is gone and the row
// reads back as no colour.
func TestNoteColorIsListedAndClearedWithTheAttribute(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	seedNotesOldestTouchedLast(t, store, "tenant-a", 1)

	n, err := store.GetNote(ctx, "tenant-a", "note_0000")
	if err != nil {
		t.Fatal(err)
	}
	n.Color = "mint"
	if n, err = store.PutNote(ctx, "tenant-a", n); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Items[0].Color; got != "mint" {
		t.Fatalf("listed colour = %q, want mint", got)
	}

	n.Color = ""
	if _, err := store.PutNote(ctx, "tenant-a", n); err != nil {
		t.Fatal(err)
	}
	item, err := repository.NoteItemAttributes("tenant-a", n)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["color"]; ok {
		t.Fatal("an uncoloured row carries a color attribute")
	}
	page, err = store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Items[0].Color; got != "" {
		t.Fatalf("listed colour after clearing = %q, want none", got)
	}
}
