package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/service"
)

// v1 truncated snippets with body[:500], a byte slice. Anything non-ASCII in the
// 500th byte was cut in half, and the invalid UTF-8 went into DynamoDB and then
// into the routing prompt.
func TestSnippetTruncationDoesNotCorruptMultiByteText(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Multibyte", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	// Devanagari: three bytes per rune, so a 500-byte cut lands mid-rune.
	body := strings.Repeat("चिन्तन ", 400)
	if !utf8.ValidString(body) {
		t.Fatal("test input is not valid UTF-8")
	}

	updated, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: &body})
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	if !utf8.ValidString(updated.Snippet) {
		t.Fatalf("snippet is not valid UTF-8: %q", updated.Snippet)
	}
	if strings.ContainsRune(updated.Snippet, utf8.RuneError) {
		t.Fatalf("snippet contains the replacement rune, so a character was cut in half: %q", updated.Snippet)
	}
	// 500 runes plus the ellipsis.
	if got := utf8.RuneCountInString(strings.TrimSuffix(updated.Snippet, "...")); got != 500 {
		t.Fatalf("snippet is %d runes, want 500 — truncation is still counting bytes", got)
	}

	// An emoji is four bytes; the same cut must survive it.
	emoji := strings.Repeat("🎤", 600)
	updated, err = notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: &emoji})
	if err != nil {
		t.Fatalf("UpdateNote(emoji): %v", err)
	}
	if strings.ContainsRune(updated.Snippet, utf8.RuneError) {
		t.Fatalf("emoji snippet was cut mid-rune: %q", updated.Snippet)
	}
}

func TestShortBodyIsNotTruncated(t *testing.T) {
	ctx := context.Background()
	notes := service.NewNotesService(memory.NewStore(), memory.NewObjects())
	note, err := notes.CreateNote(ctx, "user1", "Short", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	body := "चिन्तन"
	updated, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: &body})
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if updated.Snippet != body {
		t.Fatalf("snippet = %q, want the untouched body %q", updated.Snippet, body)
	}
}

// failingDeleteObjects refuses to delete one key, standing in for an S3 failure
// during a cascade.
type failingDeleteObjects struct {
	repository.Objects
	refuse string
}

func (o *failingDeleteObjects) Delete(ctx context.Context, key string) error {
	if key == o.refuse {
		return errors.New("induced S3 delete failure")
	}
	return o.Objects.Delete(ctx, key)
}

// v1 logged and ignored every cascade failure and then deleted the note index
// anyway, so the UI reported "purged" while the audio survived in S3 with
// nothing left pointing at it.
func TestPermanentDeleteFailsLoudlyAndLeavesTheNoteForRetry(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := &failingDeleteObjects{
		Objects: memory.NewObjects(),
		refuse:  "tenants/user1/captures/c1/audio.webm",
	}
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Doomed", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if err := objects.Put(ctx, objects.refuse, []byte("audio"), "audio/webm"); err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c1", UserID: "user1", NoteID: note.ID,
		AudioKey: objects.refuse, CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if _, err := notes.ArchiveNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}

	err = notes.PermanentlyDeleteNote(ctx, "user1", note.ID)
	if err == nil {
		t.Fatal("permanent delete reported success while the audio was still in object storage")
	}
	if !errors.Is(err, service.ErrPurgeIncomplete) {
		t.Fatalf("err = %v, want ErrPurgeIncomplete", err)
	}

	// The index must survive so the purge is retryable, rather than the audio
	// being orphaned with nothing pointing at it.
	if _, err := store.GetNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("note index was deleted despite the failed cascade: %v", err)
	}
	if _, err := store.GetCapture(ctx, "user1", "c1"); err != nil {
		t.Fatalf("capture index was deleted despite the failed cascade: %v", err)
	}
}

// The cascade has to see every capture, not just the first page. This is the
// other half of the unpaginated-query defect: a truncated list leaves orphans.
func TestPermanentDeleteCascadesBeyondOnePage(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Many captures", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	// More than one page's worth at the maximum page size.
	const total = int(repository.MaxListLimit) + 17
	base := time.Now().UTC()
	audioKeys := make([]string, 0, total)
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("tenants/user1/captures/c%04d/audio.webm", i)
		audioKeys = append(audioKeys, key)
		if err := objects.Put(ctx, key, []byte("audio"), "audio/webm"); err != nil {
			t.Fatalf("seed audio %d: %v", i, err)
		}
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: fmt.Sprintf("c%04d", i), UserID: "user1", NoteID: note.ID,
			AudioKey:  key,
			CreatedAt: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
		}); err != nil {
			t.Fatalf("seed capture %d: %v", i, err)
		}
	}

	if _, err := notes.ArchiveNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}
	if err := notes.PermanentlyDeleteNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("PermanentlyDeleteNote: %v", err)
	}

	for i, key := range audioKeys {
		if _, err := objects.Get(ctx, key); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("capture %d's audio survived the purge (%s); an unpaginated cascade orphans it", i, key)
		}
	}
	if _, err := store.GetNote(ctx, "user1", note.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("note index survived a successful purge: %v", err)
	}
}

// Matching has to page through the whole active set, not just whatever fitted
// in one page.
func TestMatchNotesSeesBeyondOnePage(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	notes := service.NewNotesService(store, memory.NewObjects())

	const total = int(repository.MaxListLimit) + 5
	for i := 0; i < total; i++ {
		if _, err := notes.CreateNote(ctx, "user1", fmt.Sprintf("Filler note %d", i), nil); err != nil {
			t.Fatalf("CreateNote %d: %v", i, err)
		}
	}
	// The needle is created last, so it sorts after everything else by id.
	if _, err := notes.CreateNote(ctx, "user1", "Zanzibar expedition", nil); err != nil {
		t.Fatalf("CreateNote(needle): %v", err)
	}

	result, err := notes.MatchNotes(ctx, "user1", "zanzibar expedition")
	if err != nil {
		t.Fatalf("MatchNotes: %v", err)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Title != "Zanzibar expedition" {
		t.Fatalf("match did not reach past the first page: %+v", result.Candidates)
	}
}

// The worker clears a capture's peaks key when the client had uploaded no
// peaks by the time the pipeline finished. A peaks object that arrives after
// that check is named by no attribute, so the cascade has to unlink it by the
// derived key or a purge leaves it behind.
func TestPermanentDeleteUnlinksPeaksTheCaptureNoLongerNames(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Late peaks", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	const latePeaks = "tenants/user1/captures/c_late/peaks.json"
	if err := objects.Put(ctx, latePeaks, []byte(`{"peaks":[0]}`), "application/json"); err != nil {
		t.Fatalf("seed peaks: %v", err)
	}
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_late", UserID: "user1", NoteID: note.ID, CreatedAt: model.Now(),
		AudioKey: "tenants/user1/captures/c_late/audio.webm",
		// PeaksKey deliberately empty: the worker found nothing at check time.
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	if _, err := notes.ArchiveNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}
	if err := notes.PermanentlyDeleteNote(ctx, "user1", note.ID); err != nil {
		t.Fatalf("PermanentlyDeleteNote: %v", err)
	}
	if _, err := objects.Get(ctx, latePeaks); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a peaks object the capture no longer named survived the purge: %v", err)
	}
}
