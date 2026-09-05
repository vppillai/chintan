package service

import (
	"context"
	"fmt"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// maxStorageRows bounds each of the two index walks a storage summary makes.
// Two thousand captures or notes is far beyond a personal corpus today; a
// tenant past it gets a summary marked approximate rather than a request that
// pages through the whole partition on every visit to the You screen. (Each
// notes page the walk asks for already costs the store a drain of the whole
// partition — see repository.MaxNotesDrained — so the bound here is on how
// many times that is paid, not on what one page reads.)
const maxStorageRows = 2000

// StorageSummary is what a tenant's recordings and notes add up to, computed
// from the index rows at read time. Nothing is stored for it: the rows already
// carry the duration and the uploaded size of every recording, and a running
// total would be one more counter to keep in step with every delete and move.
type StorageSummary struct {
	Recordings int
	// AudioSeconds is the sum of the recordings' durations as transcribed.
	AudioSeconds float64
	// AudioBytes is the sum of the uploaded sizes S3 reported. A capture
	// processed before the size was recorded (2026-09) contributes zero, so
	// this is a lower bound on an account with older recordings.
	AudioBytes int64
	// Notes is the active notes; the archive is not counted.
	Notes int
	// Approximate is true when either walk stopped at maxStorageRows.
	Approximate bool
}

// StorageService answers the storage half of GET /v1/usage.
type StorageService struct {
	store repository.Store
}

// NewStorageService builds the service over the store.
func NewStorageService(store repository.Store) *StorageService {
	return &StorageService{store: store}
}

// Summarize walks the tenant's captures and active notes, up to
// maxStorageRows of each.
func (s *StorageService) Summarize(ctx context.Context, userID string) (StorageSummary, error) {
	var out StorageSummary

	captures, err := repository.DrainPages(ctx, maxStorageRows, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCaptures(ctx, userID, opts)
	})
	if err != nil {
		return StorageSummary{}, fmt.Errorf("storage: list captures: %w", err)
	}
	for _, c := range captures {
		out.Recordings++
		out.AudioSeconds += float64(c.DurationMS) / 1000
		out.AudioBytes += c.AudioBytes
	}

	notes, err := repository.DrainPages(ctx, maxStorageRows, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return s.store.ListNotes(ctx, userID, opts)
	})
	if err != nil {
		return StorageSummary{}, fmt.Errorf("storage: list notes: %w", err)
	}
	out.Notes = len(notes)

	// DrainPages cuts the set to the cap, so hitting it exactly is the only
	// evidence there may have been more.
	out.Approximate = len(captures) >= maxStorageRows || len(notes) >= maxStorageRows
	return out, nil
}
