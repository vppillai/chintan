package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
)

func TestCreateCaptureRejectsArchivedNote(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{}
	llm := &provider.FakeLLM{}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up archived note with DeletedAt set
	archivedNote := model.NoteIndex{
		ID:        "archived-note",
		Title:     "Archived Note",
		DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	store.PutNote(context.Background(), "user1", archivedNote)

	ctx := context.Background()
	_, _, err := service.CreateCapture(ctx, "user1", "archived-note", "audio/wav")

	if err == nil {
		t.Fatal("Expected CreateCapture to fail for archived note")
	}

	if !errors.Is(err, ErrNoteArchived) {
		t.Errorf("Expected ErrNoteArchived, got %v", err)
	}
}

func TestCompleteCaptureRejectsArchivedNote(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "Hello world"}
	llm := &provider.FakeLLM{Response: "Clean: Hello world"}

	service := NewCaptureService(store, objects, stt, llm)

	// Create a capture bound to a note, then archive the note
	note := model.NoteIndex{
		ID:            "note1",
		Title:         "Test Note",
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
	}
	store.PutNote(context.Background(), "user1", note)

	capture := model.CaptureIndex{
		ID:       "capture1",
		UserID:   "user1",
		NoteID:   "note1",
		Status:   model.StatusUploaded,
		Mode:     model.CleanupFaithful,
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Now archive the note
	note.DeletedAt = time.Now().UTC().Format(time.RFC3339)
	store.PutNote(context.Background(), "user1", note)

	ctx := context.Background()
	_, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err == nil {
		t.Fatal("Expected CompleteCapture to fail for archived note")
	}

	if !errors.Is(err, ErrNoteArchived) {
		t.Errorf("Expected ErrNoteArchived, got %v", err)
	}
}

func TestSetCaptureTargetRejectsArchivedNote(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "Hello world"}
	llm := &provider.FakeLLM{Response: "Clean: Hello world"}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up archived note
	archivedNote := model.NoteIndex{
		ID:        "archived-note",
		Title:     "Archived Note",
		DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	store.PutNote(context.Background(), "user1", archivedNote)

	// Create a capture with no target
	capture := model.CaptureIndex{
		ID:     "capture1",
		UserID: "user1",
		Status: model.StatusNeedsTarget,
	}
	store.PutCapture(context.Background(), capture)

	ctx := context.Background()
	_, err := service.SetCaptureTarget(ctx, "user1", "capture1", "archived-note", "")

	if err == nil {
		t.Fatal("Expected SetCaptureTarget to fail for archived note")
	}

	if !errors.Is(err, ErrNoteArchived) {
		t.Errorf("Expected ErrNoteArchived, got %v", err)
	}
}

func TestDecideTargetSkipsArchivedNotes(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "Hello world"}
	llm := &provider.FakeLLM{Response: "Clean: Hello world"}

	// Use FakeRouter that records candidates
	fakeRouter := &provider.FakeRouter{
		Decision: provider.RouteDecision{
			Action:     provider.RouteNew,
			Title:      "New Note",
			Content:    "Hello world",
			Confidence: 0.9,
		},
	}

	service := NewCaptureService(store, objects, stt, llm).WithRouting(fakeRouter, &mockNoteCreator{store: store})

	// Create active and archived notes
	activeNote := model.NoteIndex{
		ID:        "active-note",
		Title:     "Active Note",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	store.PutNote(context.Background(), "user1", activeNote)

	archivedNote := model.NoteIndex{
		ID:        "archived-note",
		Title:     "Archived Note",
		UpdatedAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano), // older
		DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	store.PutNote(context.Background(), "user1", archivedNote)

	// Create capture for routing
	capture := model.CaptureIndex{
		ID:       "capture1",
		UserID:   "user1",
		Status:   model.StatusUploaded,
		Mode:     model.CleanupFaithful,
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	ctx := context.Background()
	_, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	// Check that the router received candidates but archived note ID is absent
	if len(fakeRouter.LastCandidates) == 0 {
		t.Fatal("Router should have received candidates")
	}

	for _, candidate := range fakeRouter.LastCandidates {
		if candidate.NoteID == "archived-note" {
			t.Error("Archived note should not be included in routing candidates")
		}
	}

	// Should only see the active note
	foundActive := false
	for _, candidate := range fakeRouter.LastCandidates {
		if candidate.NoteID == "active-note" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Error("Active note should be included in routing candidates")
	}
}

// mockNoteCreator for testing
type mockNoteCreator struct {
	store *mockStore
}

func (m *mockNoteCreator) CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error) {
	note := model.NoteIndex{
		ID:            "new-note-id",
		Title:         title,
		Aliases:       aliases,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		S3MarkdownKey: "tenants/" + userID + "/notes/new-note-id/note.md",
		S3MetaKey:     "tenants/" + userID + "/notes/new-note-id/meta.json",
	}
	return note, m.store.PutNote(ctx, userID, note)
}
