package service_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

// appendRacingObjects lands a pipeline-style voice append on the note body at
// the moment the editor writes it — that is, inside the window between
// UpdateNote's version check and its own object write. It is the interleaving
// the version check cannot see, because the pipeline writes S3 first and
// refreshes the index afterwards.
type appendRacingObjects struct {
	repository.Objects
	key  string
	text string
	done bool
}

func (o *appendRacingObjects) race(ctx context.Context, key string) error {
	if o.done || key != o.key {
		return nil
	}
	o.done = true
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	body, etag, err := o.Objects.GetWithETag(ctx, key)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	merged := string(body) + "\n\n" + o.text
	return o.Objects.PutIfMatch(ctx, key, []byte(merged), "text/markdown", etag)
}

func (o *appendRacingObjects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := o.race(ctx, key); err != nil {
		return err
	}
	return o.Objects.Put(ctx, key, body, contentType)
}

func (o *appendRacingObjects) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if err := o.race(ctx, key); err != nil {
		return err
	}
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	return o.Objects.PutIfMatch(ctx, key, body, contentType, etag)
}

// A losing editor save must lose without taking a concurrent voice append with
// it. Writing the body unconditionally before the version-checked index write
// means the editor's Put wipes the spoken paragraph out of S3 and only then
// reports a conflict: the user is told to re-read text that no longer exists.
func TestUpdateNoteDoesNotDestroyAVoiceAppendThatLandsMidSave(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Roof", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	const editorBase = "The roof needs looking at."
	opened, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: stringPtr(editorBase)})
	if err != nil {
		t.Fatalf("UpdateNote(seed body): %v", err)
	}

	const spoken = "Ellis quoted nine hundred pounds."
	racing := &appendRacingObjects{Objects: objects, key: note.S3MarkdownKey, text: spoken}
	racingNotes := service.NewNotesService(store, racing)

	version := opened.Version
	_, err = racingNotes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{
		Body:            stringPtr(editorBase + " Ringing Ellis today."),
		ExpectedVersion: &version,
	})

	if !racing.done {
		t.Fatal("the test never interleaved an append, so it proves nothing")
	}

	body, getErr := objects.Get(ctx, note.S3MarkdownKey)
	if getErr != nil {
		t.Fatalf("Get(note body): %v", getErr)
	}
	if !strings.Contains(string(body), spoken) {
		t.Errorf("the note body is %q; the spoken paragraph %q was overwritten and is gone from S3 forever", body, spoken)
	}
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Errorf("UpdateNote err = %v, want repository.ErrVersionConflict so the client re-reads and merges", err)
	}
}

// The uncontended save must still work: the conditional write is only there to
// catch the race, not to make ordinary editing fail.
func TestUpdateNoteStillWritesTheBodyWhenNothingRaces(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	objects := memory.NewObjects()
	notes := service.NewNotesService(store, objects)

	note, err := notes.CreateNote(ctx, "user1", "Roof", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	const body = "The roof needs looking at."
	updated, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: stringPtr(body)})
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if updated.Snippet != body {
		t.Errorf("snippet = %q, want %q", updated.Snippet, body)
	}
	stored, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Get(note body): %v", err)
	}
	if string(stored) != body {
		t.Errorf("stored body = %q, want %q", stored, body)
	}

	// And a second edit, over an object that now exists with a live ETag.
	const second = "Rang Ellis; he quoted nine hundred."
	version := updated.Version
	if _, err := notes.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{
		Body:            stringPtr(second),
		ExpectedVersion: &version,
	}); err != nil {
		t.Fatalf("UpdateNote(second): %v", err)
	}
	stored, err = objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Get(note body): %v", err)
	}
	if string(stored) != second {
		t.Errorf("stored body = %q, want %q", stored, second)
	}
}

// bodyObjectFaultObjects fails one operation on the note body so a transport
// fault can be told apart from a lost race.
type bodyObjectFaultObjects struct {
	repository.Objects
	key      string
	err      error
	failRead bool
}

func (o bodyObjectFaultObjects) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	if o.failRead && key == o.key {
		return nil, "", o.err
	}
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	return o.Objects.GetWithETag(ctx, key)
}

func (o bodyObjectFaultObjects) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if !o.failRead && key == o.key {
		return o.err
	}
	//nolint:staticcheck // o.Objects.X is this wrapper's "call the real store" idiom.
	return o.Objects.PutIfMatch(ctx, key, body, contentType, etag)
}

// S3 being unreachable is not a conflict. Reporting it as one sends the client
// off to re-read and merge a note that nothing has changed.
func TestUpdateNoteReportsAnObjectStoreFaultAsAFaultNotAConflict(t *testing.T) {
	boom := errors.New("s3: dial tcp: connection refused")

	for name, failRead := range map[string]bool{"reading the body": true, "writing the body": false} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memory.NewStore()
			objects := memory.NewObjects()
			notes := service.NewNotesService(store, objects)

			note, err := notes.CreateNote(ctx, "user1", "Roof", nil)
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}

			faulty := service.NewNotesService(store, bodyObjectFaultObjects{
				Objects: objects, key: note.S3MarkdownKey, err: boom, failRead: failRead,
			})
			_, err = faulty.UpdateNote(ctx, "user1", note.ID, service.NoteUpdates{Body: stringPtr("anything")})

			if !errors.Is(err, boom) {
				t.Fatalf("UpdateNote err = %v, want the object store's failure", err)
			}
			if errors.Is(err, repository.ErrVersionConflict) {
				t.Fatalf("UpdateNote reported an unreachable object store as a version conflict: %v", err)
			}
		})
	}
}

// noteIDShape is the id capture ids already use: a fixed-width sortable
// creation instant, then random bytes.
var noteIDShape = regexp.MustCompile(`^note_([0-9a-f]{16})_([0-9a-f]{16})$`)

// A note id built from time.Now().UnixNano() alone is guessable from the
// creation time and collides whenever two notes are created inside the same
// nanosecond — which surfaces to the user as an unexplained 409 on a note they
// just made. Capture and export ids already carry crypto/rand bytes.
func TestCreateNoteIDCarriesRandomnessNotJustTheWallClock(t *testing.T) {
	ctx := context.Background()
	notes := service.NewNotesService(memory.NewStore(), memory.NewObjects())

	const runs = 200
	ids := make(map[string]struct{}, runs)
	suffixes := make(map[string]struct{}, runs)
	var previousStamp uint64

	before := uint64(time.Now().UTC().Add(-time.Minute).UnixNano())
	for i := range runs {
		note, err := notes.CreateNote(ctx, "user1", fmt.Sprintf("Note %d", i), nil)
		if err != nil {
			t.Fatalf("CreateNote: %v", err)
		}
		parts := noteIDShape.FindStringSubmatch(note.ID)
		if parts == nil {
			t.Fatalf("note id %q does not carry a sortable time prefix and a random suffix", note.ID)
		}
		stamp, err := strconv.ParseUint(parts[1], 16, 64)
		if err != nil {
			t.Fatalf("note id %q: time prefix does not parse: %v", note.ID, err)
		}
		if stamp < before {
			t.Fatalf("note id %q: time prefix %d predates the test", note.ID, stamp)
		}
		if stamp < previousStamp {
			t.Fatalf("note id %q: time prefix went backwards, so ids no longer sort chronologically", note.ID)
		}
		previousStamp = stamp

		if _, dup := ids[note.ID]; dup {
			t.Fatalf("note id %q was issued twice", note.ID)
		}
		ids[note.ID] = struct{}{}
		suffixes[parts[2]] = struct{}{}
	}

	// The suffix is what makes an id unguessable to somebody who knows when the
	// note was made, so it has to vary independently of the clock.
	if len(suffixes) != runs {
		t.Fatalf("%d distinct random suffixes across %d ids, want %d", len(suffixes), runs, runs)
	}
}

// Sorting by id is how notes come back in creation order, so the prefix has to
// stay fixed-width and lexicographically ordered.
func TestCreateNoteIDsSortInCreationOrder(t *testing.T) {
	ctx := context.Background()
	notes := service.NewNotesService(memory.NewStore(), memory.NewObjects())

	var previous string
	for i := range 25 {
		note, err := notes.CreateNote(ctx, "user1", fmt.Sprintf("Note %d", i), nil)
		if err != nil {
			t.Fatalf("CreateNote: %v", err)
		}
		if previous != "" && note.ID <= previous {
			t.Fatalf("note id %q does not sort after the one created before it, %q", note.ID, previous)
		}
		previous = note.ID
	}
}
