package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

func TestNotesService(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	t.Run("CreateNote", func(t *testing.T) {
		note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test", "note"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		if note.Title != "Test Note" {
			t.Errorf("expected title 'Test Note', got %s", note.Title)
		}
		if len(note.Aliases) != 2 || note.Aliases[0] != "test" || note.Aliases[1] != "note" {
			t.Errorf("expected aliases [test, note], got %v", note.Aliases)
		}
		if note.ID == "" {
			t.Errorf("expected non-empty ID")
		}
		if note.UpdatedAt == "" {
			t.Errorf("expected non-empty UpdatedAt")
		}
		if note.S3MarkdownKey == "" || note.S3MetaKey == "" {
			t.Errorf("expected S3 keys to be set")
		}
	})

	t.Run("ListNotes", func(t *testing.T) {
		// Create a note first
		_, err := notesService.CreateNote(ctx, userID, "Listed Note", []string{"list"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		notesPage, err := notesService.ListNotes(ctx, userID, repository.ListOptions{})
		notes := notesPage.Items
		if err != nil {
			t.Fatalf("ListNotes failed: %v", err)
		}

		if len(notes) == 0 {
			t.Errorf("expected at least one note")
		}

		found := false
		for _, note := range notes {
			if note.Title == "Listed Note" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find 'Listed Note' in list")
		}
	})

	t.Run("GetNote", func(t *testing.T) {
		// Create a note first
		created, err := notesService.CreateNote(ctx, userID, "Get Note", []string{"get"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		note, err := notesService.GetNote(ctx, userID, created.ID)
		if err != nil {
			t.Fatalf("GetNote failed: %v", err)
		}

		if note.Title != "Get Note" {
			t.Errorf("expected title 'Get Note', got %s", note.Title)
		}
		if note.ID != created.ID {
			t.Errorf("expected ID %s, got %s", created.ID, note.ID)
		}
	})

	t.Run("UpdateNote", func(t *testing.T) {
		// Create a note first
		created, err := notesService.CreateNote(ctx, userID, "Original", []string{"orig"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		// Wait a moment to ensure different timestamps
		time.Sleep(10 * time.Millisecond)

		updates := service.NoteUpdates{
			Title:   stringPtr("Updated Title"),
			Aliases: &[]string{"updated", "title"},
			Body:    stringPtr("This is the body content"),
		}

		updated, err := notesService.UpdateNote(ctx, userID, created.ID, updates)
		if err != nil {
			t.Fatalf("UpdateNote failed: %v", err)
		}

		if updated.Title != "Updated Title" {
			t.Errorf("expected title 'Updated Title', got %s", updated.Title)
		}
		if len(updated.Aliases) != 2 || updated.Aliases[0] != "updated" || updated.Aliases[1] != "title" {
			t.Errorf("expected aliases [updated, title], got %v", updated.Aliases)
		}
		if updated.UpdatedAt <= created.UpdatedAt {
			t.Errorf("expected UpdatedAt to change from %s to %s", created.UpdatedAt, updated.UpdatedAt)
		}

		// Verify snippet was updated from body
		if !strings.Contains(updated.Snippet, "This is the body content") {
			t.Errorf("expected snippet to contain body content, got: %s", updated.Snippet)
		}
	})

	t.Run("DeleteNote", func(t *testing.T) {
		// Create a note first
		created, err := notesService.CreateNote(ctx, userID, "To Delete", []string{"delete"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		err = notesService.DeleteNote(ctx, userID, created.ID)
		if err != nil {
			t.Fatalf("DeleteNote failed: %v", err)
		}

		// Verify it's archived (soft deleted, not hard deleted)
		note, err := notesService.GetNote(ctx, userID, created.ID)
		if err != nil {
			t.Fatalf("expected note to still exist after archive, got %v", err)
		}
		if note.DeletedAt == "" {
			t.Errorf("expected note to have DeletedAt set after delete")
		}
		if note.PurgeAfter == "" {
			t.Errorf("expected note to have PurgeAfter set after delete")
		}

		// Verify it's not in active list
		activeNotesPage, err := notesService.ListNotes(ctx, userID, repository.ListOptions{})
		activeNotes := activeNotesPage.Items
		if err != nil {
			t.Fatalf("ListNotes failed: %v", err)
		}
		for _, n := range activeNotes {
			if n.ID == created.ID {
				t.Errorf("archived note should not appear in active list")
			}
		}
	})

	t.Run("MatchNotes", func(t *testing.T) {
		// Create test notes
		_, err := notesService.CreateNote(ctx, userID, "Machine Learning", []string{"ml", "ai"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}
		_, err = notesService.CreateNote(ctx, userID, "Go Programming", []string{"golang"})
		if err != nil {
			t.Fatalf("CreateNote failed: %v", err)
		}

		result, err := notesService.MatchNotes(ctx, userID, "machine learning")
		if err != nil {
			t.Fatalf("MatchNotes failed: %v", err)
		}

		if len(result.Candidates) == 0 {
			t.Errorf("expected at least one candidate")
		}

		// Check if top candidate matches our expectation
		topCandidate := result.Candidates[0]
		if topCandidate.Title != "Machine Learning" {
			t.Errorf("expected top candidate to be 'Machine Learning', got %s", topCandidate.Title)
		}

		// For exact match, should have auto_select_id
		if result.AutoSelectID == nil || *result.AutoSelectID != topCandidate.NoteID {
			t.Errorf("expected auto_select_id for high confidence match")
		}
	})

	t.Run("MatchNotes ambiguous", func(t *testing.T) {
		result, err := notesService.MatchNotes(ctx, userID, "programming")
		if err != nil {
			t.Fatalf("MatchNotes failed: %v", err)
		}

		// Should have candidates but no auto-select for ambiguous match
		if len(result.Candidates) == 0 {
			t.Errorf("expected at least one candidate")
		}
		if result.AutoSelectID != nil {
			t.Errorf("should not have auto_select_id for ambiguous match")
		}
	})
}

func stringPtr(s string) *string {
	return &s
}
