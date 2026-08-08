package provider

import (
	"context"

	"github.com/vppillai/chintan/backend/internal/model"
)

// TokenUsage is what a completion consumed, as the provider reported it.
//
// It carries counts and nothing else: the breaker prices a call from these, and
// a count can never leak what was said.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// Cleaned is the result of a cleanup call.
type Cleaned struct {
	Text  string
	Usage TokenUsage
}

// LLM interface for text cleanup/processing
type LLM interface {
	Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (Cleaned, error)
}
