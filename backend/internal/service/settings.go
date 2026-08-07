package service

import (
	"context"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// SettingsService handles user settings operations
type SettingsService struct {
	store repository.Store
}

// NewSettingsService creates a new settings service
func NewSettingsService(store repository.Store) *SettingsService {
	return &SettingsService{
		store: store,
	}
}

// GetSettings retrieves user settings, returning defaults if not found
func (s *SettingsService) GetSettings(ctx context.Context, userID string) (model.Settings, error) {
	settings, err := s.store.GetSettings(ctx, userID)
	if err == repository.ErrNotFound {
		// Return defaults for new users
		return model.Settings{
			CleanupMode:   model.CleanupFaithful,
			RetentionDays: 0,
		}, nil
	}
	return settings, err
}

// UpdateSettings updates user settings
func (s *SettingsService) UpdateSettings(ctx context.Context, userID string, settings model.Settings) error {
	return s.store.PutSettings(ctx, userID, settings)
}
