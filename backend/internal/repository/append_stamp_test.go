package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The append stamp is what lets an editor save tell "the version moved because
// a paragraph is on its way into the body" from "the version moved because
// somebody else saved". It has to bump the version, name the capture, touch
// nothing else, and be conditional on the version the worker read.
func TestStampNoteAppendBumpsTheVersionAndNamesTheCapture(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		seeded, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: "n1", Title: "Kept", Snippet: "kept snippet", SearchText: "kept search", CleanedBody: "kept view",
			UpdatedAt: model.Now(),
		})
		if err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

		stamped, err := store.StampNoteAppend(ctx, "tenant-a", "n1", "cap1", seeded.Version, at)
		if err != nil {
			t.Fatalf("StampNoteAppend: %v", err)
		}
		if stamped.Version != seeded.Version+1 {
			t.Fatalf("version after the stamp = %d, want %d", stamped.Version, seeded.Version+1)
		}
		if stamped.AppendingCapture != "cap1" || stamped.AppendingAt != model.FormatTime(at) {
			t.Fatalf("stamp = (%q, %q), want (cap1, %s)", stamped.AppendingCapture, stamped.AppendingAt, model.FormatTime(at))
		}

		got, err := store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if got.Version != seeded.Version+1 || got.AppendingCapture != "cap1" {
			t.Fatalf("read back version=%d stamp=%q; the stamp did not reach the row", got.Version, got.AppendingCapture)
		}
		// Named attributes only: the large fields the row carries are exactly
		// as they were, not rewritten from whatever the worker had in hand.
		if got.Title != "Kept" || got.Snippet != "kept snippet" || got.SearchText != "kept search" || got.CleanedBody != "kept view" {
			t.Fatalf("the stamp disturbed other attributes: %+v", got)
		}

		// A stale version is a lost race, and the worker re-reads.
		if _, err := store.StampNoteAppend(ctx, "tenant-a", "n1", "cap2", seeded.Version, at); !errors.Is(err, repository.ErrVersionConflict) {
			t.Fatalf("stamp on a stale version: err = %v, want ErrVersionConflict", err)
		}
		// A note that is gone is gone, not a conflict to retry.
		if _, err := store.StampNoteAppend(ctx, "tenant-a", "missing", "cap1", 0, at); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("stamp on a missing note: err = %v, want ErrNotFound", err)
		}
	})
}

// The stamp comes off in two ways. The append that wrote clears it through the
// whole-row PutNote of its index refresh; the append that did NOT write clears
// it with ClearNoteAppend, which must leave a stamp another capture has since
// written alone and must not move the version — nothing about the body changed.
func TestClearNoteAppendRemovesOnlyItsOwnStamp(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		seeded, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: "n1", Title: "Note", UpdatedAt: model.Now()})
		if err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		stamped, err := store.StampNoteAppend(ctx, "tenant-a", "n1", "cap1", seeded.Version, time.Now())
		if err != nil {
			t.Fatalf("StampNoteAppend: %v", err)
		}

		// Another capture's stamp is not this one's to clear.
		if err := store.ClearNoteAppend(ctx, "tenant-a", "n1", "someone-else"); err != nil {
			t.Fatalf("ClearNoteAppend(other): %v", err)
		}
		got, err := store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if got.AppendingCapture != "cap1" {
			t.Fatalf("a clear for another capture removed cap1's stamp")
		}

		if err := store.ClearNoteAppend(ctx, "tenant-a", "n1", "cap1"); err != nil {
			t.Fatalf("ClearNoteAppend: %v", err)
		}
		got, err = store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if got.AppendingCapture != "" || got.AppendingAt != "" {
			t.Fatalf("stamp after the clear = (%q, %q), want empty", got.AppendingCapture, got.AppendingAt)
		}
		if got.Version != stamped.Version {
			t.Fatalf("version after the clear = %d, want %d unchanged", got.Version, stamped.Version)
		}

		// Clearing what is already clear, or a note that is gone, is nothing.
		if err := store.ClearNoteAppend(ctx, "tenant-a", "n1", "cap1"); err != nil {
			t.Fatalf("second clear: %v", err)
		}
		if err := store.ClearNoteAppend(ctx, "tenant-a", "missing", "cap1"); err != nil {
			t.Fatalf("clear on a missing note: %v", err)
		}

		// The whole-row write every other caller makes, with the fields empty,
		// clears the stamp too — that is how the index refresh does it.
		if _, err := store.StampNoteAppend(ctx, "tenant-a", "n1", "cap3", got.Version, time.Now()); err != nil {
			t.Fatalf("re-stamp: %v", err)
		}
		row, err := store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		row.AppendingCapture, row.AppendingAt = "", ""
		if _, err := store.PutNote(ctx, "tenant-a", row); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		after, err := store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if after.AppendingCapture != "" {
			t.Fatalf("PutNote with an empty stamp left %q on the row", after.AppendingCapture)
		}
	})
}
