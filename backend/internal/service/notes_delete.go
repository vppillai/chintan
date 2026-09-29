package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const ArchiveRetention = 30 * 24 * time.Hour

// ErrPurgeIncomplete means part of a permanent delete failed. The note index
// is deliberately left in place so the delete can be retried, because
// reporting "purged" while audio survives in S3 is worse than reporting a
// failure.
var ErrPurgeIncomplete = errors.New("note purge incomplete")

// DeleteNote archives a note (soft delete)
func (s *NotesService) DeleteNote(ctx context.Context, userID, noteID string) error {
	_, err := s.ArchiveNote(ctx, userID, noteID)
	return err
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

	// Set archive timestamps. PurgeAfterEpoch is the deadline the archived list
	// filters on and the weekly expiry sweep (internal/purge) collects on; the
	// store derives the DynamoDB TTL backstop from it. Nothing sweeps on read.
	now := time.Now().UTC()
	purgeAt := now.Add(ArchiveRetention)
	note.DeletedAt = model.FormatTime(now)
	note.PurgeAfter = model.FormatTime(purgeAt)
	note.PurgeAfterEpoch = purgeAt.Unix()
	// The archive is never pinned, and a restore comes back unpinned: the
	// pin was a place on Home, and the note has left Home.
	note.PinnedAt, note.PinRank = "", 0

	return s.putCarryingStamp(ctx, userID, note)
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

	return s.putCarryingStamp(ctx, userID, note)
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
// It fails loudly. Logging a cascade failure and deleting the index anyway
// permanently orphans audio the UI has reported as purged. Here the index
// survives any failure, so the note stays visible as archived and the delete
// can be retried.
func (s *NotesService) hardDeleteNote(ctx context.Context, userID, noteID string, note model.NoteIndex) error {
	if err := s.PurgeNoteArtifacts(ctx, userID, noteID, note); err != nil {
		return err
	}
	return s.store.DeleteNote(ctx, userID, noteID)
}

// DiscardNote removes a note nothing references — one CreateNote made for a
// move whose write into it never landed. The body and the metadata go first
// and the index row last, as in hardDeleteNote, so a failure leaves the note
// visible and deletable rather than as objects nobody can reach.
//
// It deliberately runs none of hardDeleteNote's capture cascade. That cascade
// deletes every capture row filed against the note together with its audio
// and transcripts, and the caller cannot prove that no row points here: a
// re-point that reported a fault may still have landed. A note left behind by
// mistake costs two empty objects; a cascade that guessed wrong costs a
// recording.
func (s *NotesService) DiscardNote(ctx context.Context, userID string, note model.NoteIndex) error {
	if err := s.deleteObject(ctx, note.S3MarkdownKey); err != nil {
		return fmt.Errorf("failed to delete the note body: %w", err)
	}
	if err := s.deleteObject(ctx, note.S3MetaKey); err != nil {
		return fmt.Errorf("failed to delete the note meta: %w", err)
	}
	return s.store.DeleteNote(ctx, userID, note.ID)
}

// PurgeNoteArtifacts unlinks everything a note owns apart from its own index
// row: every capture filed against it, every S3 object those captures name, and
// the note's own body and metadata.
//
// It is separate from hardDeleteNote because the same cascade runs from two
// places. A user asking to delete forever arrives through hardDeleteNote, which
// removes the row last so a failed cascade leaves the note visible and
// retryable. The weekly sweep in internal/purge finds notes past their
// purge_after_epoch, runs this, and then deletes the row itself; DynamoDB TTL
// is only the backstop fourteen days behind it (repository.ttlGraceSeconds).
// Before the sweep existed, TTL removed the index row and left the audio, raw
// transcript, routed transcript, cleaned text, segments and peaks in the
// bucket, billed and unreachable, with `chintanctl reconcile` as the only way
// to find them.
//
// Every failure is returned rather than logged, so a caller that must not
// declare a purge complete can tell that it is not.
func (s *NotesService) PurgeNoteArtifacts(ctx context.Context, userID, noteID string, note model.NoteIndex) error {
	// Every page, not just the first: a truncated list is how "delete forever"
	// leaves orphans behind.
	captures, err := repository.DrainPages(ctx, 0, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
	})
	if err != nil {
		return fmt.Errorf("%w: list captures: %w", ErrPurgeIncomplete, err)
	}

	// And the captures the index cannot see. A row written before the index
	// keys were promoted (August 2026) is not in GSI1 at all, so the query
	// above is complete only for rows the current code wrote. In production
	// this is how "delete forever" removed every note and left thirteen filed
	// captures pointing at them, each still answering GET /v1/captures as a
	// receipt. The base-table read is the honest fix rather than promoting
	// the keys on read: it finds the rows now, for this purge, instead of
	// repairing the index for a later one that may never come — and a purge
	// is rare enough to afford one partition read per note.
	seen := make(map[string]bool, len(captures))
	for _, c := range captures {
		seen[c.ID] = true
	}
	unindexed, err := s.store.ListUnindexedCaptures(ctx, userID)
	if err != nil {
		return fmt.Errorf("%w: list unindexed captures: %w", ErrPurgeIncomplete, err)
	}
	for _, c := range unindexed {
		if c.NoteID == noteID && !seen[c.ID] {
			captures = append(captures, c)
		}
	}

	for _, c := range captures {
		// Every object the capture may own, including a peaks key the row no
		// longer records; see captureObjectKeys.
		for _, key := range captureObjectKeys(userID, c) {
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

	return nil
}

// deleteObject removes a key, treating "already gone" as success so a retried
// purge can make progress.
func (s *NotesService) deleteObject(ctx context.Context, key string) error {
	return deleteObjectIfPresent(ctx, s.objects, key)
}
