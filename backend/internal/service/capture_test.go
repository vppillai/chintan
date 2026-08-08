package service

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider/fake"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestCaptureService_CreateCapture(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	stt := &fake.STT{}
	llm := &fake.LLM{}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test note
	note := model.NoteIndex{
		ID:    "note1",
		Title: "Test Note",
	}
	store.PutNote(context.Background(), "user1", note)

	ctx := context.Background()
	capture, uploadURL, err := service.CreateCapture(ctx, "user1", "note1", "audio/wav")

	if err != nil {
		t.Fatalf("CreateCapture failed: %v", err)
	}

	if capture.ID == "" {
		t.Error("Expected capture ID to be set")
	}

	if capture.Status != model.StatusUploaded {
		t.Errorf("Expected status %v, got %v", model.StatusUploaded, capture.Status)
	}

	if capture.UserID != "user1" {
		t.Errorf("Expected UserID user1, got %v", capture.UserID)
	}

	if capture.NoteID != "note1" {
		t.Errorf("Expected NoteID note1, got %v", capture.NoteID)
	}

	if uploadURL == "" {
		t.Error("Expected upload URL to be set")
	}
}

func TestCaptureService_CompleteCapture_HappyPath(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	stt := &fake.STT{Response: "Hello world"}
	llm := &fake.LLM{Response: "Clean: Hello world"}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
	note := model.NoteIndex{
		ID:            "note1",
		Title:         "Test Note",
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
		Snippet:       "Original content",
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

	// Put existing note content
	objects.Put(context.Background(), note.S3MarkdownKey, []byte("Original note content"), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusAppended {
		t.Errorf("Expected status %v, got %v", model.StatusAppended, result.Status)
	}

	// Check that note was updated with cleaned text
	updatedNote, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get updated note: %v", err)
	}

	expectedContent := "Original note content\n\nClean: Hello world"
	if string(updatedNote) != expectedContent {
		t.Errorf("Expected note content %q, got %q", expectedContent, string(updatedNote))
	}

	// Check that raw and clean files were stored
	rawData, err := objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != nil {
		t.Errorf("Expected raw.txt to be stored: %v", err)
	}
	if string(rawData) != "Hello world" {
		t.Errorf("Expected raw content 'Hello world', got %q", string(rawData))
	}

	cleanData, err := objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != nil {
		t.Errorf("Expected clean.txt to be stored: %v", err)
	}
	if string(cleanData) != "Clean: Hello world" {
		t.Errorf("Expected clean content 'Clean: Hello world', got %q", string(cleanData))
	}
}

func TestCaptureService_CompleteCapture_STTFailure(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	stt := &fake.STT{ShouldFail: true}
	llm := &fake.LLM{}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
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
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Put original note content
	originalContent := "Original note content"
	objects.Put(context.Background(), note.S3MarkdownKey, []byte(originalContent), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusFailed {
		t.Errorf("Expected status %v, got %v", model.StatusFailed, result.Status)
	}

	// Check that note was NOT modified
	noteContent, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get note: %v", err)
	}

	if string(noteContent) != originalContent {
		t.Errorf("Note content should not have changed, got %q", string(noteContent))
	}

	// Check that raw.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != repository.ErrNotFound {
		t.Error("raw.txt should not exist after STT failure")
	}

	// Check that clean.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != repository.ErrNotFound {
		t.Error("clean.txt should not exist after STT failure")
	}
}

func TestCaptureService_CompleteCapture_CleanupFailure(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	stt := &fake.STT{Response: "Hello world"}
	llm := &fake.LLM{ShouldFail: true}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
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
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Put original note content
	originalContent := "Original note content"
	objects.Put(context.Background(), note.S3MarkdownKey, []byte(originalContent), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusFailed {
		t.Errorf("Expected status %v, got %v", model.StatusFailed, result.Status)
	}

	// Check that note was NOT modified
	noteContent, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get note: %v", err)
	}

	if string(noteContent) != originalContent {
		t.Errorf("Note content should not have changed, got %q", string(noteContent))
	}

	// Check that raw.txt WAS created (STT succeeded)
	rawData, err := objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != nil {
		t.Errorf("raw.txt should exist after STT success: %v", err)
	}
	if string(rawData) != "Hello world" {
		t.Errorf("Expected raw content 'Hello world', got %q", string(rawData))
	}

	// Check that clean.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != repository.ErrNotFound {
		t.Error("clean.txt should not exist after cleanup failure")
	}
}

func TestCompleteCaptureIdempotent(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	stt := &fake.STT{Response: "raw words"}
	llm := &fake.LLM{Response: "cleaned words"}
	svc := NewCaptureService(store, objects, stt, llm)

	ctx := context.Background()
	userID := "user1"
	noteID := "n1"
	store.PutNote(ctx, userID, model.NoteIndex{
		ID: noteID, Title: "T", S3MarkdownKey: "tenants/user1/notes/n1/note.md",
	})
	objects.Put(ctx, "tenants/user1/notes/n1/note.md", []byte(""), "text/markdown")
	objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm")
	store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: userID, NoteID: noteID, Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm",
	})

	first, err := svc.CompleteCapture(ctx, userID, "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != model.StatusAppended {
		t.Fatalf("status=%s", first.Status)
	}
	second, err := svc.CompleteCapture(ctx, userID, "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != model.StatusAppended {
		t.Fatalf("second status=%s", second.Status)
	}
	body, _ := objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if strings.Count(string(body), "cleaned words") != 1 {
		t.Fatalf("expected single append, got %q", body)
	}
}
