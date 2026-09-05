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

// The clean-request stamp is the same kind of write as the append stamp — two
// named attributes under the row's version — with one difference: the version
// does not move, because nothing about the note's content changed. A stamp
// that bumped the version sent every open editor round a conflict with
// nothing to reconcile, and a whole-row write carried whatever cleaned_body
// the caller had in hand, which a list-projected note has not.
func TestStampCleanRequestTouchesOnlyTheStampAndLeavesTheVersionAlone(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		seeded, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: "n1", Title: "Kept", CleanedBody: "the stored view", SearchText: "kept search", UpdatedAt: model.Now(),
		})
		if err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		const at = "2026-09-05T12:00:00.000000000Z"

		stamped, err := store.StampCleanRequest(ctx, "tenant-a", "n1", model.NoteCleanStructured, at, seeded.Version)
		if err != nil {
			t.Fatalf("StampCleanRequest: %v", err)
		}
		if stamped.Version != seeded.Version {
			t.Fatalf("version after the stamp = %d, want %d unchanged", stamped.Version, seeded.Version)
		}
		if stamped.CleanedRequestedAt != at || stamped.CleanedRequestedMode != model.NoteCleanStructured {
			t.Fatalf("stamp = (%q, %q)", stamped.CleanedRequestedAt, stamped.CleanedRequestedMode)
		}
		got, err := store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if got.CleanedBody != "the stored view" || got.SearchText != "kept search" || got.Title != "Kept" {
			t.Fatalf("the stamp disturbed other attributes: %+v", got)
		}
		if got.CleanedRequestedAt != at {
			t.Fatalf("read back stamp %q, want %s", got.CleanedRequestedAt, at)
		}

		if _, err := store.StampCleanRequest(ctx, "tenant-a", "n1", model.NoteCleanPolished, at, seeded.Version+7); !errors.Is(err, repository.ErrVersionConflict) {
			t.Fatalf("stamp on a stale version: err = %v, want ErrVersionConflict", err)
		}
		if _, err := store.StampCleanRequest(ctx, "tenant-a", "missing", model.NoteCleanPolished, at, 0); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("stamp on a missing note: err = %v, want ErrNotFound", err)
		}

		// The clear takes back only the stamp it names.
		if err := store.ClearCleanStamp(ctx, "tenant-a", "n1", "2026-09-05T12:00:01.000000000Z"); err != nil {
			t.Fatalf("ClearCleanStamp(other): %v", err)
		}
		if got, _ := store.GetNote(ctx, "tenant-a", "n1"); got.CleanedRequestedAt != at {
			t.Fatal("a clear for another stamp removed this one")
		}
		if err := store.ClearCleanStamp(ctx, "tenant-a", "n1", at); err != nil {
			t.Fatalf("ClearCleanStamp: %v", err)
		}
		got, err = store.GetNote(ctx, "tenant-a", "n1")
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if got.CleanedRequestedAt != "" || got.CleanedRequestedMode != "" {
			t.Fatalf("stamp after the clear = (%q, %q), want empty", got.CleanedRequestedAt, got.CleanedRequestedMode)
		}
		if got.Version != seeded.Version {
			t.Fatalf("version after the clear = %d, want %d", got.Version, seeded.Version)
		}

		// A whole-row write that carried the stamp forward, then a clear: the
		// read must not resurrect the stamp from the row's record blob.
		if _, err := store.StampCleanRequest(ctx, "tenant-a", "n1", model.NoteCleanStructured, at, seeded.Version); err != nil {
			t.Fatalf("re-stamp: %v", err)
		}
		row, _ := store.GetNote(ctx, "tenant-a", "n1")
		row.Title = "Retitled with the stamp on the row"
		carried, err := store.PutNote(ctx, "tenant-a", row)
		if err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		if err := store.ClearCleanStamp(ctx, "tenant-a", "n1", at); err != nil {
			t.Fatalf("ClearCleanStamp: %v", err)
		}
		after, _ := store.GetNote(ctx, "tenant-a", "n1")
		if after.CleanedRequestedAt != "" {
			t.Fatalf("stamp %q came back from the record blob after the clear", after.CleanedRequestedAt)
		}
		if after.Version != carried.Version {
			t.Fatalf("version = %d, want %d", after.Version, carried.Version)
		}
	})
}

// One round trip for a page's worth of notes, true for the rows that exist
// and false — present, not absent — for the ones that do not, duplicates asked
// once.
func TestNotesExistAnswersASetInOneCall(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		for _, id := range []string{"n1", "n2"} {
			if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: id, Title: id, UpdatedAt: model.Now()}); err != nil {
				t.Fatalf("PutNote: %v", err)
			}
		}
		got, err := store.NotesExist(ctx, "tenant-a", []string{"n1", "missing", "n2", "n1"})
		if err != nil {
			t.Fatalf("NotesExist: %v", err)
		}
		want := map[string]bool{"n1": true, "n2": true, "missing": false}
		if len(got) != len(want) {
			t.Fatalf("NotesExist = %v, want %v", got, want)
		}
		for id, exists := range want {
			if got[id] != exists {
				t.Fatalf("NotesExist[%s] = %v, want %v", id, got[id], exists)
			}
		}
		// Another tenant's note is absent from this tenant's answer.
		other, err := store.NotesExist(ctx, "tenant-b", []string{"n1"})
		if err != nil || other["n1"] {
			t.Fatalf("NotesExist across tenants = %v, %v; want false", other, err)
		}
		empty, err := store.NotesExist(ctx, "tenant-a", nil)
		if err != nil || len(empty) != 0 {
			t.Fatalf("NotesExist(nil) = %v, %v", empty, err)
		}
	})
}

func TestNotesExistIsOneBatchGetProjectedToTheKey(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	seedNotes(t, store, "tenant-a", 5)
	api.batchGets, api.gets = nil, 0

	got, err := store.NotesExist(ctx, "tenant-a", []string{"note_000", "note_003", "nope"})
	if err != nil {
		t.Fatalf("NotesExist: %v", err)
	}
	if !got["note_000"] || !got["note_003"] || got["nope"] {
		t.Fatalf("NotesExist = %v", got)
	}
	if api.gets != 0 || len(api.batchGets) != 1 {
		t.Fatalf("NotesExist made %d GetItems and %d BatchGetItems; want one batch", api.gets, len(api.batchGets))
	}
	if p := *api.batchGets[0].RequestItems[tableName].ProjectionExpression; p != "sk" {
		t.Fatalf("projection = %q; nothing but the key should cross the wire", p)
	}
}
