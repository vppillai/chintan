package provider

import (
	"context"

	"github.com/vppillai/chintan/backend/internal/model"
)

// LLM interface for text cleanup/processing
type LLM interface {
	Cleanup(ctx context.Context, mode model.CleanupMode, raw string) (cleaned string, err error)
}
