package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/match"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const ArchiveRetention = 30 * 24 * time.Hour

var (
	ErrNoteArchived    = errors.New("note is archived")
	ErrNoteNotArchived = errors.New("note is not archived")
)

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
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
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
	err = s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("failed to save note: %w", err)
	}

	return note, nil
}

// ListNotes lists active notes for a user (excludes archived notes)
func (s *NotesService) ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	all, err := s.store.ListNotes(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]model.NoteIndex, 0, len(all))
	for _, n := range all {
		if NoteIsActive(n) {
			out = append(out, n)
		}
	}
	return out, nil
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
	if err != nil && err != repository.ErrNotFound {
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
		note.Title = *updates.Title
	}
	if updates.Aliases != nil {
		note.Aliases = *updates.Aliases
	}

	// Update timestamp
	note.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)

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

	// Save to store
	err = s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return note, nil
}

// DeleteNote archives a note (soft delete)
func (s *NotesService) DeleteNote(ctx context.Context, userID, noteID string) error {
	_, err := s.ArchiveNote(ctx, userID, noteID)
	return err
}

// MatchNotes finds matching notes for a query (only searches active notes)
func (s *NotesService) MatchNotes(ctx context.Context, userID, query string) (MatchResult, error) {
	// Get active notes only
	notes, err := s.ListNotes(ctx, userID)
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

// generateSnippet creates a snippet from note body (first ~500 chars)
func generateSnippet(body string) string {
	const maxLength = 500
	if len(body) <= maxLength {
		return body
	}
	return body[:maxLength] + "..."
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

	// Set archive timestamps
	now := time.Now().UTC()
	note.DeletedAt = now.Format(time.RFC3339Nano)
	note.PurgeAfter = now.Add(ArchiveRetention).Format(time.RFC3339Nano)

	// Save updated note
	err = s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return note, nil
}

// RestoreNote restores an archived note to active status
func (s *NotesService) RestoreNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	note, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return model.NoteIndex{}, err
	}

	// If already active, return as-is (idempotent)
	if NoteIsActive(note) {
		return note, nil
	}

	// Clear archive fields
	note.DeletedAt = ""
	note.PurgeAfter = ""

	// Save updated note
	err = s.store.PutNote(ctx, userID, note)
	if err != nil {
		return model.NoteIndex{}, err
	}

	return note, nil
}

// ListArchivedNotes lists archived notes for a user (excludes expired ones)
func (s *NotesService) ListArchivedNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	all, err := s.store.ListNotes(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := make([]model.NoteIndex, 0)
	now := time.Now().UTC()

	for _, n := range all {
		if !NoteIsActive(n) {
			// Check if not yet expired
			if n.PurgeAfter != "" {
				purgeTime, err := time.Parse(time.RFC3339Nano, n.PurgeAfter)
				if err == nil && now.Before(purgeTime) {
					out = append(out, n)
				}
			}
		}
	}
	return out, nil
}

// PermanentlyDeleteNote permanently deletes an archived note and all its captures
func (s *NotesService) PermanentlyDeleteNote(ctx context.Context, userID, noteID string) error {
	_ = s.purgeExpired(ctx, userID)
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

// hardDeleteNote performs the actual deletion (used by PermanentlyDeleteNote and purgeExpired)
func (s *NotesService) hardDeleteNote(ctx context.Context, userID, noteID string, note model.NoteIndex) error {
	captures, err := s.store.ListCapturesByNote(ctx, userID, noteID)
	if err != nil {
		log.Printf("hardDeleteNote: ListCapturesByNote failed userID=%s noteID=%s: %v", userID, noteID, err)
	} else {
		for _, c := range captures {
			for _, key := range []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey} {
				if key != "" {
					if err := s.objects.Delete(ctx, key); err != nil {
						log.Printf("hardDeleteNote: objects.Delete failed userID=%s noteID=%s captureID=%s key=%s: %v", userID, noteID, c.ID, key, err)
					}
				}
			}
			if err := s.store.DeleteCapture(ctx, userID, c.ID); err != nil {
				log.Printf("hardDeleteNote: DeleteCapture failed userID=%s noteID=%s captureID=%s: %v", userID, noteID, c.ID, err)
			}
		}
	}

	if err := s.objects.Delete(ctx, note.S3MarkdownKey); err != nil {
		log.Printf("hardDeleteNote: objects.Delete failed userID=%s noteID=%s key=%s: %v", userID, noteID, note.S3MarkdownKey, err)
	}
	if err := s.objects.Delete(ctx, note.S3MetaKey); err != nil {
		log.Printf("hardDeleteNote: objects.Delete failed userID=%s noteID=%s key=%s: %v", userID, noteID, note.S3MetaKey, err)
	}

	return s.store.DeleteNote(ctx, userID, noteID)
}

// purgeExpired removes notes that have passed their purge deadline
func (s *NotesService) purgeExpired(ctx context.Context, userID string) error {
	all, err := s.store.ListNotes(ctx, userID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, n := range all {
		if !NoteIsActive(n) && n.PurgeAfter != "" {
			purgeTime, err := time.Parse(time.RFC3339Nano, n.PurgeAfter)
			if err == nil && !now.Before(purgeTime) {
				if err := s.hardDeleteNote(ctx, userID, n.ID, n); err != nil {
					log.Printf("purgeExpired: hardDeleteNote failed userID=%s noteID=%s: %v", userID, n.ID, err)
				}
			}
		}
	}
	return nil
}
