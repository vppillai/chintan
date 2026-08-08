package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

func TestArchiveNoteHidesFromActiveList(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create a note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	// Verify it's in active list
	activeNotes, err := notesService.ListNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListNotes failed: %v", err)
	}
	if len(activeNotes) == 0 || activeNotes[0].ID != note.ID {
		t.Errorf("expected note %s in active list", note.ID)
	}

	// Archive the note
	archivedNote, err := notesService.ArchiveNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Verify DeletedAt and PurgeAfter are set
	if archivedNote.DeletedAt == "" {
		t.Errorf("expected DeletedAt to be set")
	}
	if archivedNote.PurgeAfter == "" {
		t.Errorf("expected PurgeAfter to be set")
	}

	// Verify it's hidden from active list
	activeNotes, err = notesService.ListNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListNotes failed: %v", err)
	}
	for _, n := range activeNotes {
		if n.ID == note.ID {
			t.Errorf("archived note %s should not be in active list", note.ID)
		}
	}

	// Verify it's in archived list
	archivedNotes, err := notesService.ListArchivedNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListArchivedNotes failed: %v", err)
	}
	found := false
	for _, n := range archivedNotes {
		if n.ID == note.ID {
			found = true
			if n.DeletedAt == "" || n.PurgeAfter == "" {
				t.Errorf("archived note should have DeletedAt and PurgeAfter set")
			}
		}
	}
	if !found {
		t.Errorf("expected archived note %s in archived list", note.ID)
	}
}

func TestRestoreNoteReturnsToActive(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create and archive a note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	_, err = notesService.ArchiveNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Restore the note
	restoredNote, err := notesService.RestoreNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("RestoreNote failed: %v", err)
	}

	// Verify DeletedAt and PurgeAfter are cleared
	if restoredNote.DeletedAt != "" {
		t.Errorf("expected DeletedAt to be cleared, got %s", restoredNote.DeletedAt)
	}
	if restoredNote.PurgeAfter != "" {
		t.Errorf("expected PurgeAfter to be cleared, got %s", restoredNote.PurgeAfter)
	}

	// Verify it's back in active list
	activeNotes, err := notesService.ListNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListNotes failed: %v", err)
	}
	found := false
	for _, n := range activeNotes {
		if n.ID == note.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("restored note %s should be in active list", note.ID)
	}

	// Verify it's not in archived list
	archivedNotes, err := notesService.ListArchivedNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListArchivedNotes failed: %v", err)
	}
	for _, n := range archivedNotes {
		if n.ID == note.ID {
			t.Errorf("restored note %s should not be in archived list", note.ID)
		}
	}
}

func TestPermanentlyDeleteRequiresArchive(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create an active note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	// Try to permanently delete while active - should fail
	err = notesService.PermanentlyDeleteNote(ctx, userID, note.ID)
	if err != service.ErrNoteNotArchived {
		t.Errorf("expected ErrNoteNotArchived, got %v", err)
	}

	// Archive the note first
	_, err = notesService.ArchiveNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Now permanently delete should work
	err = notesService.PermanentlyDeleteNote(ctx, userID, note.ID)
	if err != nil {
		t.Errorf("PermanentlyDeleteNote failed after archive: %v", err)
	}
}

func TestPermanentlyDeleteCascadesCaptures(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create a note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	// Create S3 objects that will be referenced by captures
	audioKey := "captures/audio_123.m4a"
	rawKey := "captures/raw_123.txt"
	cleanKey := "captures/clean_123.txt"
	routedKey := "captures/routed_123.txt"

	err = objects.Put(ctx, audioKey, []byte("audio data"), "audio/m4a")
	if err != nil {
		t.Fatalf("Failed to put audio object: %v", err)
	}
	err = objects.Put(ctx, rawKey, []byte("raw transcript"), "text/plain")
	if err != nil {
		t.Fatalf("Failed to put raw object: %v", err)
	}
	err = objects.Put(ctx, cleanKey, []byte("clean transcript"), "text/plain")
	if err != nil {
		t.Fatalf("Failed to put clean object: %v", err)
	}
	err = objects.Put(ctx, routedKey, []byte("routed transcript"), "text/plain")
	if err != nil {
		t.Fatalf("Failed to put routed object: %v", err)
	}

	// Create captures linked to the note
	capture := createCaptureWithKeys(userID, note.ID, audioKey, rawKey, cleanKey, routedKey)
	err = store.PutCapture(ctx, capture)
	if err != nil {
		t.Fatalf("Failed to put capture: %v", err)
	}

	// Archive the note
	_, err = notesService.ArchiveNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Permanently delete the note
	err = notesService.PermanentlyDeleteNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("PermanentlyDeleteNote failed: %v", err)
	}

	// Verify note is gone
	_, err = notesService.GetNote(ctx, userID, note.ID)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted note, got %v", err)
	}

	// Verify capture is gone
	_, err = store.GetCapture(ctx, userID, capture.ID)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted capture, got %v", err)
	}

	// Verify S3 objects are gone
	_, err = objects.Get(ctx, audioKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted audio object, got %v", err)
	}
	_, err = objects.Get(ctx, rawKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted raw object, got %v", err)
	}
	_, err = objects.Get(ctx, cleanKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted clean object, got %v", err)
	}
	_, err = objects.Get(ctx, routedKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted routed object, got %v", err)
	}

	// Verify note's S3 objects are gone too
	_, err = objects.Get(ctx, note.S3MarkdownKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted note markdown, got %v", err)
	}
	_, err = objects.Get(ctx, note.S3MetaKey)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted note meta, got %v", err)
	}
}

func TestLazyPurgeExpiredOnList(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create a note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	// Manually set it as expired (PurgeAfter in the past)
	note.DeletedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	note.PurgeAfter = time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)
	err = store.PutNote(ctx, userID, note)
	if err != nil {
		t.Fatalf("Failed to update note with expired purge time: %v", err)
	}

	// Call ListNotes - should trigger lazy purge
	activeNotes, err := notesService.ListNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListNotes failed: %v", err)
	}

	// Note should be gone from active list (and purged)
	for _, n := range activeNotes {
		if n.ID == note.ID {
			t.Errorf("expired note %s should be purged from active list", note.ID)
		}
	}

	// Call ListArchivedNotes - should also not find it
	archivedNotes, err := notesService.ListArchivedNotes(ctx, userID)
	if err != nil {
		t.Fatalf("ListArchivedNotes failed: %v", err)
	}

	for _, n := range archivedNotes {
		if n.ID == note.ID {
			t.Errorf("expired note %s should be purged from archived list", note.ID)
		}
	}

	// Verify note is completely gone from store
	_, err = store.GetNote(ctx, userID, note.ID)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for purged note, got %v", err)
	}
}

func TestUpdateArchivedNoteRejected(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create and archive a note
	note, err := notesService.CreateNote(ctx, userID, "Test Note", []string{"test"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	_, err = notesService.ArchiveNote(ctx, userID, note.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Try to update archived note - should fail
	updates := service.NoteUpdates{
		Title: stringPtr("Updated Title"),
	}
	_, err = notesService.UpdateNote(ctx, userID, note.ID, updates)
	if err != service.ErrNoteArchived {
		t.Errorf("expected ErrNoteArchived, got %v", err)
	}
}

func TestMatchNotesSkipsArchived(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)

	ctx := context.Background()
	userID := "test-user"

	// Create two notes with similar titles
	activeNote, err := notesService.CreateNote(ctx, userID, "Machine Learning Basics", []string{"ml"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	archivedNote, err := notesService.CreateNote(ctx, userID, "Machine Learning Advanced", []string{"ml", "advanced"})
	if err != nil {
		t.Fatalf("CreateNote failed: %v", err)
	}

	// Archive the second note
	_, err = notesService.ArchiveNote(ctx, userID, archivedNote.ID)
	if err != nil {
		t.Fatalf("ArchiveNote failed: %v", err)
	}

	// Match notes with "machine learning"
	result, err := notesService.MatchNotes(ctx, userID, "machine learning")
	if err != nil {
		t.Fatalf("MatchNotes failed: %v", err)
	}

	// Should only find the active note
	found := false
	for _, candidate := range result.Candidates {
		if candidate.NoteID == activeNote.ID {
			found = true
		}
		if candidate.NoteID == archivedNote.ID {
			t.Errorf("archived note %s should not appear in match results", archivedNote.ID)
		}
	}
	if !found {
		t.Errorf("active note %s should appear in match results", activeNote.ID)
	}
}

// Helper function to create a capture with S3 keys
func createCaptureWithKeys(userID, noteID, audioKey, rawKey, cleanKey, routedKey string) model.CaptureIndex {
	return model.CaptureIndex{
		ID:        "capture_test_123",
		NoteID:    noteID,
		UserID:    userID,
		Status:    model.StatusAppended,
		Mode:      model.CleanupFaithful,
		AudioKey:  audioKey,
		RawKey:    rawKey,
		CleanKey:  cleanKey,
		RoutedKey: routedKey,
		CreatedAt: time.Now().Format(time.RFC3339Nano),
	}
}
