package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/routing"
)

// routeConfidenceThreshold is how sure the router must be before appending to an
// existing note without asking. Below it, the user confirms first.
const routeConfidenceThreshold = 0.75

// maxRouteCandidates bounds the note list handed to the router.
const maxRouteCandidates = 50

// maxRouteCandidatePool bounds how many notes are paged through before the most
// recent maxRouteCandidates are chosen.
const maxRouteCandidatePool = 500

// maxAppendAttempts bounds the ETag-conditional retry when a note body is being
// written concurrently.
const maxAppendAttempts = 5

// maxIndexRefreshAttempts bounds the optimistic-concurrency retry on the note
// index after an append.
const maxIndexRefreshAttempts = 5

// NoteCreator creates the destination note for a capture that has none.
type NoteCreator interface {
	CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error)
}

// CaptureService handles audio capture processing pipeline
type CaptureService struct {
	store   repository.Store
	objects repository.Objects
	stt     provider.STT
	llm     provider.LLM
	router  provider.Router
	notes   NoteCreator
}

// NewCaptureService creates a new capture service
func NewCaptureService(store repository.Store, objects repository.Objects, stt provider.STT, llm provider.LLM) *CaptureService {
	return &CaptureService{
		store:   store,
		objects: objects,
		stt:     stt,
		llm:     llm,
	}
}

// WithRouting enables voice-directed routing, so a capture can be created with no
// target note and have its destination decided from what the speaker said.
func (s *CaptureService) WithRouting(router provider.Router, notes NoteCreator) *CaptureService {
	s.router = router
	s.notes = notes
	return s
}

// CreateCapture creates a new capture and returns upload URL.
// An empty noteID defers the destination to routing at completion time.
func (s *CaptureService) CreateCapture(ctx context.Context, userID, noteID, contentType string) (*model.CaptureIndex, string, error) {
	if noteID != "" {
		note, err := s.store.GetNote(ctx, userID, noteID)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get note: %w", err)
		}
		if !NoteIsActive(note) {
			return nil, "", ErrNoteArchived
		}
	}

	settings, err := s.store.GetSettings(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get settings: %w", err)
	}

	captureIDBytes := make([]byte, 8)
	if _, err := rand.Read(captureIDBytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate capture ID: %w", err)
	}
	captureID := "c_" + hex.EncodeToString(captureIDBytes)

	ext := extensionForContentType(contentType)
	audioKey, err := keys.CaptureAudio(userID, captureID, ext)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate audio key: %w", err)
	}

	capture := model.CaptureIndex{
		ID:        captureID,
		UserID:    userID,
		NoteID:    noteID,
		Status:    model.StatusUploaded,
		Mode:      settings.CleanupMode,
		AudioKey:  audioKey,
		CreatedAt: model.Now(),
	}

	stored, err := s.store.PutCapture(ctx, capture)
	if err != nil {
		return nil, "", fmt.Errorf("failed to store capture: %w", err)
	}
	capture = stored

	uploadURL, err := s.objects.PresignPut(ctx, audioKey, contentType, 15*time.Minute)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate upload URL: %w", err)
	}

	return &capture, uploadURL, nil
}

// CompleteCapture runs the capture processing pipeline.
// Already-terminal captures (appended/failed) are returned as-is (idempotent).
func (s *CaptureService) CompleteCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to get capture: %w", err)
	}

	switch capture.Status {
	case model.StatusAppended, model.StatusFailed, model.StatusNoContent:
		return &capture, nil
	}

	if capture.RawKey == "" {
		if err := s.transcribeCapture(ctx, userID, &capture); err != nil {
			return nil, err
		}
		if capture.Status == model.StatusFailed {
			return &capture, nil
		}
	}

	if capture.NoteID == "" {
		// Nothing is written until the user picks a destination.
		if capture.Status == model.StatusNeedsTarget {
			return &capture, nil
		}
		if err := s.routeCapture(ctx, userID, &capture); err != nil {
			return nil, err
		}
		if capture.NoteID == "" {
			return &capture, nil
		}
	}

	note, err := s.store.GetNote(ctx, userID, capture.NoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to get note: %w", err)
	}
	if !NoteIsActive(note) {
		return nil, ErrNoteArchived
	}

	if capture.CleanKey == "" {
		if err := s.cleanupCapture(ctx, userID, &capture); err != nil {
			return nil, err
		}
		switch capture.Status {
		case model.StatusFailed, model.StatusNoContent:
			return &capture, nil
		}
	}

	cleanBytes, err := s.objects.Get(ctx, capture.CleanKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get clean text: %w", err)
	}
	cleanedText := string(cleanBytes)

	// The append is the one step that must happen exactly once. v1 appended,
	// then updated the index, then set the status, with nothing tying the three
	// together: a failure after the append left the capture in `cleaned`, and
	// the retry path re-appended the same text. The token is derived from the
	// capture and its cleaned artefact, so every attempt at the same work
	// computes the same value and can recognise its own earlier claim.
	token := appendToken(capture.ID, capture.CleanKey)

	claimed, current, err := s.store.ClaimCaptureAppend(ctx, userID, capture.ID, token)
	if err != nil {
		return nil, fmt.Errorf("failed to claim capture append: %w", err)
	}
	if !claimed {
		// Somebody owns this append: either an earlier attempt that finished, or
		// one running right now. Either way this attempt must not write the text
		// a second time.
		return &current, nil
	}
	capture = current

	if err := s.appendToNote(ctx, note.S3MarkdownKey, cleanedText); err != nil {
		// Hand the claim back so a transient object-store failure does not park
		// the capture until the claim lease expires.
		s.releaseAppendClaim(ctx, userID, &capture)
		return nil, fmt.Errorf("failed to append to note: %w", err)
	}

	if err := s.refreshNoteIndex(ctx, userID, note.ID, cleanedText); err != nil {
		return nil, fmt.Errorf("failed to refresh note index: %w", err)
	}

	appended, err := s.store.CompleteCaptureAppend(ctx, userID, capture.ID, token)
	if err != nil {
		return nil, fmt.Errorf("failed to update capture status: %w", err)
	}

	return &appended, nil
}

// appendToken is deterministic so a retry of the same work recognises its own
// claim rather than treating it as somebody else's.
func appendToken(captureID, cleanKey string) string {
	sum := sha256.Sum256([]byte(captureID + "\x00" + cleanKey))
	return hex.EncodeToString(sum[:16])
}

func (s *CaptureService) releaseAppendClaim(ctx context.Context, userID string, capture *model.CaptureIndex) {
	released := *capture
	released.AppendToken = ""
	released.AppendClaimedAt = 0
	if updated, err := s.store.PutCapture(ctx, released); err == nil {
		*capture = updated
	}
}

// refreshNoteIndex re-derives the snippet and touch time from the note body that
// is now in object storage. The body is authoritative, so a version conflict is
// resolved by re-reading rather than by overwriting whoever won.
func (s *CaptureService) refreshNoteIndex(ctx context.Context, userID, noteID, fallbackBody string) error {
	var lastErr error
	for attempt := 0; attempt < maxIndexRefreshAttempts; attempt++ {
		note, err := s.store.GetNote(ctx, userID, noteID)
		if err != nil {
			return err
		}
		if existing, err := s.objects.Get(ctx, note.S3MarkdownKey); err == nil {
			note.Snippet = generateSnippet(string(existing))
		} else {
			note.Snippet = generateSnippet(fallbackBody)
		}
		note.UpdatedAt = model.Now()

		if _, err := s.store.PutNote(ctx, userID, note); err == nil {
			return nil
		} else if !errors.Is(err, repository.ErrVersionConflict) {
			return err
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// SetCaptureTarget records a user-chosen destination and resumes the pipeline.
// Exactly one of noteID or newNoteTitle must be set.
func (s *CaptureService) SetCaptureTarget(ctx context.Context, userID, captureID, noteID, newNoteTitle string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to get capture: %w", err)
	}
	if capture.NoteID != "" {
		return nil, fmt.Errorf("capture already targets a note")
	}

	newNoteTitle = sanitizeNoteTitle(newNoteTitle)

	switch {
	case noteID != "":
		note, err := s.store.GetNote(ctx, userID, noteID)
		if err != nil {
			return nil, fmt.Errorf("failed to get note: %w", err)
		}
		if !NoteIsActive(note) {
			return nil, ErrNoteArchived
		}
		capture.NoteID = noteID
	case newNoteTitle != "":
		if s.notes == nil {
			return nil, fmt.Errorf("note creation is unavailable")
		}
		note, err := s.notes.CreateNote(ctx, userID, newNoteTitle, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create note: %w", err)
		}
		capture.NoteID = note.ID
	default:
		return nil, fmt.Errorf("note_id or new_note_title is required")
	}

	capture.Status = model.StatusTranscribed
	capture.SuggestedNoteID = ""
	capture.SuggestedTitle = ""
	capture.Error = ""
	if _, err := s.store.PutCapture(ctx, capture); err != nil {
		return nil, fmt.Errorf("failed to update capture: %w", err)
	}

	return s.CompleteCapture(ctx, userID, captureID)
}

func (s *CaptureService) transcribeCapture(ctx context.Context, userID string, capture *model.CaptureIndex) error {
	audioData, err := s.objects.Get(ctx, capture.AudioKey)
	if err != nil {
		return fmt.Errorf("failed to get audio: %w", err)
	}

	rawText, err := s.stt.Transcribe(ctx, audioData, contentTypeForAudioKey(capture.AudioKey))
	if err != nil {
		s.markFailed(ctx, capture, err)
		return nil
	}

	rawKey, err := keys.CaptureRaw(userID, capture.ID)
	if err != nil {
		return fmt.Errorf("failed to generate raw key: %w", err)
	}
	if err := s.objects.Put(ctx, rawKey, []byte(rawText), "text/plain"); err != nil {
		return fmt.Errorf("failed to store raw text: %w", err)
	}

	capture.RawKey = rawKey
	capture.Status = model.StatusTranscribed
	capture.Error = ""
	updated, err := s.store.PutCapture(ctx, *capture)
	if err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
	*capture = updated
	return nil
}

// routeCapture decides the destination note from what the speaker said. It either
// sets NoteID, or leaves it empty with StatusNeedsTarget for the user to confirm.
func (s *CaptureService) routeCapture(ctx context.Context, userID string, capture *model.CaptureIndex) error {
	rawBytes, err := s.objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("failed to get raw text: %w", err)
	}
	transcript := string(rawBytes)

	decision, err := s.decideTarget(ctx, userID, transcript)
	if err != nil {
		// Routing is a convenience; a recording is never lost because of it.
		decision = provider.RouteDecision{Action: provider.RouteNew, Content: transcript}
	}

	// Persist the transcript minus any spoken instruction, so cleanup and any
	// later retry work from the words the user meant to keep.
	routedKey, err := keys.CaptureRouted(userID, capture.ID)
	if err != nil {
		return fmt.Errorf("failed to generate routed key: %w", err)
	}
	if err := s.objects.Put(ctx, routedKey, []byte(decision.Content), "text/plain"); err != nil {
		return fmt.Errorf("failed to store routed text: %w", err)
	}
	capture.RoutedKey = routedKey
	capture.RouteConfidence = decision.Confidence

	if decision.Action == provider.RouteAppend {
		if note, err := s.store.GetNote(ctx, userID, decision.NoteID); err == nil && NoteIsActive(note) {
			if decision.Confidence >= routeConfidenceThreshold {
				capture.NoteID = decision.NoteID
			} else {
				// Plausible but unsure: ask before writing into an existing note.
				capture.SuggestedNoteID = decision.NoteID
				capture.Status = model.StatusNeedsTarget
			}
			updated, err := s.store.PutCapture(ctx, *capture)
			if err != nil {
				return fmt.Errorf("failed to update capture: %w", err)
			}
			*capture = updated
			return nil
		}
	}

	title := sanitizeNoteTitle(decision.Title)
	if title == "" {
		title = fallbackNoteTitle()
	}
	if s.notes == nil {
		capture.SuggestedTitle = title
		capture.Status = model.StatusNeedsTarget
		updated, err := s.store.PutCapture(ctx, *capture)
		if err != nil {
			return fmt.Errorf("failed to update capture: %w", err)
		}
		*capture = updated
		return nil
	}

	note, err := s.notes.CreateNote(ctx, userID, title, nil)
	if err != nil {
		return fmt.Errorf("failed to create note for capture: %w", err)
	}
	capture.NoteID = note.ID
	updated, err := s.store.PutCapture(ctx, *capture)
	if err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
	*capture = updated
	return nil
}

func (s *CaptureService) decideTarget(ctx context.Context, userID, transcript string) (provider.RouteDecision, error) {
	if s.router == nil {
		return provider.RouteDecision{}, fmt.Errorf("routing is not configured")
	}

	// Notes are paged, and the router only ever sees the most recent
	// maxRouteCandidates of them, so the pool has to be drained before it can be
	// ordered.
	active, err := repository.DrainPages(ctx, maxRouteCandidatePool, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return s.store.ListNotes(ctx, userID, opts)
	})
	if err != nil {
		return provider.RouteDecision{}, fmt.Errorf("failed to list notes: %w", err)
	}

	// Most recently touched notes are the likeliest destinations. Compare parsed
	// instants: v1 compared RFC3339Nano strings, and Go trims trailing
	// fractional zeros, so "…:00Z" sorted above "…:00.1Z" because 'Z' > '.' and
	// the router was handed the wrong fifty notes.
	sort.SliceStable(active, func(i, j int) bool {
		return noteTouchedAt(active[i]).After(noteTouchedAt(active[j]))
	})
	if len(active) > maxRouteCandidates {
		active = active[:maxRouteCandidates]
	}

	candidates := make([]routing.Candidate, 0, len(active))
	for _, n := range active {
		candidates = append(candidates, routing.Candidate{
			NoteID:  n.ID,
			Title:   n.Title,
			Aliases: n.Aliases,
		})
	}

	return s.router.Route(ctx, transcript, candidates)
}

func (s *CaptureService) cleanupCapture(ctx context.Context, userID string, capture *model.CaptureIndex) error {
	sourceKey := capture.RoutedKey
	if sourceKey == "" {
		sourceKey = capture.RawKey
	}

	sourceBytes, err := s.objects.Get(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("failed to get raw text: %w", err)
	}

	if strings.TrimSpace(string(sourceBytes)) == "" {
		// The speaker only told the app what to do, so the note they asked for exists
		// and there is nothing to clean or append.
		capture.Status = model.StatusNoContent
		capture.Error = ""
		updated, err := s.store.PutCapture(ctx, *capture)
		if err != nil {
			return fmt.Errorf("failed to update capture: %w", err)
		}
		*capture = updated
		return nil
	}

	cleanedText, err := s.llm.Cleanup(ctx, capture.Mode, string(sourceBytes))
	if err != nil {
		s.markFailed(ctx, capture, err)
		return nil
	}

	cleanKey, err := keys.CaptureClean(userID, capture.ID)
	if err != nil {
		return fmt.Errorf("failed to generate clean key: %w", err)
	}
	if err := s.objects.Put(ctx, cleanKey, []byte(cleanedText), "text/plain"); err != nil {
		return fmt.Errorf("failed to store clean text: %w", err)
	}

	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned
	capture.Error = ""
	updated, err := s.store.PutCapture(ctx, *capture)
	if err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
	*capture = updated
	return nil
}

func (s *CaptureService) markFailed(ctx context.Context, capture *model.CaptureIndex, cause error) {
	capture.Status = model.StatusFailed
	capture.Error = cause.Error()
	if updated, err := s.store.PutCapture(ctx, *capture); err == nil {
		*capture = updated
	}
}

func fallbackNoteTitle() string {
	return "Voice note " + time.Now().UTC().Format("2006-01-02 15:04")
}

// noteTouchedAt parses a note's update time, tolerating the RFC3339 and
// RFC3339Nano values written before the fixed-width layout existed.
func noteTouchedAt(n model.NoteIndex) time.Time {
	t, err := model.ParseTime(n.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// appendToNote adds text to the end of a note body under a conditional write.
//
// v1 did read-concat-write with no concurrency control, so a voice append
// landing while the editor was saving silently discarded one of the two. Here
// the write carries the ETag that was read; a lost race re-reads and retries so
// both edits survive.
func (s *CaptureService) appendToNote(ctx context.Context, noteKey, text string) error {
	var lastErr error
	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		existingContent, etag, err := s.objects.GetWithETag(ctx, noteKey)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			existingContent, etag = nil, ""
		case err != nil:
			return fmt.Errorf("failed to get existing note: %w", err)
		}

		newContent := text
		if len(existingContent) > 0 {
			newContent = string(existingContent) + "\n\n" + text
		}

		err = s.objects.PutIfMatch(ctx, noteKey, []byte(newContent), "text/markdown", etag)
		if err == nil {
			return nil
		}
		if !errors.Is(err, repository.ErrPreconditionFailed) {
			return fmt.Errorf("failed to update note: %w", err)
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	return fmt.Errorf("failed to update note after %d attempts: %w", maxAppendAttempts, lastErr)
}

// GetCapture returns a capture by ID.
func (s *CaptureService) GetCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, err
	}
	return &capture, nil
}

// ListCapturesForNote returns one page of a note's captures, newest first.
func (s *CaptureService) ListCapturesForNote(ctx context.Context, userID, noteID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	if _, err := s.store.GetNote(ctx, userID, noteID); err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
}

// GetDownloadURL returns a presigned download URL for capture artifacts
func (s *CaptureService) GetDownloadURL(ctx context.Context, userID, captureID, kind string) (string, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return "", fmt.Errorf("failed to get capture: %w", err)
	}

	var key string
	switch kind {
	case "audio":
		key = capture.AudioKey
	case "raw":
		key = capture.RawKey
	case "clean":
		key = capture.CleanKey
	default:
		return "", fmt.Errorf("invalid download kind: %s", kind)
	}

	if key == "" {
		return "", fmt.Errorf("no %s file available for capture", kind)
	}

	url, err := s.objects.PresignGet(ctx, key, 15*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to generate download URL: %w", err)
	}
	return url, nil
}

func extensionForContentType(contentType string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch base {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/mp3", "audio/mpeg":
		return "mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/ogg", "audio/ogg;codecs=opus":
		return "ogg"
	case "audio/webm", "audio/webm;codecs=opus":
		return "webm"
	default:
		if strings.Contains(base, "webm") {
			return "webm"
		}
		if strings.Contains(base, "ogg") {
			return "ogg"
		}
		return "bin"
	}
}

func contentTypeForAudioKey(audioKey string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(audioKey), "."))
	switch ext {
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "ogg":
		return "audio/ogg"
	case "webm":
		return "audio/webm"
	case "wav":
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}
