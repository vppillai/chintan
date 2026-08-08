package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/match"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const ArchiveRetention = 30 * 24 * time.Hour

// maxNoteTitleLen bounds a stored title. It matches the OpenAPI document's
// maxLength for a note title, so a title the API accepts is a title that is
// stored whole. Two different limits for one field means a request the handler
// validated is quietly truncated by the service, which is data loss with a
// receipt.
//
// The routing prompt does not depend on this number: it bounds every rendered
// candidate field itself (routing.maxFieldLen), so a longer stored title cannot
// grow the prompt.
const maxNoteTitleLen = 200

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

// maxTagLen bounds one tag. Tags are rendered into list filters and into the
// routing prompt, so an unbounded tag is a stored cost amplifier.
const maxTagLen = 40

// normalizeTags folds tags to a canonical form: trimmed, lowercased, collapsed
// whitespace, deduplicated, ordered. Without this "Roof", "roof " and "roof"
// are three tags in the index and one tag to the person who said them.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		t := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if t == "" {
			continue
		}
		if runes := []rune(t); len(runes) > maxTagLen {
			t = strings.TrimSpace(string(runes[:maxTagLen]))
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// NotesService handles note operations
type NotesService struct {
	store   repository.Store
	objects repository.Objects
}

// NoteUpdates represents partial updates to a note.
//
// ExpectedVersion is the version the client read. It is compared before
// anything is written, so a voice append landing while the editor is open
// surfaces as a conflict the client reconciles rather than one of the two
// writes vanishing. A nil ExpectedVersion skips the check and is for in-process
// callers only — every HTTP caller supplies one.
type NoteUpdates struct {
	Title           *string
	Aliases         *[]string
	Tags            *[]string
	Body            *string
	Verbatim        *bool
	ExpectedVersion *int64
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

// CreateNote creates a new note.
//
// The signature is what the worker's NoteCreator expects; CreateNoteWithTags is
// the fuller form the API uses.
func (s *NotesService) CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error) {
	return s.CreateNoteWithTags(ctx, userID, title, aliases, nil)
}

// CreateNoteWithTags creates a note carrying tags.
func (s *NotesService) CreateNoteWithTags(ctx context.Context, userID, title string, aliases, tags []string) (model.NoteIndex, error) {
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

	now := model.Now()
	note := model.NoteIndex{
		ID:            noteID,
		Title:         title,
		Aliases:       aliases,
		Tags:          normalizeTags(tags),
		Snippet:       "", // No content initially
		CreatedAt:     now,
		UpdatedAt:     now,
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
		"tags":       note.Tags,
		"created_at": note.CreatedAt,
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

	// The version the client read is checked before any object is written, so a
	// losing writer does not leave a rewritten body behind with the index intact.
	if updates.ExpectedVersion != nil && *updates.ExpectedVersion != note.Version {
		return note, repository.ErrVersionConflict
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
	if updates.Tags != nil {
		note.Tags = normalizeTags(*updates.Tags)
	}
	if updates.Verbatim != nil {
		note.Verbatim = *updates.Verbatim
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
		"tags":       note.Tags,
		"verbatim":   note.Verbatim,
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
