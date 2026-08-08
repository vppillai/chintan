package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// ErrEmptySearchQuery rejects a search with nothing to search for. Returning
// every note for an empty query is an unbounded list wearing a filter's clothes.
var ErrEmptySearchQuery = errors.New("search query is empty")

// MaxSearchQueryRunes bounds a query. Longer than this is not a search, it is a
// paste.
const MaxSearchQueryRunes = 500

// searchScanPages bounds how many store pages one request will read looking for
// matches.
//
// A filter runs after the query, exactly as a DynamoDB FilterExpression does, so
// a page can match nothing. Scanning a few pages keeps the common case to one
// round trip without letting one request walk an entire corpus.
const searchScanPages = 5

// excerptRadius is how many runes of context surround a hit.
const excerptRadius = 60

// Match fields, in the order they are reported.
const (
	MatchTitle = "title"
	MatchAlias = "alias"
	MatchTag   = "tag"
	MatchBody  = "body"
)

// SearchHit is one result with the context that makes it recognisable.
type SearchHit struct {
	NoteID    string   `json:"note_id"`
	Title     string   `json:"title"`
	Excerpt   string   `json:"excerpt,omitempty"`
	MatchedIn []string `json:"matched_in,omitempty"`

	// score orders results within a page. It is not serialised: a relevance
	// number the client cannot reproduce is noise in the contract.
	score int
}

// SearchService answers GET /v1/search.
//
// It queries the caller's own partition and filters, which is correct at
// personal scale and costs nothing beyond the table. There is no managed search
// service and no second copy of the corpus to keep consistent. If this ever
// stops scaling the replacement is a per-tenant inverted index in the same
// table, not an external cluster.
//
// It searches the note index — titles, aliases, tags and the stored snippet.
// It deliberately does not fetch note bodies or transcripts from object
// storage: that would be one S3 GET per candidate note per keystroke-driven
// request, which is the shape of an accidental denial of service against your
// own bucket. The client's IndexedDB corpus covers full-text offline; this
// refines and extends it.
type SearchService struct {
	notes *NotesService
}

// NewSearchService builds the search service over the note index.
func NewSearchService(notes *NotesService) *SearchService {
	return &SearchService{notes: notes}
}

// searchCursorPrefix tags a cursor this service issued, so a store
// continuation token — which is base64 of a JSON object in the DynamoDB store —
// is never mistaken for one.
const searchCursorPrefix = "srch1:"

// searchCursor resumes a search inside a *window*: the run of store pages one
// request scanned.
//
// Ranking happens over the whole window, so a window can hold more hits than
// one page returns. The surplus has to stay reachable, which means the cursor
// has to name the window as well as the offset into it — the store's own
// continuation token points past every note scanned, so on its own it skips
// exactly the hits that did not fit.
//
// Start and End bound the window in the store's own terms rather than in hits,
// so a resumed request rebuilds the identical window and the identical order
// even if the client changes its limit mid-traversal.
type searchCursor struct {
	// Start is the store cursor the window began at. Empty is the first page
	// of the partition.
	Start string `json:"start,omitempty"`
	// End is the store cursor the window ended at. Empty means the window ran
	// to the end of the partition. It is only meaningful when Skip is set.
	End string `json:"end,omitempty"`
	// Skip is how many of the window's ranked hits earlier pages returned. Zero
	// means "begin a fresh window at Start".
	Skip int `json:"skip,omitempty"`
}

func encodeSearchCursor(c searchCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("search: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(append([]byte(searchCursorPrefix), raw...)), nil
}

// decodeSearchCursor reads a cursor this service issued. Anything else is
// treated as a store continuation token — that is what search used to hand out,
// so a cursor held across a deploy still resumes — and the store rejects it if
// it belongs to another partition.
func decodeSearchCursor(cursor string) (searchCursor, error) {
	if cursor == "" {
		return searchCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return searchCursor{}, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	body, ok := strings.CutPrefix(string(raw), searchCursorPrefix)
	if !ok {
		return searchCursor{Start: cursor}, nil
	}
	var c searchCursor
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return searchCursor{}, fmt.Errorf("%w: malformed search cursor", ErrInvalidCursor)
	}
	if c.Skip < 0 {
		return searchCursor{}, fmt.Errorf("%w: malformed search cursor", ErrInvalidCursor)
	}
	return c, nil
}

// Search returns one page of matches for q.
//
// It scans store pages until it has enough hits to fill the page, ranks what it
// found, and returns a cursor that resumes either inside that window — when
// ranking produced more hits than fitted — or at the store position the window
// stopped at. Nothing matched is ever dropped: paging to exhaustion returns
// every matching note exactly once. A page may still legitimately be short, or
// empty, with a cursor set, because a scanned page can match nothing.
func (s *SearchService) Search(ctx context.Context, userID, q string, opts repository.ListOptions) (repository.Page[SearchHit], error) {
	terms := searchTerms(q)
	if len(terms) == 0 {
		return repository.Page[SearchHit]{}, ErrEmptySearchQuery
	}

	want := int(opts.Limit)
	if want <= 0 {
		want = int(repository.DefaultListLimit)
	}

	from, err := decodeSearchCursor(opts.Cursor)
	if err != nil {
		return repository.Page[SearchHit]{}, err
	}

	hits := make([]SearchHit, 0, want)
	cursor := from.Start
	for page := 0; page < searchScanPages; page++ {
		got, err := s.notes.ListNotes(ctx, userID, repository.ListOptions{
			Limit:  repository.MaxListLimit,
			Cursor: cursor,
		})
		if err != nil {
			return repository.Page[SearchHit]{}, err
		}
		for _, note := range got.Items {
			if hit, ok := matchNote(note, terms); ok {
				hits = append(hits, hit)
			}
		}
		cursor = got.Cursor
		if cursor == "" {
			break
		}
		if from.Skip > 0 {
			// Resuming: rebuild the window the earlier request scanned, so the
			// ranked order this offset indexes into is the same one.
			if cursor == from.End {
				break
			}
			continue
		}
		if len(hits) >= want {
			break
		}
	}

	// Stable order: score first, then title, then id, so two runs of the same
	// query over an unchanged corpus return the same page — and so an offset
	// into the window means the same thing on the request that issued it and on
	// the request that redeems it.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].Title != hits[j].Title {
			return hits[i].Title < hits[j].Title
		}
		return hits[i].NoteID < hits[j].NoteID
	})

	start := min(from.Skip, len(hits))
	end := min(start+want, len(hits))
	page := repository.Page[SearchHit]{Items: hits[start:end]}

	switch {
	case end < len(hits):
		// Ranking found more than this page carries. The rest of the window is
		// only reachable through a cursor that names the window.
		page.Cursor, err = encodeSearchCursor(searchCursor{Start: from.Start, End: cursor, Skip: end})
	case cursor != "":
		page.Cursor, err = encodeSearchCursor(searchCursor{Start: cursor})
	}
	if err != nil {
		return repository.Page[SearchHit]{}, err
	}

	return page, nil
}

// searchTerms splits a query into lowercase terms, bounded in length.
func searchTerms(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if runes := []rune(q); len(runes) > MaxSearchQueryRunes {
		q = string(runes[:MaxSearchQueryRunes])
	}
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// matchNote requires every term to appear somewhere in the note. An AND across
// terms is what a person typing two words means; an OR returns the corpus.
func matchNote(note model.NoteIndex, terms []string) (SearchHit, bool) {
	title := strings.ToLower(note.Title)
	snippet := strings.ToLower(note.Snippet)

	fields := map[string]bool{}
	score := 0

	for _, term := range terms {
		found := false
		if strings.Contains(title, term) {
			fields[MatchTitle] = true
			score += 8
			found = true
		}
		for _, alias := range note.Aliases {
			if strings.Contains(strings.ToLower(alias), term) {
				fields[MatchAlias] = true
				score += 4
				found = true
				break
			}
		}
		for _, tag := range note.Tags {
			if strings.Contains(strings.ToLower(tag), term) {
				fields[MatchTag] = true
				score += 4
				found = true
				break
			}
		}
		if strings.Contains(snippet, term) {
			fields[MatchBody] = true
			score++
			found = true
		}
		if !found {
			return SearchHit{}, false
		}
	}

	matched := make([]string, 0, len(fields))
	for _, name := range []string{MatchTitle, MatchAlias, MatchTag, MatchBody} {
		if fields[name] {
			matched = append(matched, name)
		}
	}

	hit := SearchHit{
		NoteID:    note.ID,
		Title:     note.Title,
		MatchedIn: matched,
		score:     score,
	}
	if fields[MatchBody] {
		hit.Excerpt = excerpt(note.Snippet, terms[0])
	}
	return hit, true
}

// excerpt returns the text around the first occurrence of term, cut on rune
// boundaries. A byte slice here would cut a multi-byte rune in half and put
// invalid UTF-8 on the wire.
//
// The term is located by rune offset, not by byte offset. Lowercasing does not
// preserve byte length — U+023A `Ⱥ` is two bytes and folds to a three-byte
// U+2C65 `ⱥ`, KELVIN SIGN folds three bytes down to one — so a byte offset
// found in the lowered text does not address the same character in the
// original. Used against the original it cuts the window loose by whole runes,
// and once the fold has grown the text past its original length it runs off the
// end and panics, which is a 5xx on GET /v1/search for text the user dictated.
//
// strings.ToLower maps rune for rune, so rune offsets do survive the fold even
// where byte offsets do not.
func excerpt(text, term string) string {
	lowered := strings.ToLower(text)
	loweredTerm := strings.ToLower(term)
	idx := strings.Index(lowered, loweredTerm)
	if idx < 0 {
		return ""
	}
	runes := []rune(text)
	// Both offsets below are taken in the lowered text, which is where the
	// match was found; the result is a rune offset, and that one addresses the
	// original too.
	start := len([]rune(lowered[:idx]))
	from := start - excerptRadius
	if from < 0 {
		from = 0
	}
	to := start + len([]rune(loweredTerm)) + excerptRadius
	if to > len(runes) {
		to = len(runes)
	}

	out := strings.TrimSpace(string(runes[from:to]))
	if from > 0 {
		out = "…" + out
	}
	if to < len(runes) {
		out += "…"
	}
	return out
}
