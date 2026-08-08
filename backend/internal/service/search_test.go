package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// searchFixture indexes notes directly. Search reads the note index and
// deliberately never fetches a body from object storage, so the index is the
// whole of its input.
func searchFixture(t *testing.T, notes ...model.NoteIndex) *SearchService {
	t.Helper()
	store := memory.NewStore()
	for _, n := range notes {
		if _, err := store.PutNote(context.Background(), "user1", n); err != nil {
			t.Fatalf("PutNote(%s): %v", n.ID, err)
		}
	}
	return NewSearchService(NewNotesService(store, memory.NewObjects()))
}

func hitIDs(page repository.Page[SearchHit]) []string {
	out := make([]string, 0, len(page.Items))
	for _, hit := range page.Items {
		out = append(out, hit.NoteID)
	}
	return out
}

func TestSearchMatchesTitleAliasTagAndSnippet(t *testing.T) {
	svc := searchFixture(t,
		model.NoteIndex{ID: "n_title", Title: "Roof repair"},
		model.NoteIndex{ID: "n_alias", Title: "House", Aliases: []string{"the roof"}},
		model.NoteIndex{ID: "n_tag", Title: "Shed", Tags: []string{"roofing"}},
		model.NoteIndex{ID: "n_body", Title: "Garage", Snippet: "the roof leaks over the bench"},
		model.NoteIndex{ID: "n_miss", Title: "Kitchen", Snippet: "the tiles are cracked"},
	)

	page, err := svc.Search(context.Background(), "user1", "roof", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := hitIDs(page)
	for _, want := range []string{"n_title", "n_alias", "n_tag", "n_body"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is missing from the results %v", want, got)
		}
	}
	if slices.Contains(got, "n_miss") {
		t.Errorf("n_miss matches nothing but was returned: %v", got)
	}

	byID := map[string][]string{}
	excerpts := map[string]string{}
	for _, hit := range page.Items {
		byID[hit.NoteID] = hit.MatchedIn
		excerpts[hit.NoteID] = hit.Excerpt
	}
	for id, want := range map[string]string{
		"n_title": MatchTitle,
		"n_alias": MatchAlias,
		"n_tag":   MatchTag,
		"n_body":  MatchBody,
	} {
		if len(byID[id]) != 1 || byID[id][0] != want {
			t.Errorf("%s matched_in = %v, want exactly [%s]", id, byID[id], want)
		}
	}
	// The excerpt is the body's match context. A title-only hit has no body
	// context to show, and inventing one would put the title on screen twice.
	if excerpts["n_body"] == "" {
		t.Error("the snippet match returned no excerpt, so the result cannot be recognised")
	}
	if excerpts["n_title"] != "" {
		t.Errorf("the title-only match carries excerpt %q, which came from no matched body text", excerpts["n_title"])
	}
}

// Two words mean both words. An OR across terms returns most of the corpus for
// any query with a common word in it, which is a list wearing a filter's
// clothes.
func TestSearchRequiresEveryTermToMatchSomewhere(t *testing.T) {
	svc := searchFixture(t,
		model.NoteIndex{ID: "n_one", Title: "Roof"},
		model.NoteIndex{ID: "n_both", Title: "Roof", Tags: []string{"shingle"}},
	)

	page, err := svc.Search(context.Background(), "user1", "roof shingle", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got := hitIDs(page); len(got) != 1 || got[0] != "n_both" {
		t.Fatalf("results = %v, want only n_both: n_one matches \"roof\" but not \"shingle\"", got)
	}
}

// The order is fixed by the constants, not by map iteration. A matched_in that
// reorders itself between two identical requests is a contract the client
// cannot render stably.
func TestSearchReportsMatchedFieldsInTheDeclaredOrder(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{
		ID:      "n_all",
		Title:   "Roof",
		Aliases: []string{"roofline"},
		Tags:    []string{"roofing"},
		Snippet: "the roof again",
	})

	want := []string{MatchTitle, MatchAlias, MatchTag, MatchBody}
	for range 8 {
		page, err := svc.Search(context.Background(), "user1", "roof", repository.ListOptions{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("results = %v, want one hit", hitIDs(page))
		}
		if got := page.Items[0].MatchedIn; !slices.Equal(got, want) {
			t.Fatalf("matched_in = %v, want %v", got, want)
		}
	}
}

func TestSearchExcerptMarksEveryCutWithAnEllipsis(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{
		ID:      "n_long",
		Title:   "Long",
		Snippet: strings.Repeat("a ", 80) + "needle" + strings.Repeat(" b", 80),
	})

	page, err := svc.Search(context.Background(), "user1", "needle", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	excerpt := page.Items[0].Excerpt

	if !strings.Contains(excerpt, "needle") {
		t.Fatalf("excerpt = %q, want it to contain the term it is context for", excerpt)
	}
	if !strings.HasPrefix(excerpt, "…") {
		t.Errorf("excerpt = %q, want a leading … : text was cut from the front", excerpt)
	}
	if !strings.HasSuffix(excerpt, "…") {
		t.Errorf("excerpt = %q, want a trailing … : text was cut from the end", excerpt)
	}
}

func TestSearchExcerptOmitsTheEllipsisWhenNothingWasCut(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{ID: "n_short", Title: "Short", Snippet: "a needle here"})

	page, err := svc.Search(context.Background(), "user1", "needle", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := page.Items[0].Excerpt; got != "a needle here" {
		t.Fatalf("excerpt = %q, want the whole snippet with no ellipsis: nothing was cut", got)
	}
}

// The cut is by rune. A byte slice here halves a multi-byte rune and puts
// invalid UTF-8 on the wire, which is what v1's snippet code did.
func TestSearchExcerptCutsOnRuneBoundaries(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{
		ID:      "n_utf8",
		Title:   "Multibyte",
		Snippet: strings.Repeat("日", 100) + "needle" + strings.Repeat("語", 100),
	})

	page, err := svc.Search(context.Background(), "user1", "needle", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	excerpt := page.Items[0].Excerpt

	if !utf8.ValidString(excerpt) {
		t.Fatalf("excerpt is not valid UTF-8: %q", excerpt)
	}
	if !strings.Contains(excerpt, "needle") {
		t.Fatalf("excerpt = %q, want it to contain the term", excerpt)
	}
	// Radius is counted in runes, so a 3-byte script must not shrink the window
	// to a third of it. The two ellipses and the term are the only extras.
	wantRunes := 2*excerptRadius + len([]rune("needle"))
	if got := len([]rune(excerpt)); got < wantRunes {
		t.Fatalf("excerpt is %d runes, want at least %d: the radius is being counted in bytes, so a multi-byte script gets a third of the context", got, wantRunes)
	}
}

// An empty query returning every note is an unbounded list with a filter's
// name on it.
func TestSearchRejectsAQueryWithNothingToSearchFor(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{ID: "n1", Title: "Roof"})

	for _, q := range []string{"", "   ", "\t\n "} {
		page, err := svc.Search(context.Background(), "user1", q, repository.ListOptions{})
		if !errors.Is(err, ErrEmptySearchQuery) {
			t.Errorf("Search(%q) err = %v, want ErrEmptySearchQuery", q, err)
		}
		if len(page.Items) != 0 {
			t.Errorf("Search(%q) returned %d notes alongside the error", q, len(page.Items))
		}
	}
}

// A title hit is a stronger answer than a body hit, and the order must be the
// same on every run over an unchanged corpus or the client's list jumps.
func TestSearchRanksATitleMatchAboveABodyMatch(t *testing.T) {
	svc := searchFixture(t,
		// The ids are ordered so that the store returns the weaker hit first;
		// only scoring can put the stronger one on top.
		model.NoteIndex{ID: "n_a_body", Title: "Zebra", Snippet: "the roof leaks"},
		model.NoteIndex{ID: "n_b_title", Title: "Roof"},
	)

	page, err := svc.Search(context.Background(), "user1", "roof", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := hitIDs(page); len(got) != 2 || got[0] != "n_b_title" {
		t.Fatalf("results = %v, want the title match first", got)
	}
}

func TestSearchHonoursTheRequestedLimitAndPagesWithTheStoresCursor(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	// More notes than one store page holds, so the cursor is the store's own
	// continuation token rather than something search invented.
	const total = int(repository.MaxListLimit) + 5
	for i := range total {
		if _, err := store.PutNote(ctx, "user1", model.NoteIndex{
			ID:    fmt.Sprintf("note_%03d", i),
			Title: fmt.Sprintf("Widget %03d", i),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	}
	svc := NewSearchService(NewNotesService(store, memory.NewObjects()))

	first, err := svc.Search(ctx, "user1", "widget", repository.ListOptions{Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(first.Items) != 3 {
		t.Fatalf("first page held %d hits, want the requested 3", len(first.Items))
	}
	if first.Cursor == "" {
		t.Fatalf("first page returned no cursor with %d matching notes behind it", total)
	}

	second, err := svc.Search(ctx, "user1", "widget", repository.ListOptions{Limit: 3, Cursor: first.Cursor})
	if err != nil {
		t.Fatalf("Search(cursor): %v", err)
	}
	if len(second.Items) == 0 {
		t.Fatal("the cursor from the first page returned nothing")
	}
	for _, id := range hitIDs(second) {
		if slices.Contains(hitIDs(first), id) {
			t.Fatalf("%s appears on both pages; the cursor was not consumed (first=%v second=%v)",
				id, hitIDs(first), hitIDs(second))
		}
	}
}

// A query longer than this is not a search, it is a paste. The bound is in
// runes so a multi-byte paste is cut the same way.
func TestSearchTermsBoundsAPastedQuery(t *testing.T) {
	terms := searchTerms(strings.Repeat("a", MaxSearchQueryRunes+100) + " needle")

	if len(terms) != 1 {
		t.Fatalf("terms = %d, want the trailing word to fall outside the bound", len(terms))
	}
	if got := len([]rune(terms[0])); got != MaxSearchQueryRunes {
		t.Fatalf("first term is %d runes, want it cut at %d", got, MaxSearchQueryRunes)
	}
}

func TestSearchTermsLowercasesAndSplitsOnWhitespace(t *testing.T) {
	got := searchTerms("  Roof\tSHINGLE\n repair  ")
	want := []string{"roof", "shingle", "repair"}

	if !slices.Equal(got, want) {
		t.Fatalf("searchTerms = %v, want %v", got, want)
	}
}

func TestSearchSurfacesAStoreFailure(t *testing.T) {
	boom := errors.New("dynamodb: dial tcp: connection refused")
	svc := NewSearchService(NewNotesService(listErrStore{Store: memory.NewStore(), err: boom}, memory.NewObjects()))

	if _, err := svc.Search(context.Background(), "user1", "roof", repository.ListOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's failure rather than an empty result set", err)
	}
}

// listErrStore fails the note list, which is the only read search makes.
type listErrStore struct {
	repository.Store
	err error
}

func (s listErrStore) ListNotes(context.Context, string, repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	return repository.Page[model.NoteIndex]{}, s.err
}
