package main

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
)

func outcomesOf(res *backfillResult) map[string]string {
	out := map[string]string{}
	for _, n := range res.Notes {
		out[n.NoteID] = n.Outcome
	}
	return out
}

func TestBackfillIsADryRunByDefault(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	res, err := runBackfillSearchText(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := outcomesOf(res)["n1"]; got != backfillFilled {
		t.Fatalf("n1 outcome = %q, want %q: the seeded row has no search_text", got, backfillFilled)
	}
	if part.updates != 0 || part.puts != 0 {
		t.Errorf("a dry run wrote: %d updates, %d puts", part.updates, part.puts)
	}
}

func TestBackfillWritesTheAttributeTheStoreReads(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	res, err := runBackfillSearchText(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("backfill --apply: %v", err)
	}
	if got := outcomesOf(res)["n1"]; got != backfillFilled {
		t.Fatalf("n1 outcome = %q, want %q", got, backfillFilled)
	}
	if part.updates != 1 {
		t.Fatalf("updates = %d, want exactly one conditional SET", part.updates)
	}

	var row Item
	if err := part.Scan(ctx, tenantPK("tenantA"), "NOTE#n1", func(it Item) error { row = it; return nil }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := service.SearchText("# Kitchen\n\nCounter depth is 24 inches.\n")
	if got := row.Str("search_text"); got != want {
		t.Errorf("search_text = %q, want %q", got, want)
	}
	// The write must be a SET of one attribute: the record blob, the version
	// and every other promoted attribute are untouched, so the API's
	// optimistic concurrency does not see a phantom edit.
	if row.Num("version") != 3 {
		t.Errorf("version = %d after the backfill, want 3 (unchanged)", row.Num("version"))
	}
	if !strings.Contains(row.Str("data"), `"title":"Kitchen: Rebuild Plan"`) {
		t.Errorf("the record blob was disturbed: %s", row.Str("data"))
	}
	if strings.Contains(row.Str("data"), "counter depth is 24") {
		t.Errorf("the search text leaked into the record blob: %s", row.Str("data"))
	}

	// Idempotent: a second run finds the row current and writes nothing.
	again, err := runBackfillSearchText(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if got := outcomesOf(again)["n1"]; got != backfillCurrent {
		t.Errorf("second run outcome = %q, want %q", got, backfillCurrent)
	}
	if part.updates != 1 {
		t.Errorf("second run wrote again: updates = %d", part.updates)
	}
}

func TestBackfillReportsANoteWithoutABodyAndTouchesNothing(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	put(t, part, noteItem("tenantA", model.NoteIndex{
		ID: "n2", Title: "No body", UpdatedAt: "2026-08-07T11:00:00.000000000Z",
		S3MarkdownKey: "tenants/tenantA/notes/n2/note.md",
	}))

	res, err := runBackfillSearchText(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := outcomesOf(res)["n2"]; got != backfillNoBody {
		t.Errorf("n2 outcome = %q, want %q", got, backfillNoBody)
	}
	if part.updates != 1 {
		t.Errorf("updates = %d, want 1 (n1 only)", part.updates)
	}
}

func TestBackfillSkipsARowThatMovedUnderIt(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	// A partition whose rows are always one version ahead of what was read is
	// what a concurrent editor looks like from here.
	e.Part = &racingPartition{fakePartition: part}

	res, err := runBackfillSearchText(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := outcomesOf(res)["n1"]; got != backfillChanged {
		t.Errorf("n1 outcome = %q, want %q", got, backfillChanged)
	}
	if part.updates != 0 {
		t.Errorf("a moved row was written: updates = %d", part.updates)
	}
}

// racingPartition bumps every row's version between the scan and the update.
type racingPartition struct {
	*fakePartition
}

func (r *racingPartition) Update(ctx context.Context, pk, sk string, set Item, expectVersion int64) error {
	return r.fakePartition.Update(ctx, pk, sk, set, expectVersion+1)
}
