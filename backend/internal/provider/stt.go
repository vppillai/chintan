package provider

import (
	"context"
)

// STT interface for speech-to-text transcription
type STT interface {
	Transcribe(ctx context.Context, audio []byte, contentType string) (text string, err error)
}
