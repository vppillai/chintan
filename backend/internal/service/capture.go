package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
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
	_, err := s.store.GetNote(ctx, userID, noteID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get note: %w", err)
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

	note, err := s.store.GetNote(ctx, userID, capture.NoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to get note: %w", err)
	}

	audioData, err := s.objects.Get(ctx, capture.AudioKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get audio: %w", err)
	}

	contentType := contentTypeForAudioKey(capture.AudioKey)

	if capture.Status == model.StatusUploaded || capture.RawKey == "" {
		rawText, err := s.stt.Transcribe(ctx, audioData, contentType)
		if err != nil {
			capture.Status = model.StatusFailed
			capture.Error = err.Error()
			_ = s.store.PutCapture(ctx, capture)
			return &capture, nil
		}

		rawKey, err := keys.CaptureRaw(userID, captureID)
		if err != nil {
			return nil, fmt.Errorf("failed to generate raw key: %w", err)
		}
		if err := s.objects.Put(ctx, rawKey, []byte(rawText), "text/plain"); err != nil {
			return nil, fmt.Errorf("failed to store raw text: %w", err)
		}
		capture.RawKey = rawKey
		capture.Status = model.StatusTranscribed
		capture.Error = ""
		if err := s.store.PutCapture(ctx, capture); err != nil {
			return nil, fmt.Errorf("failed to update capture: %w", err)
		}
	}

	if capture.Status == model.StatusTranscribed || capture.CleanKey == "" {
		rawBytes, err := s.objects.Get(ctx, capture.RawKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get raw text: %w", err)
		}
		cleanedText, err := s.llm.Cleanup(ctx, capture.Mode, string(rawBytes))
		if err != nil {
			capture.Status = model.StatusFailed
			capture.Error = err.Error()
			_ = s.store.PutCapture(ctx, capture)
			return &capture, nil
		}

		cleanKey, err := keys.CaptureClean(userID, captureID)
		if err != nil {
			return nil, fmt.Errorf("failed to generate clean key: %w", err)
		}
		if err := s.objects.Put(ctx, cleanKey, []byte(cleanedText), "text/plain"); err != nil {
			return nil, fmt.Errorf("failed to store clean text: %w", err)
		}
		capture.CleanKey = cleanKey
		capture.Status = model.StatusCleaned
		capture.Error = ""
		if err := s.store.PutCapture(ctx, capture); err != nil {
			return nil, fmt.Errorf("failed to update capture: %w", err)
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
