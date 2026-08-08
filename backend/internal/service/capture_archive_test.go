package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestCreateCaptureRejectsArchivedNote(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	svc := NewCaptureService(store, objects, nil, nil)

	archivedNote := model.NoteIndex{
		ID:        "archived-note",
		Title:     "Archived Note",
		DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := store.PutNote(context.Background(), "user1", archivedNote); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	ctx := context.Background()
	_, _, err := svc.CreateCapture(ctx, "user1", "archived-note", "audio/wav")
	if err == nil {
		t.Fatal("Expected CreateCapture to fail for archived note")
	}
	if !errors.Is(err, ErrNoteArchived) {
		t.Errorf("Expected ErrNoteArchived, got %v", err)
	}
}

func TestSetCaptureTargetRejectsArchivedNote(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	svc := NewCaptureService(store, objects, nil, nil).WithQueue(&stubQueue{})

	archivedNote := model.NoteIndex{
		ID:        "archived-note",
		Title:     "Archived Note",
		DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := store.PutNote(context.Background(), "user1", archivedNote); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	capture := model.CaptureIndex{
		ID:     "capture1",
		UserID: "user1",
		Status: model.StatusNeedsTarget,
	}
	if _, err := store.PutCapture(context.Background(), capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	ctx := context.Background()
	_, err := svc.SetCaptureTarget(ctx, "user1", "capture1", "archived-note", "")
	if err == nil {
		t.Fatal("Expected SetCaptureTarget to fail for archived note")
	}
	if !errors.Is(err, ErrNoteArchived) {
		t.Errorf("Expected ErrNoteArchived, got %v", err)
	}
}
