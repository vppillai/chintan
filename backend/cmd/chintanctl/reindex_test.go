package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// putLegacyNote writes a note item the way it looked before gsi2 existed:
// carrying its promoted attributes and no index key at all.
func putLegacyNote(part *fakePartition, tenantID, id, updatedAt, deletedAt string) {
	blob, _ := json.Marshal(model.NoteIndex{
		ID: id, Title: "Note " + id, UpdatedAt: updatedAt, DeletedAt: deletedAt,
	})
	s := func(v string) AttrValue { return AttrValue{S: &v} }
	part.items["USER#"+tenantID] = orMake(part.items["USER#"+tenantID])
	part.items["USER#"+tenantID]["NOTE#"+id] = Item{
		"pk": s("USER#" + tenantID), "sk": s("NOTE#" + id),
		"type": s("note"), "note_id": s(id), "title": s("Note " + id),
		"updated_at": s(updatedAt), "deleted_at": s(deletedAt),
		"data": s(string(blob)),
	}
}

// seedBlob gives the tenant an S3 prefix, which is how resolveTenants
// discovers tenants without scanning the table.
func seedBlob(t *testing.T, blobs *fakeBlobs, key string) {
	t.Helper()
	if err := blobs.Put(context.Background(), key, strings.NewReader("x"), 1, "text/plain"); err != nil {
		t.Fatalf("seed blob: %v", err)
	}
}

func orMake(m map[string]Item) map[string]Item {
	if m == nil {
		return map[string]Item{}
	}
	return m
}

// TestReindexWritesTheIndexKeysOntoNotesThatLackThem is the migration this
// command exists for. A note written before gsi2 existed is not in the index —
// DynamoDB backfills a new index only from items already carrying its key
// attributes — so the notes list, which reads that index, shows nothing at all
// until each note is rewritten.
func TestReindexWritesTheIndexKeysOntoNotesThatLackThem(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	putLegacyNote(part, "user1", "note_a", "2026-08-01T00:00:00.000000000Z", "")
	putLegacyNote(part, "user1", "note_b", "2026-08-02T00:00:00.000000000Z", "2026-08-03T00:00:00.000000000Z")

	res, err := runReindex(context.Background(), e, []string{"user1"}, true)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if res.Notes != 2 || res.Written != 2 {
		t.Fatalf("examined %d and wrote %d, want 2 and 2", res.Notes, res.Written)
	}

	for _, tc := range []struct{ id, shelf, sk string }{
		{"note_a", "ACTIVE", "2026-08-01T00:00:00.000000000Z"},
		// A note with a deletion stamp belongs on the archived shelf, or the
		// archive list cannot find it and the active list shows it.
		{"note_b", "ARCHIVED", "2026-08-02T00:00:00.000000000Z"},
	} {
		it := part.items["USER#user1"]["NOTE#"+tc.id]
		wantPK := "TENANT#user1#NOTES#" + tc.shelf
		if got := it.Str("gsi2pk"); got != wantPK {
			t.Errorf("%s gsi2pk = %q, want %q", tc.id, got, wantPK)
		}
		if got := it.Str("gsi2sk"); got != tc.sk {
			t.Errorf("%s gsi2sk = %q, want %q", tc.id, got, tc.sk)
		}
		// The record itself must be untouched: a version bump here would hand a
		// client holding the previous one a conflict for a change nobody made.
		if got := it.Str("title"); got != "Note "+tc.id {
			t.Errorf("%s title = %q; reindex rewrote the record", tc.id, got)
		}
	}
}

// TestReindexIsIdempotentAndReportsNothingToDo keeps a second run from being a
// second migration. It is safe to run twice, and it must say so rather than
// reporting the same work again.
func TestReindexIsIdempotentAndReportsNothingToDo(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	putLegacyNote(part, "user1", "note_a", "2026-08-01T00:00:00.000000000Z", "")

	if _, err := runReindex(context.Background(), e, []string{"user1"}, true); err != nil {
		t.Fatalf("first run: %v", err)
	}
	writesAfterFirst := part.puts

	res, err := runReindex(context.Background(), e, []string{"user1"}, true)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Written != 0 {
		t.Errorf("second run reindexed %d notes, want 0", res.Written)
	}
	if part.puts != writesAfterFirst {
		t.Errorf("second run performed %d extra writes, want none", part.puts-writesAfterFirst)
	}
}

// TestReindexWithoutApplyWritesNothing holds it to the same dry-run default as
// every other chintanctl command that can change data.
func TestReindexWithoutApplyWritesNothing(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	putLegacyNote(part, "user1", "note_a", "2026-08-01T00:00:00.000000000Z", "")

	res, err := runReindex(context.Background(), e, []string{"user1"}, false)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if res.Written != 1 {
		t.Errorf("reported %d notes to reindex, want 1", res.Written)
	}
	if part.puts != 0 {
		t.Errorf("a dry run performed %d writes", part.puts)
	}
	if got := part.items["USER#user1"]["NOTE#note_a"].Str("gsi2pk"); got != "" {
		t.Errorf("a dry run wrote gsi2pk = %q", got)
	}
}

// TestReindexDerivesTheSameKeysAsTheWritePath is the drift guard. The command
// and the store must agree exactly about the shelf and the timestamp
// normalisation, and neither disagreement would error — a note would simply
// stop appearing in a list.
func TestReindexDerivesTheSameKeysAsTheWritePath(t *testing.T) {
	n := model.NoteIndex{ID: "note_a", UpdatedAt: "2026-08-01T00:00:00Z"}
	pk, sk := repository.NoteIndexKeys("user1", n)
	if pk != "TENANT#user1#NOTES#ACTIVE" {
		t.Errorf("gsi2pk = %q", pk)
	}
	// Re-rendered to the fixed width, because RFC3339Nano trims trailing zeros
	// and a variable-width sort key stops sorting chronologically.
	if sk != "2026-08-01T00:00:00.000000000Z" {
		t.Errorf("gsi2sk = %q, want the fixed-width instant", sk)
	}
}
