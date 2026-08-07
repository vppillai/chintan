package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// CaptureService handles audio capture processing pipeline
type CaptureService struct {
	store   repository.Store
	objects repository.Objects
	stt     provider.STT
	llm     provider.LLM
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

// CreateCapture creates a new capture and returns upload URL
func (s *CaptureService) CreateCapture(ctx context.Context, userID, noteID, contentType string) (*model.CaptureIndex, string, error) {
	// Verify note exists
	_, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get note: %w", err)
	}

	// Get user settings for cleanup mode
	settings, err := s.store.GetSettings(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get settings: %w", err)
	}

	// Generate capture ID - use c_ prefix with hex
	captureIDBytes := make([]byte, 8)
	if _, err := rand.Read(captureIDBytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate capture ID: %w", err)
	}
	captureID := "c_" + hex.EncodeToString(captureIDBytes)

	// Determine file extension from content type
	ext := "bin"
	switch contentType {
	case "audio/wav":
		ext = "wav"
	case "audio/mp3", "audio/mpeg":
		ext = "mp3"
	case "audio/mp4", "audio/m4a":
		ext = "m4a"
	case "audio/ogg":
		ext = "ogg"
	}

	// Generate keys
	audioKey, err := keys.CaptureAudio(userID, captureID, ext)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate audio key: %w", err)
	}

	// Create capture index
	capture := model.CaptureIndex{
		ID:        captureID,
		UserID:    userID,
		NoteID:    noteID,
		Status:    model.StatusUploaded,
		Mode:      settings.CleanupMode,
		AudioKey:  audioKey,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Store capture
	if err := s.store.PutCapture(ctx, capture); err != nil {
		return nil, "", fmt.Errorf("failed to store capture: %w", err)
	}

	// Generate presigned upload URL
	uploadURL, err := s.objects.PresignPut(ctx, audioKey, contentType, 15*time.Minute)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate upload URL: %w", err)
	}

	return &capture, uploadURL, nil
}

// CompleteCapture runs the capture processing pipeline
func (s *CaptureService) CompleteCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	// Get capture
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to get capture: %w", err)
	}

	// Get note
	note, err := s.store.GetNote(ctx, userID, capture.NoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to get note: %w", err)
	}

	// Get audio data
	audioData, err := s.objects.Get(ctx, capture.AudioKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get audio: %w", err)
	}

	// Determine content type from audio key
	contentType := "audio/wav" // default
	if len(capture.AudioKey) > 4 {
		switch capture.AudioKey[len(capture.AudioKey)-3:] {
		case "mp3":
			contentType = "audio/mp3"
		case "m4a":
			contentType = "audio/m4a"
		case "ogg":
			contentType = "audio/ogg"
		}
	}

	// Step 1: STT transcription
	rawText, err := s.stt.Transcribe(ctx, audioData, contentType)
	if err != nil {
		// STT failed - update status and exit
		if updateErr := s.store.UpdateCaptureStatus(ctx, userID, captureID, model.StatusFailed, err.Error()); updateErr != nil {
			return nil, fmt.Errorf("failed to update capture status: %w", updateErr)
		}
		capture.Status = model.StatusFailed
		capture.Error = err.Error()
		return &capture, nil
	}

	// Generate keys for raw and clean files
	rawKey, err := keys.CaptureRaw(userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate raw key: %w", err)
	}

	// Store raw text
	if err := s.objects.Put(ctx, rawKey, []byte(rawText), "text/plain"); err != nil {
		return nil, fmt.Errorf("failed to store raw text: %w", err)
	}

	capture.RawKey = rawKey
	capture.Status = model.StatusTranscribed

	// Step 2: LLM cleanup
	cleanedText, err := s.llm.Cleanup(ctx, capture.Mode, rawText)
	if err != nil {
		// Cleanup failed - update status but keep raw text
		if updateErr := s.store.UpdateCaptureStatus(ctx, userID, captureID, model.StatusFailed, err.Error()); updateErr != nil {
			return nil, fmt.Errorf("failed to update capture status: %w", updateErr)
		}
		capture.Status = model.StatusFailed
		capture.Error = err.Error()
		return &capture, nil
	}

	// Generate key for clean file
	cleanKey, err := keys.CaptureClean(userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate clean key: %w", err)
	}

	// Store cleaned text
	if err := s.objects.Put(ctx, cleanKey, []byte(cleanedText), "text/plain"); err != nil {
		return nil, fmt.Errorf("failed to store clean text: %w", err)
	}

	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned

	// Step 3: Append to note
	if err := s.appendToNote(ctx, note.S3MarkdownKey, cleanedText); err != nil {
		return nil, fmt.Errorf("failed to append to note: %w", err)
	}

	// Update capture status to appended
	if err := s.store.UpdateCaptureStatus(ctx, userID, captureID, model.StatusAppended, ""); err != nil {
		return nil, fmt.Errorf("failed to update capture status: %w", err)
	}

	capture.Status = model.StatusAppended
	capture.Error = ""

	return &capture, nil
}

// appendToNote appends text to existing note content
func (s *CaptureService) appendToNote(ctx context.Context, noteKey, text string) error {
	// Get existing content
	existingContent, err := s.objects.Get(ctx, noteKey)
	if err != nil && err != repository.ErrNotFound {
		return fmt.Errorf("failed to get existing note: %w", err)
	}

	// Prepare new content
	var newContent string
	if len(existingContent) > 0 {
		newContent = string(existingContent) + "\n\n" + text
	} else {
		newContent = text
	}

	// Store updated content
	if err := s.objects.Put(ctx, noteKey, []byte(newContent), "text/plain"); err != nil {
		return fmt.Errorf("failed to update note: %w", err)
	}

	return nil
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
