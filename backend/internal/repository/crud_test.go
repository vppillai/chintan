package repository_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The plain reads and writes every service leans on, proven on the real store
// over the fake table. They came from the in-memory Store's own tests when
// that double went (CH-O2); the rest of that file was already covered here.

func TestGetSettingsDefault(t *testing.T) {
	store, _ := newTestStore(t)
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
	store, _ := newTestStore(t)
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
	store, _ := newTestStore(t)
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
	store, _ := newTestStore(t)
	_, err := store.GetNote(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListNotesEmpty(t *testing.T) {
	store, _ := newTestStore(t)
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
	store, _ := newTestStore(t)
	err := store.DeleteNote(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCaptureCRUD(t *testing.T) {
	store, _ := newTestStore(t)
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
	// Not ==: the row carries a map (StageAt), so the struct is not comparable.
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("got capture %+v, want %+v", got, stored)
	}
}

func TestGetCaptureNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.GetCapture(context.Background(), "user1", "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteCapture(t *testing.T) {
	store, _ := newTestStore(t)
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
