package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeReindexer stands in for the store. The derivation of the index keys and
// the conditional write are the store's, and are covered where they live
// (internal/repository); what this command decides is which tenants to run for
// and whether to write at all.
type fakeReindexer struct {
	calls   []string
	perCall int
	err     error
}

func (f *fakeReindexer) ReindexNotes(_ context.Context, tenantID string) (int, error) {
	f.calls = append(f.calls, tenantID)
	if f.err != nil {
		return 0, f.err
	}
	return f.perCall, nil
}

func withReindexer(e *env, f *fakeReindexer) *env {
	e.Notes = f
	return e
}

func seedBlob(t *testing.T, blobs *fakeBlobs, key string) {
	t.Helper()
	if err := blobs.Put(context.Background(), key, strings.NewReader("x"), 1, "text/plain"); err != nil {
		t.Fatalf("seed blob: %v", err)
	}
}

func putNoteItem(part *fakePartition, tenantID, id string) {
	s := func(v string) AttrValue { return AttrValue{S: &v} }
	if part.items["USER#"+tenantID] == nil {
		part.items["USER#"+tenantID] = map[string]Item{}
	}
	part.items["USER#"+tenantID]["NOTE#"+id] = Item{
		"pk": s("USER#" + tenantID), "sk": s("NOTE#" + id),
		"type": s("note"), "note_id": s(id), "title": s("Note " + id),
	}
}

// TestReindexRunsForEveryTenantAndReportsWhatItWrote is the migration this
// command exists for: adding a GSI does not index the rows already in the
// table, so the notes list reads empty until each note is rewritten.
func TestReindexRunsForEveryTenantAndReportsWhatItWrote(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	f := &fakeReindexer{perCall: 2}
	withReindexer(e, f)

	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	seedBlob(t, blobs, "tenants/user2/notes/note_c/note.md")
	putNoteItem(part, "user1", "note_a")
	putNoteItem(part, "user1", "note_b")
	putNoteItem(part, "user2", "note_c")

	res, err := runReindex(context.Background(), e, nil, true)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("reindexed %d tenants (%v), want both", len(f.calls), f.calls)
	}
	if res.Notes != 3 {
		t.Errorf("examined %d notes, want 3", res.Notes)
	}
	if res.Written != 4 {
		t.Errorf("reported %d writes, want 4 (2 per tenant)", res.Written)
	}
}

// TestReindexWithoutApplyWritesNothing holds it to the same dry-run default as
// every other chintanctl command that can change data.
func TestReindexWithoutApplyWritesNothing(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	f := &fakeReindexer{perCall: 1}
	withReindexer(e, f)

	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	putNoteItem(part, "user1", "note_a")

	res, err := runReindex(context.Background(), e, nil, false)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("a dry run wrote to %v", f.calls)
	}
	if part.puts != 0 {
		t.Errorf("a dry run performed %d writes", part.puts)
	}
	if res.Written != 1 {
		t.Errorf("a dry run reported %d candidates, want the 1 note it found", res.Written)
	}
}

// TestReindexStopsOnAFailure keeps a partial migration from being reported as a
// finished one. Half an index is a notes list missing half its notes, and the
// operator has to know to run it again.
func TestReindexStopsOnAFailure(t *testing.T) {
	e, _, blobs := newTestEnv(nil)
	boom := errors.New("dynamodb: ProvisionedThroughputExceeded")
	withReindexer(e, &fakeReindexer{err: boom})
	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")

	if _, err := runReindex(context.Background(), e, nil, true); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying failure to surface", err)
	}
}

// TestReindexHonoursAnExplicitTenant keeps a targeted repair from touching
// every tenant on the instance.
func TestReindexHonoursAnExplicitTenant(t *testing.T) {
	e, part, blobs := newTestEnv(nil)
	f := &fakeReindexer{perCall: 1}
	withReindexer(e, f)
	seedBlob(t, blobs, "tenants/user1/notes/note_a/note.md")
	seedBlob(t, blobs, "tenants/user2/notes/note_c/note.md")
	putNoteItem(part, "user1", "note_a")

	if _, err := runReindex(context.Background(), e, []string{"user1"}, true); err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != "user1" {
		t.Fatalf("reindexed %v, want only user1", f.calls)
	}
}
