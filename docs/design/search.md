# Search

Finding a note by a word in it: an instant ranking of the corpus the device
already holds, refined by `GET /v1/search`. Code: the server in
`backend/internal/service/search.go` (`SearchService`, `matchNote`,
`excerpt`) and `search_text.go` (`SearchText`), the handler in
`backend/internal/handler/search.go`; the client in
`frontend/src/features/search/localSearch.ts` (`rankLocal`,
`mergeResults`, `excerptAround`) wired up in
`frontend/src/screens/NotesScreen.tsx` with `useSearch` and
`useSearchCorpus` (`frontend/src/api/queries/notes.ts`).

## What is indexed

Search reads the note index row only — title, aliases, tags, the snippet and
`NoteIndex.SearchText` — and never fetches a body or a transcript from S3 per
query: one GET per candidate note per keystroke is a denial of service
against your own bucket. Transcripts are covered anyway, because every
capture's cleaned text is appended into the note body.

`SearchText(body)` is the body prepared for matching: append markers
stripped (`StripCaptureMarkers`), chillu spellings folded
(`llm.FoldScript`, which rewrites the Malayalam sequences carrying a
ZWJ/ZWNJ to the atomic chillu, so the spelling the speech provider writes
meets the one a person types), lowercased, whitespace collapsed, and cut on
a rune boundary to `model.MaxSearchTextBytes` (32 KiB) after the fold,
because `strings.ToLower` can grow a string. Every writer of a body writes
it: `UpdateNote` (`service/notes.go`), the pipeline's index refresh after an
append (`service/capture_edit.go`), and `chintanctl backfill-search-text` for notes that
predate the field. It is a promoted attribute the item blob does not carry
(`data-model.md`) and reaches a list only on request
(`ListOptions.IncludeSearchText`; `GET /v1/notes?include=search_text`),
hydrated for the page alone with one `BatchGetItem` (`hydrateNotes`,
`repository/dynamo_notes.go`) so a 200-row page never drags 32 KiB per note
through the drain. Tests: `TestSearchTextLowercasesStripsMarkersAndCollapsesWhitespace`,
`TestSearchTextIsCappedOnARuneBoundary`, `TestSearchTextCapAppliesAfterLowercasing`,
`TestSearchTextAndQueryFoldChilluSpellings`.

## `GET /v1/search`

`q` is required, at most `MaxSearchQueryRunes` (500) runes — longer is a
paste, not a search — and is folded, lowercased and split on whitespace into
terms (`searchTerms`). Every term must appear somewhere in the note
(`matchNote`): an AND is what two typed words mean; an OR returns the
corpus. A term scores 8 in the title, 4 in an alias, 4 in a tag and 1 in the
snippet or search text; `matched_in` reports the fields hit, in the fixed
order `title, alias, tag, body`. The excerpt is `excerptRadius` (60) runes
either side of the first term's first hit, taken from the snippet (which
keeps its case) and otherwise from the search text; cuts are marked with an
ellipsis, located by rune offset because lowercasing does not preserve byte
length (`TestSearchExcerptLocatesTheTermByRuneNotByLoweredByteOffset`).
The score is not serialised: a number the client cannot reproduce is noise
in the contract.

A request scans up to `searchScanPages` (5) store pages of
`repository.MaxListLimit` (200) rows each, stops once it holds `limit` hits
(default `repository.DefaultListLimit`, 50), ranks the whole window by score,
then title, then id, and returns one page. The cursor (`srch1:` + JSON,
base64url) names the window (`Start`, `End`) and an offset (`Skip`) into its
ranked order, because the store's own continuation token points past every
note scanned and would skip the hits that did not fit; a cursor without the
prefix is taken as a store token. Paging to exhaustion returns every match
exactly once (`TestSearchPagingToExhaustionReturnsEveryMatchExactlyOnce`,
`TestSearchPagingIsDeterministicAcrossRuns`,
`TestSearchCursorRoundTripsTheWindowItNames`). The query string reaches no
log line or metric: a person searches for what they are worried about.
Results are the caller's own partition (`TestSearchIsScopedToTheCaller`).

## Local search

The library ranks on every keystroke the rows it already has
(`rankLocal`), so the first result appears before the network answers and
search works with no connection. The corpus is the server's list rows
overlaid with what the device holds for each note: the `search_text` from
`useSearchCorpus`, which pages `GET /v1/notes?include=search_text` 200 at a
time into IndexedDB once per session and again after five minutes or when a
recording files, or a cached full note's body (`offline.md`). Folding is
`foldMalayalam(text.toLowerCase())` from `features/notes/find.ts`, the same
chillu fold as the server's.

The weights are crude on purpose: a title prefix 100, a title substring 60,
an alias 40, a tag 30, the snippet or body 15; ties break on `updated_at`,
newest first. The excerpt (`excerptAround`, 48 code points of context,
sliced by code point so a surrogate pair is never split) prefers the body
context over repeating the title. `localSearch.test.ts` pins the order
("ranks a title prefix above a body match", "ranks a body match below a tag
match", "breaks ties by recency").

## Merging the two

The server is asked once typing pauses for `SERVER_SEARCH_DEBOUNCE_MS`
(250 ms), only online and only in the active view, which is all it indexes;
the answer is kept for 30 s (`useSearch`). `mergeResults` keeps the local
list first and in its order — reordering under a finger is worse than a
stable list — and appends what only the server found, which in practice is
a word deep in a transcript the device has not cached; a note in both keeps
its local excerpt and the union of `matched_in`. Offline, or on a server
error, a status line under the field says the search covered this device's
notes only (`LibraryList.tsx`, `search-offline`), and the local list stands.

## Elsewhere

Find on a note (`FindBar.tsx`, `find.ts`) is the same fold applied inside one
note's text, with a count and a highlight; `note-screen.md` owns it. Ask
does not call search: `ask.Rank` ranks the same index rows with its own
weights; `ask.md` owns it.

History: `docs/backlog.md`.
