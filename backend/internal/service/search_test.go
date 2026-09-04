package service

import (
	"context"
	"encoding/base64"
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
// invalid UTF-8 on the wire.
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

// The offset of the term has to be found in the same string the excerpt is cut
// from. Locating it in strings.ToLower(text) and then slicing text is only
// correct while lowercasing preserves byte length, which it does not: U+023A Ⱥ
// is two bytes and lowercases to three, so the offset runs past the end of the
// original and the slice panics. GET /v1/search has no recover() above it, so
// that is a 5xx for every query this tenant makes — over text they dictated.
//
// unicode.ToLower is a rune-for-rune map, so a rune offset is stable across the
// fold even when the byte offset is not.
func TestSearchExcerptLocatesTheTermByRuneNotByLoweredByteOffset(t *testing.T) {
	const kelvin = "K" // KELVIN SIGN, 3 bytes, lowercases to "k", 1 byte.
	const dotted = "İ" // LATIN CAPITAL LETTER I WITH DOT ABOVE, 2 bytes -> "i".
	const alveolar = "Ⱥ"

	cases := []struct {
		name string
		text string
		term string
		want string
	}{
		{
			name: "plain ascii is unchanged",
			text: "the roof leaks over the bench",
			term: "roof",
			want: "the roof leaks over the bench",
		},
		{
			// 7 bytes of text; the lowered form is 10, so the byte offset of
			// "x" is 9 and text[:9] is out of range.
			name: "lowercase longer in utf8 than the original",
			text: alveolar + alveolar + alveolar + "x",
			term: "x",
			want: alveolar + alveolar + alveolar + "x",
		},
		{
			name: "dotted capital i",
			text: dotted + dotted + "needle",
			term: "needle",
			want: dotted + dotted + "needle",
		},
		{
			name: "lowercase shorter in utf8 than the original",
			text: kelvin + kelvin + kelvin + "needle",
			term: "needle",
			want: kelvin + kelvin + kelvin + "needle",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := excerpt(tc.text, tc.term)
			if got != tc.want {
				t.Fatalf("excerpt(%q, %q) = %q, want %q", tc.text, tc.term, got, tc.want)
			}
		})
	}
}

// Below the panic threshold the same byte/rune confusion still moves the window,
// and it moves it far enough to cut the term out of its own excerpt.
func TestSearchExcerptWindowsAMultiByteHitAroundTheTerm(t *testing.T) {
	const kelvin = "K"
	text := strings.Repeat(kelvin, 100) + "needle" + strings.Repeat("z", 100)

	got := excerpt(text, "needle")

	if !strings.Contains(got, "needle") {
		t.Fatalf("excerpt = %q, want it to contain the term it is context for", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("excerpt = %q contains the replacement rune, so a character was cut in half", got)
	}
	runes := []rune(text)
	want := "…" + strings.TrimSpace(string(runes[100-excerptRadius:100+len([]rune("needle"))+excerptRadius])) + "…"
	if got != want {
		t.Fatalf("excerpt = %q, want %q", got, want)
	}
}

// The panic is reachable from the endpoint, not only from the helper: snippets
// are dictated or typed note text.
func TestSearchSurvivesASnippetWhoseLowercaseIsLonger(t *testing.T) {
	svc := searchFixture(t, model.NoteIndex{
		ID:      "n_alveolar",
		Title:   "Fold",
		Snippet: "ȺȺȺ x",
	})

	page, err := svc.Search(context.Background(), "user1", "x", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := hitIDs(page); len(got) != 1 || got[0] != "n_alveolar" {
		t.Fatalf("results = %v, want the note whose snippet holds the term", got)
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

// Every match has to be reachable. Collecting hits across store pages,
// truncating to the requested limit and then handing back the store's cursor
// from past every note scanned throws away the surplus: the cursor resumes
// after the notes those hits came from, so no later request can ever return
// them. With 120 matching notes and limit=50 the first page returns 50, the
// store is exhausted so the cursor is empty, and 70 notes the user can see in
// their own list are unfindable by search forever.
func TestSearchPagingToExhaustionReturnsEveryMatchExactlyOnce(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	const total = 120
	for i := range total {
		if _, err := store.PutNote(ctx, "user1", model.NoteIndex{
			ID:    fmt.Sprintf("note_%03d", i),
			Title: fmt.Sprintf("Widget %03d", i),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	}
	svc := NewSearchService(NewNotesService(store, memory.NewObjects()))

	seen := map[string]int{}
	order := []string{}
	cursor := ""
	for request := 0; ; request++ {
		if request > total {
			t.Fatalf("paging did not terminate after %d requests", request)
		}
		page, err := svc.Search(ctx, "user1", "widget", repository.ListOptions{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatalf("Search(request %d): %v", request, err)
		}
		if len(page.Items) > 50 {
			t.Fatalf("request %d returned %d hits, more than the requested limit", request, len(page.Items))
		}
		for _, id := range hitIDs(page) {
			seen[id]++
			order = append(order, id)
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}

	missing := []string{}
	duplicated := []string{}
	for i := range total {
		id := fmt.Sprintf("note_%03d", i)
		switch seen[id] {
		case 1:
		case 0:
			missing = append(missing, id)
		default:
			duplicated = append(duplicated, id)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d matching notes were never returned by any page, e.g. %v",
			len(missing), total, missing[:min(len(missing), 5)])
	}
	if len(duplicated) > 0 {
		t.Errorf("%d notes were returned by more than one page, e.g. %v",
			len(duplicated), duplicated[:min(len(duplicated), 5)])
	}
	if len(order) != total {
		t.Errorf("paging returned %d hits in total, want %d", len(order), total)
	}
}

// Paging has to be repeatable: the same corpus and the same cursors produce the
// same pages, or a client that re-requests a page sees a different one.
func TestSearchPagingIsDeterministicAcrossRuns(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	for i := range 120 {
		if _, err := store.PutNote(ctx, "user1", model.NoteIndex{
			ID:    fmt.Sprintf("note_%03d", i),
			Title: fmt.Sprintf("Widget %03d", i),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	}
	svc := NewSearchService(NewNotesService(store, memory.NewObjects()))

	drain := func() []string {
		var out []string
		cursor := ""
		for {
			page, err := svc.Search(ctx, "user1", "widget", repository.ListOptions{Limit: 37, Cursor: cursor})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			out = append(out, hitIDs(page)...)
			cursor = page.Cursor
			if cursor == "" {
				return out
			}
		}
	}

	first, second := drain(), drain()
	if !slices.Equal(first, second) {
		t.Fatalf("two identical traversals returned different orders:\n%v\n%v", first, second)
	}
}

func TestSearchCursorRoundTripsTheWindowItNames(t *testing.T) {
	for _, want := range []searchCursor{
		{},
		{Start: "c1"},
		{Start: "c1", End: "c2", Skip: 50},
		{End: "c2", Skip: 7},
	} {
		encoded, err := encodeSearchCursor(want)
		if err != nil {
			t.Fatalf("encodeSearchCursor(%+v): %v", want, err)
		}
		if err := ValidateCursor(encoded); err != nil {
			t.Errorf("the cursor search issues is rejected by the API's own cursor check: %v", err)
		}
		got, err := decodeSearchCursor(encoded)
		if err != nil {
			t.Fatalf("decodeSearchCursor(%q): %v", encoded, err)
		}
		if got != want {
			t.Errorf("cursor round-tripped to %+v, want %+v", got, want)
		}
	}
}

// A malformed cursor is a client mistake. Typing it as ErrInvalidCursor is what
// lets the handler answer 400 rather than 500.
func TestSearchCursorRejectsAMalformedToken(t *testing.T) {
	cases := map[string]string{
		"not base64":    "!!!not base64!!!",
		"bad json":      base64.RawURLEncoding.EncodeToString([]byte(searchCursorPrefix + "{nope")),
		"negative skip": base64.RawURLEncoding.EncodeToString([]byte(searchCursorPrefix + `{"skip":-1}`)),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSearchCursor(cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("decodeSearchCursor(%q) err = %v, want ErrInvalidCursor", cursor, err)
			}
		})
	}
}

// Search used to hand out the store's own continuation token. One held across a
// deploy — in a client's in-flight request, or in a bookmark — still has to
// resume rather than 400.
func TestSearchAcceptsAStoreCursorIssuedBeforeThisEncodingExisted(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	for i := range 5 {
		if _, err := store.PutNote(ctx, "user1", model.NoteIndex{
			ID:    fmt.Sprintf("note_%03d", i),
			Title: fmt.Sprintf("Widget %03d", i),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	}
	svc := NewSearchService(NewNotesService(store, memory.NewObjects()))

	notes, err := store.ListNotes(ctx, "user1", repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if notes.Cursor == "" {
		t.Fatal("the store issued no cursor, so this test has nothing to hand back")
	}

	page, err := svc.Search(ctx, "user1", "widget", repository.ListOptions{Cursor: notes.Cursor})
	if err != nil {
		t.Fatalf("Search(store cursor): %v", err)
	}
	// The note list is ordered most-recently-touched first. These five carry no
	// update time at all, so every sort key is equal and the tie is broken by
	// id, descending — so the page the cursor skipped is note_004 and note_003,
	// and the three that remain are 000 to 002. Search then ranks what it is
	// given, so the cursor decides WHICH notes are searched and this order is
	// search's own. Same three-note assertion as before; the trio changed
	// because the list order did.
	if got := hitIDs(page); !slices.Equal(got, []string{"note_000", "note_001", "note_002"}) {
		t.Fatalf("results = %v, want the three notes after the store cursor", got)
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

// The owner searched for a word several transcripts contained and got nothing:
// the body match was the 500-rune snippet only. The stored search text is the
// whole body (capped), so a term past the snippet is a hit.
func TestSearchMatchesTheStoredSearchTextBeyondTheSnippet(t *testing.T) {
	body := strings.Repeat("filler ", 200) + "There is a Bug in the recorder."
	svc := searchFixture(t,
		model.NoteIndex{ID: "n_deep", Title: "Recorder", Snippet: Snippet(body), SearchText: SearchText(body)},
		model.NoteIndex{ID: "n_none", Title: "Kitchen", Snippet: "tiles", SearchText: "the tiles are cracked"},
	)

	page, err := svc.Search(context.Background(), "user1", "BUG", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := hitIDs(page)
	if len(got) != 1 || got[0] != "n_deep" {
		t.Fatalf("results = %v, want only n_deep", got)
	}
	hit := page.Items[0]
	if !slices.Equal(hit.MatchedIn, []string{MatchBody}) {
		t.Errorf("matched_in = %v, want [body]", hit.MatchedIn)
	}
	if !strings.Contains(hit.Excerpt, "bug in the recorder") {
		t.Errorf("excerpt = %q, want the search-text context around the term", hit.Excerpt)
	}
}

// Search must ask the store for the search text. The in-memory store, like the
// DynamoDB projection, drops it unless asked — so a search that forgot would
// silently degrade to snippet-only matching.
func TestSearchAsksTheStoreForSearchText(t *testing.T) {
	store := memory.NewStore()
	if _, err := store.PutNote(context.Background(), "user1", model.NoteIndex{
		ID: "n1", Title: "Plain", SearchText: "a needle only the search text holds",
	}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	svc := NewSearchService(NewNotesService(store, memory.NewObjects()))
	page, err := svc.Search(context.Background(), "user1", "needle", repository.ListOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := hitIDs(page); len(got) != 1 {
		t.Fatalf("results = %v, want the search-text hit", got)
	}
}
