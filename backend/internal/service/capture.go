package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
		if _, err := s.store.GetNote(ctx, userID, noteID); err != nil {
			return nil, "", fmt.Errorf("failed to get note: %w", err)
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
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.store.PutCapture(ctx, capture); err != nil {
		return nil, "", fmt.Errorf("failed to store capture: %w", err)
	}

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
	case model.StatusAppended, model.StatusFailed:
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

	if capture.CleanKey == "" {
		if err := s.cleanupCapture(ctx, userID, &capture); err != nil {
			return nil, err
		}
		if capture.Status == model.StatusFailed {
			return &capture, nil
		}
	}

	cleanBytes, err := s.objects.Get(ctx, capture.CleanKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get clean text: %w", err)
	}
	cleanedText := string(cleanBytes)

	if err := s.appendToNote(ctx, note.S3MarkdownKey, cleanedText); err != nil {
		return nil, fmt.Errorf("failed to append to note: %w", err)
	}

	if existing, err := s.objects.Get(ctx, note.S3MarkdownKey); err == nil {
		note.Snippet = generateSnippet(string(existing))
	} else {
		note.Snippet = generateSnippet(cleanedText)
	}
	note.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.PutNote(ctx, userID, note); err != nil {
		return nil, fmt.Errorf("failed to refresh note index: %w", err)
	}

	capture.Status = model.StatusAppended
	capture.Error = ""
	if err := s.store.PutCapture(ctx, capture); err != nil {
		return nil, fmt.Errorf("failed to update capture status: %w", err)
	}

	return &capture, nil
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

	switch {
	case noteID != "":
		if _, err := s.store.GetNote(ctx, userID, noteID); err != nil {
			return nil, fmt.Errorf("failed to get note: %w", err)
		}
		capture.NoteID = noteID
	case strings.TrimSpace(newNoteTitle) != "":
		if s.notes == nil {
			return nil, fmt.Errorf("note creation is unavailable")
		}
		note, err := s.notes.CreateNote(ctx, userID, strings.TrimSpace(newNoteTitle), nil)
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
	if err := s.store.PutCapture(ctx, capture); err != nil {
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
	if err := s.store.PutCapture(ctx, *capture); err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
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
		if _, err := s.store.GetNote(ctx, userID, decision.NoteID); err == nil {
			if decision.Confidence >= routeConfidenceThreshold {
				capture.NoteID = decision.NoteID
			} else {
				// Plausible but unsure: ask before writing into an existing note.
				capture.SuggestedNoteID = decision.NoteID
				capture.Status = model.StatusNeedsTarget
			}
			if err := s.store.PutCapture(ctx, *capture); err != nil {
				return fmt.Errorf("failed to update capture: %w", err)
			}
			return nil
		}
	}

	title := strings.TrimSpace(decision.Title)
	if title == "" {
		title = fallbackNoteTitle()
	}
	if s.notes == nil {
		capture.SuggestedTitle = title
		capture.Status = model.StatusNeedsTarget
		if err := s.store.PutCapture(ctx, *capture); err != nil {
			return fmt.Errorf("failed to update capture: %w", err)
		}
		return nil
	}

	note, err := s.notes.CreateNote(ctx, userID, title, nil)
	if err != nil {
		return fmt.Errorf("failed to create note for capture: %w", err)
	}
	capture.NoteID = note.ID
	if err := s.store.PutCapture(ctx, *capture); err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
	return nil
}

func (s *CaptureService) decideTarget(ctx context.Context, userID, transcript string) (provider.RouteDecision, error) {
	if s.router == nil {
		return provider.RouteDecision{}, fmt.Errorf("routing is not configured")
	}

	notes, err := s.store.ListNotes(ctx, userID)
	if err != nil {
		return provider.RouteDecision{}, fmt.Errorf("failed to list notes: %w", err)
	}

	// Most recently touched notes are the likeliest destinations.
	sort.Slice(notes, func(i, j int) bool { return notes[i].UpdatedAt > notes[j].UpdatedAt })
	if len(notes) > maxRouteCandidates {
		notes = notes[:maxRouteCandidates]
	}

	candidates := make([]routing.Candidate, 0, len(notes))
	for _, n := range notes {
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
	if err := s.store.PutCapture(ctx, *capture); err != nil {
		return fmt.Errorf("failed to update capture: %w", err)
	}
	return nil
}

func (s *CaptureService) markFailed(ctx context.Context, capture *model.CaptureIndex, cause error) {
	capture.Status = model.StatusFailed
	capture.Error = cause.Error()
	_ = s.store.PutCapture(ctx, *capture)
}

func fallbackNoteTitle() string {
	return "Voice note " + time.Now().UTC().Format("2006-01-02 15:04")
}

func (s *CaptureService) appendToNote(ctx context.Context, noteKey, text string) error {
	existingContent, err := s.objects.Get(ctx, noteKey)
	if err != nil && err != repository.ErrNotFound {
		return fmt.Errorf("failed to get existing note: %w", err)
	}

	var newContent string
	if len(existingContent) > 0 {
		newContent = string(existingContent) + "\n\n" + text
	} else {
		newContent = text
	}

	if err := s.objects.Put(ctx, noteKey, []byte(newContent), "text/markdown"); err != nil {
		return fmt.Errorf("failed to update note: %w", err)
	}
	return nil
}

// GetCapture returns a capture by ID.
func (s *CaptureService) GetCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, err
	}
	return &capture, nil
}

// ListCapturesForNote returns captures for a note, newest first.
func (s *CaptureService) ListCapturesForNote(ctx context.Context, userID, noteID string) ([]model.CaptureIndex, error) {
	if _, err := s.store.GetNote(ctx, userID, noteID); err != nil {
		return nil, err
	}
	return s.store.ListCapturesByNote(ctx, userID, noteID)
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
