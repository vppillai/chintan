package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestGetSettingsDefault(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	got, err := store.GetSettings(ctx, "user1")
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	want := model.Settings{CleanupMode: model.CleanupFaithful, RetentionDays: 0}
	if got != want {
		t.Fatalf("got settings %+v, want %+v", got, want)
	}
}

func TestPutGetSettings(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	want := model.Settings{CleanupMode: model.CleanupPolished, RetentionDays: 30}
	if err := store.PutSettings(ctx, "user1", want); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	got, err := store.GetSettings(ctx, "user1")
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got != want {
		t.Fatalf("got settings %+v, want %+v", got, want)
	}
}

func TestNoteCRUD(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	userID := "user1"

	note := model.NoteIndex{
		ID:            "n1",
		Title:         "Meeting Notes",
		Aliases:       []string{"standup"},
		Snippet:       "discussed roadmap",
		UpdatedAt:     "2026-08-06T12:00:00Z",
		S3MarkdownKey: "tenants/user1/notes/n1/note.md",
		S3MetaKey:     "tenants/user1/notes/n1/meta.json",
	}
	if _, err := store.PutNote(ctx, userID, note); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	got, err := store.GetNote(ctx, userID, "n1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got.ID != note.ID || got.Title != note.Title || got.Snippet != note.Snippet ||
		got.UpdatedAt != note.UpdatedAt || got.S3MarkdownKey != note.S3MarkdownKey ||
		got.S3MetaKey != note.S3MetaKey || len(got.Aliases) != len(note.Aliases) {
		t.Fatalf("got note %+v, want %+v", got, note)
	}
	for i, a := range note.Aliases {
		if got.Aliases[i] != a {
			t.Fatalf("got aliases %+v, want %+v", got.Aliases, note.Aliases)
		}
	}

	notesPage, err := store.ListNotes(ctx, userID, repository.ListOptions{})
	notes := notesPage.Items
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "n1" {
		t.Fatalf("ListNotes = %+v, want one note n1", notes)
	}

	if err := store.DeleteNote(ctx, userID, "n1"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	_, err = store.GetNote(ctx, userID, "n1")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("after delete GetNote err = %v, want ErrNotFound", err)
	}
}

func TestGetNoteNotFound(t *testing.T) {
	store := memory.NewStore()
	_, err := store.GetNote(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListNotesEmpty(t *testing.T) {
	store := memory.NewStore()
	notesPage, err := store.ListNotes(context.Background(), "user1", repository.ListOptions{})
	notes := notesPage.Items
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if notes == nil || len(notes) != 0 {
		t.Fatalf("ListNotes = %v, want empty non-nil slice", notes)
	}
}

func TestDeleteNoteNotFound(t *testing.T) {
	store := memory.NewStore()
	err := store.DeleteNote(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNotesScopedByUser(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	note := model.NoteIndex{ID: "n1", Title: "Shared title"}
	if _, err := store.PutNote(ctx, "user1", note); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	_, err := store.GetNote(ctx, "user2", "n1")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("user2 GetNote err = %v, want ErrNotFound", err)
	}
}

func TestCaptureCRUD(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	capture := model.CaptureIndex{
		ID:        "c1",
		NoteID:    "n1",
		UserID:    "user1",
		Status:    model.StatusUploaded,
		Mode:      model.CleanupFaithful,
		AudioKey:  "tenants/user1/captures/c1/audio.webm",
		RawKey:    "tenants/user1/captures/c1/raw.txt",
		CleanKey:  "tenants/user1/captures/c1/clean.txt",
		CreatedAt: "2026-08-06T12:00:00Z",
	}
	// The store stamps the next version on write, so compare against what it
	// returned rather than against the pre-write value.
	stored, err := store.PutCapture(ctx, capture)
	if err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	if stored.Version != capture.Version+1 {
		t.Fatalf("stored version = %d, want %d", stored.Version, capture.Version+1)
	}

	got, err := store.GetCapture(ctx, "user1", "c1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if got != stored {
		t.Fatalf("got capture %+v, want %+v", got, stored)
	}
}

func TestUpdateCaptureStatus(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	capture := model.CaptureIndex{
		ID:     "c1",
		UserID: "user1",
		Status: model.StatusUploaded,
	}
	if _, err := store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	if err := store.UpdateCaptureStatus(ctx, "user1", "c1", model.StatusFailed, "stt timeout"); err != nil {
		t.Fatalf("UpdateCaptureStatus: %v", err)
	}

	got, err := store.GetCapture(ctx, "user1", "c1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if got.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error != "stt timeout" {
		t.Fatalf("error = %q, want stt timeout", got.Error)
	}
}

func TestGetCaptureNotFound(t *testing.T) {
	store := memory.NewStore()
	_, err := store.GetCapture(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteCapture(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	c := model.CaptureIndex{ID: "c1", UserID: "user1", NoteID: "n1", Status: model.StatusUploaded}
	if _, err := store.PutCapture(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCapture(ctx, "user1", "c1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if _, err := store.GetCapture(ctx, "user1", "c1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := store.DeleteCapture(ctx, "user1", "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

func TestObjectsPutGetDelete(t *testing.T) {
	objs := memory.NewObjects()
	ctx := context.Background()
	key := "tenants/user1/notes/n1/note.md"
	body := []byte("# Hello")

	if err := objs.Put(ctx, key, body, "text/markdown"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := objs.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("Get = %q, want %q", got, body)
	}
	if err := objs.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = objs.Get(ctx, key)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("after delete Get err = %v, want ErrNotFound", err)
	}
}

func TestObjectsGetNotFound(t *testing.T) {
	objs := memory.NewObjects()
	_, err := objs.Get(context.Background(), "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestObjectsPresignPutGet(t *testing.T) {
	objs := memory.NewObjects()
	ctx := context.Background()
	key := "tenants/user1/captures/c1/audio.webm"
	ttl := 15 * time.Minute

	putURL, err := objs.PresignPut(ctx, key, "audio/webm", ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if putURL == "" {
		t.Fatal("PresignPut returned empty URL")
	}

	getURL, err := objs.PresignGet(ctx, key, ttl)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if getURL == "" {
		t.Fatal("PresignGet returned empty URL")
	}
}
