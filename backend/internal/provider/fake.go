package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
)

// FakeSTT is a fake STT implementation for testing
type FakeSTT struct {
	ShouldFail bool
	Response   string
}

func (f *FakeSTT) Transcribe(ctx context.Context, audio []byte, contentType string) (text string, err error) {
	if f.ShouldFail {
		return "", fmt.Errorf("fake STT failed")
	}
	if f.Response != "" {
		return f.Response, nil
	}
	return fmt.Sprintf("transcribed audio: %d bytes of %s", len(audio), contentType), nil
}

// FakeLLM is a fake LLM implementation for testing
type FakeLLM struct {
	ShouldFail bool
	Response   string
}

func (f *FakeLLM) Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (cleaned string, err error) {
	if f.ShouldFail {
		return "", fmt.Errorf("fake LLM failed")
	}
	if f.Response != "" {
		return f.Response, nil
	}

	// Simple fake cleanup based on mode
	switch mode {
	case model.CleanupFaithful:
		return "[faithful] " + strings.ToLower(raw), nil
	case model.CleanupPolished:
		return "[polished] " + strings.Title(strings.ToLower(raw)), nil
	default:
		return raw, nil
	}
}
