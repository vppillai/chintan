package service

import (
	"context"
	"errors"
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

// Search returns one page of matches for q.
//
// The returned cursor is the store's own continuation token, so paging is
// stable across requests and a page may legitimately be short — or empty — with
// a cursor still set.
func (s *SearchService) Search(ctx context.Context, userID, q string, opts repository.ListOptions) (repository.Page[SearchHit], error) {
	terms := searchTerms(q)
	if len(terms) == 0 {
		return repository.Page[SearchHit]{}, ErrEmptySearchQuery
	}

	want := int(opts.Limit)
	if want <= 0 {
		want = int(repository.DefaultListLimit)
	}

	hits := make([]SearchHit, 0, want)
	cursor := opts.Cursor
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
		if cursor == "" || len(hits) >= want {
			break
		}
	}

	// Stable order: score first, then title, so two runs of the same query over
	// an unchanged corpus return the same page.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].Title < hits[j].Title
	})
	if len(hits) > want {
		hits = hits[:want]
	}

	return repository.Page[SearchHit]{Items: hits, Cursor: cursor}, nil
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
func excerpt(text, term string) string {
	idx := strings.Index(strings.ToLower(text), term)
	if idx < 0 {
		return ""
	}
	runes := []rune(text)
	// Convert the byte offset to a rune offset.
	start := len([]rune(text[:idx]))
	from := start - excerptRadius
	if from < 0 {
		from = 0
	}
	to := start + len([]rune(term)) + excerptRadius
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
