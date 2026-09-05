package service

import (
	"context"
	"sort"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// maxTagScanNotes bounds how many notes a tag count will read. Personal scale is
// hundreds of notes; the bound exists so the endpoint cannot become the one
// unbounded query in an API whose stated rule is that none survive.
const maxTagScanNotes = 2000

// TagCount is one tag and how many active notes carry it.
type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TagsService answers GET /v1/tags.
//
// The counts are derived from the note index rather than kept in a TAG# item,
// because a denormalised counter has to be corrected on every archive, restore,
// purge and edit, and the first one of those that is missed leaves a tag in the
// picker that matches nothing.
type TagsService struct {
	notes *NotesService
}

// NewTagsService builds the tags service over the note index.
func NewTagsService(notes *NotesService) *TagsService {
	return &TagsService{notes: notes}
}

// List returns every tag in use on an active note, most used first.
func (s *TagsService) List(ctx context.Context, userID string) ([]TagCount, error) {
	notes, err := s.notes.DrainNotes(ctx, userID, repository.DrainOptions{MaxItems: maxTagScanNotes})
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, note := range notes {
		for _, tag := range note.Tags {
			counts[tag]++
		}
	}

	out := make([]TagCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, TagCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
