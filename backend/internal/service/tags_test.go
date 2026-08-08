package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// The counts are derived from the note index rather than kept in a counter
// item, because a denormalised counter has to be corrected on every archive,
// restore, purge and edit, and the first correction that is missed leaves a tag
// in the picker that matches nothing.
func TestListTagsCountsEveryActiveNoteThatCarriesTheTag(t *testing.T) {
	store := memory.NewStore()
	notes := NewNotesService(store, memory.NewObjects())
	svc := NewTagsService(notes)
	ctx := context.Background()

	for _, tags := range [][]string{
		{"roof", "garden"},
		{"roof"},
		{"apple"},
	} {
		if _, err := notes.CreateNoteWithTags(ctx, "user1", "A note", nil, tags); err != nil {
			t.Fatalf("CreateNoteWithTags(%v): %v", tags, err)
		}
	}

	got, err := svc.List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Most used first, then alphabetical, so two tags used equally often do not
	// swap places between requests.
	want := []TagCount{
		{Name: "roof", Count: 2},
		{Name: "apple", Count: 1},
		{Name: "garden", Count: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tags = %+v, want %+v (count descending, then name ascending)", got, want)
	}
}

// An archived note is out of the picker. Counting its tags offers a filter that
// returns nothing, which reads as a broken search rather than an empty archive.
func TestListTagsIgnoresAnArchivedNotesTags(t *testing.T) {
	store := memory.NewStore()
	notes := NewNotesService(store, memory.NewObjects())
	svc := NewTagsService(notes)
	ctx := context.Background()

	kept, err := notes.CreateNoteWithTags(ctx, "user1", "Kept", nil, []string{"roof"})
	if err != nil {
		t.Fatalf("CreateNoteWithTags: %v", err)
	}
	archived, err := notes.CreateNoteWithTags(ctx, "user1", "Archived", nil, []string{"roof", "attic"})
	if err != nil {
		t.Fatalf("CreateNoteWithTags: %v", err)
	}
	if _, err := notes.ArchiveNote(ctx, "user1", archived.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}

	got, err := svc.List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []TagCount{{Name: "roof", Count: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("tags = %+v, want %+v: only the active note %s should be counted", got, want, kept.ID)
	}
}

func TestListTagsIsEmptyForATenantWithNoTaggedNotes(t *testing.T) {
	store := memory.NewStore()
	notes := NewNotesService(store, memory.NewObjects())
	ctx := context.Background()

	if _, err := notes.CreateNote(ctx, "user1", "Untagged", nil); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	got, err := NewTagsService(notes).List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tags = %+v, want none", got)
	}
}

// Tags are folded to a canonical form on the way in, so "Roof", "roof " and
// "roof" are one tag to the picker as well as to the person who said them.
func TestListTagsCountsTheCanonicalFormOfATag(t *testing.T) {
	store := memory.NewStore()
	notes := NewNotesService(store, memory.NewObjects())
	ctx := context.Background()

	for _, tags := range [][]string{{"Roof"}, {"  roof  "}, {"ROOF"}} {
		if _, err := notes.CreateNoteWithTags(ctx, "user1", "A note", nil, tags); err != nil {
			t.Fatalf("CreateNoteWithTags(%v): %v", tags, err)
		}
	}

	got, err := NewTagsService(notes).List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []TagCount{{Name: "roof", Count: 3}}
	if !slices.Equal(got, want) {
		t.Fatalf("tags = %+v, want %+v: three spellings of one tag are one tag", got, want)
	}
}

func TestListTagsSurfacesAStoreFailure(t *testing.T) {
	boom := errors.New("dynamodb: dial tcp: connection refused")
	notes := NewNotesService(listErrStore{Store: memory.NewStore(), err: boom}, memory.NewObjects())

	if _, err := NewTagsService(notes).List(context.Background(), "user1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's failure rather than an empty tag list", err)
	}
}
