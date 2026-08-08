package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/match"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const ArchiveRetention = 30 * 24 * time.Hour

// maxNoteTitleLen bounds a stored title. Titles can be dictated, and they are rendered
// back into the routing prompt for later recordings.
const maxNoteTitleLen = 120

var (
	ErrNoteArchived    = errors.New("note is archived")
	ErrNoteNotArchived = errors.New("note is not archived")
	ErrEmptyNoteTitle  = errors.New("note title is empty")
	// ErrPurgeIncomplete means part of a permanent delete failed. The note index
	// is deliberately left in place so the delete can be retried, because
	// reporting "purged" while audio survives in S3 is worse than reporting a
	// failure.
	ErrPurgeIncomplete = errors.New("note purge incomplete")
)

// maxMatchCandidates bounds how many notes note-matching will page through.
const maxMatchCandidates = 500

// sanitizeNoteTitle collapses a title to a single bounded line.
func sanitizeNoteTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > maxNoteTitleLen {
		title = strings.TrimSpace(string(runes[:maxNoteTitleLen]))
	}
	return title
}

func NoteIsActive(n model.NoteIndex) bool {
	return strings.TrimSpace(n.DeletedAt) == ""
}

// NotesService handles note operations
type NotesService struct {
	store   repository.Store
	objects repository.Objects
}

// NoteUpdates represents partial updates to a note
type NoteUpdates struct {
	Title   *string
	Aliases *[]string
	Body    *string
}

// MatchResult represents the result of a note matching operation
type MatchResult struct {
	Candidates   []match.Candidate `json:"candidates"`
	AutoSelectID *string           `json:"auto_select_id,omitempty"`
}

// NewNotesService creates a new notes service
func NewNotesService(store repository.Store, objects repository.Objects) *NotesService {
	return &NotesService{
		store:   store,
		objects: objects,
	}
}

// CreateNote creates a new note
func (s *NotesService) CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error) {
	title = sanitizeNoteTitle(title)
	if title == "" {
		return model.NoteIndex{}, ErrEmptyNoteTitle
	}

	// Generate a simple ID (in production, use UUID or similar)
	noteID := fmt.Sprintf("note_%d", time.Now().UnixNano())

	// Generate S3 keys
	markdownKey, err := keys.NoteMarkdown(userID, noteID)
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to generate markdown key: %w", err)
	}

	metaKey, err := keys.NoteMeta(userID, noteID)
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to generate meta key: %w", err)
	}

	if aliases == nil {
		aliases = []string{}
	}

	note := model.NoteIndex{
		ID:            noteID,
		Title:         title,
		Aliases:       aliases,
		Snippet:       "", // No content initially
		UpdatedAt:     model.Now(),
		S3MarkdownKey: markdownKey,
		S3MetaKey:     metaKey,
	}

	// Create initial empty markdown file
	err = s.objects.Put(ctx, markdownKey, []byte(""), "text/markdown")
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to create markdown file: %w", err)
	}

	// Create metadata file
	metaData := map[string]interface{}{
		"title":      title,
		"aliases":    aliases,
		"created_at": note.UpdatedAt,
		"updated_at": note.UpdatedAt,
	}
	metaBytes, _ := json.Marshal(metaData)
	err = s.objects.Put(ctx, metaKey, metaBytes, "application/json")
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to create meta file: %w", err)
	}

	// Save to store
	stored, err := s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to save note: %w", err)
	}

	return stored, nil
}

// ListNotes returns one page of active notes. Archived notes are excluded by
// the store, so the filtering is no longer paid for in Go.
func (s *NotesService) ListNotes(ctx context.Context, userID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	return s.store.ListNotes(ctx, userID, opts)
}

// GetNote retrieves a specific note
func (s *NotesService) GetNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	return s.store.GetNote(ctx, userID, noteID)
}

// NoteDetail is a note index plus markdown body for the editor.
type NoteDetail struct {
	model.NoteIndex
	Body string `json:"body"`
}

// GetNoteDetail retrieves a note and its markdown body from object storage.
func (s *NotesService) GetNoteDetail(ctx context.Context, userID, noteID string) (NoteDetail, error) {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return NoteDetail{}, err
	}
	bodyBytes, err := s.objects.Get(ctx, note.S3MarkdownKey)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return NoteDetail{}, fmt.Errorf("failed to load note body: %w", err)
	}
	return NoteDetail{NoteIndex: note, Body: string(bodyBytes)}, nil
}

// UpdateNote updates a note with partial changes
func (s *NotesService) UpdateNote(ctx context.Context, userID, noteID string, updates NoteUpdates) (model.NoteIndex, error) {
	// Get existing note
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return model.NoteIndex{}, err
	}

	// Check if note is archived
	if !NoteIsActive(note) {
		return model.NoteIndex{}, ErrNoteArchived
	}

	// Apply updates
	if updates.Title != nil {
		title := sanitizeNoteTitle(*updates.Title)
		if title == "" {
			return model.NoteIndex{}, ErrEmptyNoteTitle
		}
		note.Title = title
	}
	if updates.Aliases != nil {
		note.Aliases = *updates.Aliases
	}

	// Update timestamp
	note.UpdatedAt = model.Now()

	// Handle body update
	if updates.Body != nil {
		// Write to markdown file
		err = s.objects.Put(ctx, note.S3MarkdownKey, []byte(*updates.Body), "text/markdown")
		if err != nil {
			return model.NoteIndex{}, fmt.Errorf("failed to update markdown: %w", err)
		}

		// Update snippet (first ~500 chars)
		note.Snippet = generateSnippet(*updates.Body)
	}

	// Update metadata
	metaData := map[string]interface{}{
		"title":      note.Title,
		"aliases":    note.Aliases,
		"updated_at": note.UpdatedAt,
	}
	metaBytes, _ := json.Marshal(metaData)
	err = s.objects.Put(ctx, note.S3MetaKey, metaBytes, "application/json")
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to update meta: %w", err)
	}

	// Save to store. A version conflict here means somebody else wrote the note
	// between the read and this write; the caller reconciles rather than one of
	// the two edits vanishing.
	stored, err := s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return stored, nil
}

// DeleteNote archives a note (soft delete)
func (s *NotesService) DeleteNote(ctx context.Context, userID, noteID string) error {
	_, err := s.ArchiveNote(ctx, userID, noteID)
	return err
}

// MatchNotes finds matching notes for a query (only searches active notes)
func (s *NotesService) MatchNotes(ctx context.Context, userID, query string) (MatchResult, error) {
	// Matching scores against every candidate, so it pages through the whole
	// active set rather than seeing whatever fitted in one page.
	notes, err := repository.DrainPages(ctx, maxMatchCandidates, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return s.store.ListNotes(ctx, userID, opts)
	})
	if err != nil {
		return MatchResult{}, err
	}

	// Rank all candidates first (following binding decision #1)
	candidates := match.Rank(query, notes, 0) // 0 means no limit, get all

	// Check for high confidence
	highConfidence := match.HighConfidence(candidates)

	// Limit response to reasonable number for UI
	if len(candidates) > 10 {
		candidates = candidates[:10]
	}

	result := MatchResult{
		Candidates: candidates,
	}

	// Set auto_select_id only for high confidence matches
	if highConfidence && len(candidates) > 0 {
		result.AutoSelectID = &candidates[0].NoteID
	}

	return result, nil
}

// generateSnippet creates a snippet from a note body.
//
// The cut is by rune, not byte. A byte slice cuts multi-byte runes in half and
// writes invalid UTF-8 into DynamoDB and into the routing prompt, which is what
// v1 did.
func generateSnippet(body string) string {
	const maxRunes = 500
	runes := []rune(body)
	if len(runes) <= maxRunes {
		return body
	}
	return string(runes[:maxRunes]) + "..."
}

// ArchiveNote archives a note (soft delete with retention period)
func (s *NotesService) ArchiveNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return model.NoteIndex{}, err
	}

	// If already archived, return as-is (idempotent)
	if !NoteIsActive(note) {
		return note, nil
	}

	// Set archive timestamps. PurgeAfterEpoch is the DynamoDB TTL attribute, so
	// expiry is the table's job rather than a synchronous sweep on every read.
	now := time.Now().UTC()
	purgeAt := now.Add(ArchiveRetention)
	note.DeletedAt = model.FormatTime(now)
	note.PurgeAfter = model.FormatTime(purgeAt)
	note.PurgeAfterEpoch = purgeAt.Unix()

	// Save updated note
	stored, err := s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return stored, nil
}

// RestoreNote restores an archived note to active status
func (s *NotesService) RestoreNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return model.NoteIndex{}, err
	}

	// If already active, return as-is (idempotent)
	if NoteIsActive(note) {
		return note, nil
	}

	// Clear archive fields, including the TTL, so the table stops counting down.
	note.DeletedAt = ""
	note.PurgeAfter = ""
	note.PurgeAfterEpoch = 0

	// Save updated note
	stored, err := s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return stored, nil
}

// ListArchivedNotes returns one page of archived notes that have not passed
// their purge deadline. Expiry itself belongs to DynamoDB TTL; the filter only
// keeps an item the table has not collected yet out of the UI.
func (s *NotesService) ListArchivedNotes(ctx context.Context, userID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	return s.store.ListArchivedNotes(ctx, userID, opts)
}

// PermanentlyDeleteNote permanently deletes an archived note and all its captures
func (s *NotesService) PermanentlyDeleteNote(ctx context.Context, userID, noteID string) error {
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return err
	}

	// Must be archived first
	if NoteIsActive(note) {
		return ErrNoteNotArchived
	}

	return s.hardDeleteNote(ctx, userID, noteID, note)
}

// hardDeleteNote removes a note's captures, its objects, and finally its index.
//
// It fails loudly. v1 logged and ignored every cascade failure and then deleted
// the index anyway, permanently orphaning audio that the UI reported as purged.
// Here the index survives any failure, so the note stays visible as archived and
// the delete can be retried.
//
// TODO(phase 4+): when DynamoDB TTL expires an archived note, a Streams handler
// performs this same S3 cascade. Until that exists, TTL removes the index row
// and leaves the objects; `chintanctl` reconciliation (§4.5) is the backstop.
func (s *NotesService) hardDeleteNote(ctx context.Context, userID, noteID string, note model.NoteIndex) error {
	// Every page, not just the first: a truncated list is how "delete forever"
	// leaves orphans behind.
	captures, err := repository.DrainPages(ctx, 0, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
	})
	if err != nil {
		return fmt.Errorf("%w: list captures: %w", ErrPurgeIncomplete, err)
	}

	for _, c := range captures {
		for _, key := range []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey, c.SegmentsKey, c.PeaksKey} {
			if err := s.deleteObject(ctx, key); err != nil {
				return fmt.Errorf("%w: capture %s object: %w", ErrPurgeIncomplete, c.ID, err)
			}
		}
		if err := s.store.DeleteCapture(ctx, userID, c.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: capture %s index: %w", ErrPurgeIncomplete, c.ID, err)
		}
	}

	if err := s.deleteObject(ctx, note.S3MarkdownKey); err != nil {
		return fmt.Errorf("%w: note body: %w", ErrPurgeIncomplete, err)
	}
	if err := s.deleteObject(ctx, note.S3MetaKey); err != nil {
		return fmt.Errorf("%w: note meta: %w", ErrPurgeIncomplete, err)
	}

	return s.store.DeleteNote(ctx, userID, noteID)
}

// deleteObject removes a key, treating "already gone" as success so a retried
// purge can make progress.
func (s *NotesService) deleteObject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	if err := s.objects.Delete(ctx, key); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	return nil
}
