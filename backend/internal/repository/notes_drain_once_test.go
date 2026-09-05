package repository_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// seedNotesWithSearchText writes n active notes, each with a search text that
// names it, so a test can tell which rows carried the field.
func seedNotesWithSearchText(t *testing.T, store repository.Store, tenantID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := store.PutNote(context.Background(), tenantID, model.NoteIndex{
			ID: fmt.Sprintf("note_%03d", i), Title: fmt.Sprintf("Note %d", i), UpdatedAt: model.Now(),
			SearchText: fmt.Sprintf("search text of note %d", i),
		}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}
}

// The notes list is ordered in Go over a drain of the partition, so paging
// through it with a cursor drains the partition once per page. A caller that
// wants every row — Ask, routing, the storage summary, export, tags — pays
// ⌈N/200⌉ full reads for one read's worth of rows. DrainNotes is that one read.
func TestDrainNotesReadsThePartitionOnceWhereDrainPagesReadsItPerPage(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	const total = 37
	seedNotesWithSearchText(t, store, "tenant-a", total)
	// Four rows per Query, so one read of the partition is ten Queries and the
	// two approaches are told apart by their counts.
	api.pageSize = 4
	const queriesPerDrain = (total + 3) / 4

	api.queries = nil
	paged, err := repository.DrainPages(ctx, 0, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		opts.Limit = 10
		return store.ListNotes(ctx, "tenant-a", opts)
	})
	if err != nil {
		t.Fatalf("DrainPages: %v", err)
	}
	pagedQueries := len(api.queries)
	if pagedQueries != 4*queriesPerDrain {
		t.Fatalf("DrainPages over four pages issued %d queries, want %d — the premise of this test is that it re-drains per page", pagedQueries, 4*queriesPerDrain)
	}

	api.queries, api.batchGets = nil, nil
	drained, truncated, err := store.DrainNotes(ctx, "tenant-a", repository.DrainOptions{IncludeSearchText: true})
	if err != nil {
		t.Fatalf("DrainNotes: %v", err)
	}
	if truncated {
		t.Fatal("a drain of 37 notes reported the ceiling")
	}
	if got := len(api.queries); got != queriesPerDrain {
		t.Fatalf("DrainNotes issued %d queries, want %d: one read of the partition", got, queriesPerDrain)
	}
	if len(api.batchGets) != 0 {
		t.Fatalf("DrainNotes hydrated with BatchGetItem; the drain itself should carry the field for every row")
	}
	if len(drained) != len(paged) {
		t.Fatalf("DrainNotes returned %d notes, DrainPages %d", len(drained), len(paged))
	}
	for i := range drained {
		if drained[i].ID != paged[i].ID {
			t.Fatalf("order differs at %d: drain %s, pages %s", i, drained[i].ID, paged[i].ID)
		}
		if !strings.HasPrefix(drained[i].SearchText, "search text of note") {
			t.Fatalf("note %s came back without its search text: %q", drained[i].ID, drained[i].SearchText)
		}
	}

	// MaxItems cuts the ordered result and the shelf is honoured.
	cut, _, err := store.DrainNotes(ctx, "tenant-a", repository.DrainOptions{MaxItems: 5})
	if err != nil {
		t.Fatalf("DrainNotes(MaxItems): %v", err)
	}
	if len(cut) != 5 || cut[0].ID != paged[0].ID {
		t.Fatalf("cut drain = %v, want the first five of the list order", ids(cut))
	}
	if cut[0].SearchText != "" {
		t.Fatal("a drain that did not ask carried search text")
	}
	archived, _, err := store.DrainNotes(ctx, "tenant-a", repository.DrainOptions{Shelf: repository.NoteShelfArchived})
	if err != nil {
		t.Fatalf("DrainNotes(archived): %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("the archive drain returned %d active notes", len(archived))
	}
}

// A paged list that asks for search_text drains the partition light and
// fetches the field for the page alone, re-asking for what a batch left
// unprocessed. The offline corpus reads the whole corpus this way, 200 notes a
// page; before, each page carried 32 KB per note for EVERY note.
func TestListNotesWithSearchTextHydratesOnlyThePage(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	const total = 230
	seedNotesWithSearchText(t, store, "tenant-a", total)
	api.unprocessedEvery = 2

	api.queries, api.batchGets = nil, nil
	page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: repository.MaxListLimit, IncludeSearchText: true})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != int(repository.MaxListLimit) || page.Cursor == "" {
		t.Fatalf("page = %d items, cursor %q", len(page.Items), page.Cursor)
	}
	for _, q := range api.queries {
		if strings.Contains(*q.ProjectionExpression, "search_text") {
			t.Fatalf("the drain projected search_text for the whole partition: %q", *q.ProjectionExpression)
		}
	}
	requested := 0
	for _, b := range api.batchGets {
		keys := len(b.RequestItems[tableName].Keys)
		if keys > 100 {
			t.Fatalf("a BatchGetItem carried %d keys; DynamoDB accepts 100", keys)
		}
		requested += keys
	}
	// 200 keys in two batches of 100, plus the re-asks for the keys the fake
	// left unprocessed.
	if requested < int(repository.MaxListLimit) || len(api.batchGets) < 3 {
		t.Fatalf("hydration asked for %d keys in %d calls; want the page's 200 with the unprocessed ones asked again", requested, len(api.batchGets))
	}
	for _, n := range page.Items {
		if !strings.HasPrefix(n.SearchText, "search text of note") {
			t.Fatalf("note %s on the page has no search text after hydration: %q", n.ID, n.SearchText)
		}
	}

	// The last page hydrates only what is on it.
	api.batchGets = nil
	last, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: repository.MaxListLimit, IncludeSearchText: true, Cursor: page.Cursor})
	if err != nil {
		t.Fatalf("ListNotes(page 2): %v", err)
	}
	if len(last.Items) != total-int(repository.MaxListLimit) {
		t.Fatalf("last page = %d items, want %d", len(last.Items), total-int(repository.MaxListLimit))
	}
	requested = 0
	for _, b := range api.batchGets {
		requested += len(b.RequestItems[tableName].Keys)
	}
	if requested > len(last.Items)+1 {
		t.Fatalf("the last page hydrated %d keys for %d notes", requested, len(last.Items))
	}
	for _, n := range last.Items {
		if n.SearchText == "" {
			t.Fatalf("note %s on the last page has no search text", n.ID)
		}
	}
}
