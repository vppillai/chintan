package service

import (
	"errors"
	"fmt"

	"github.com/vppillai/chintan/backend/internal/model"
)

var (
	// ErrInvalidCleanupMode rejects a cleanup mode outside the known set.
	ErrInvalidCleanupMode = errors.New("cleanup_mode must be faithful or polished")
	// ErrInvalidRetentionDays rejects a retention that is negative or absurd.
	ErrInvalidRetentionDays = errors.New("retention_days must be between 0 and 3650")
	// ErrInvalidTheme rejects an unknown theme.
	ErrInvalidTheme = errors.New("theme must be ink, nocturne or system")
)

// ValidateSettings checks a settings record and returns the canonical form that
// will be stored.
//
// The caller stores and returns this value, not the request body. v1 echoed the
// request straight back, which hid every coercion from the client: a retention
// of "30" that was stored as 30 and a theme of "purple" that was stored as
// nothing both came back looking accepted.
func ValidateSettings(s model.Settings) (model.Settings, error) {
	out := model.Settings{}

	switch s.CleanupMode {
	case "":
		out.CleanupMode = model.CleanupFaithful
	case model.CleanupFaithful, model.CleanupPolished:
		out.CleanupMode = s.CleanupMode
	default:
		return model.Settings{}, fmt.Errorf("%w", ErrInvalidCleanupMode)
	}

	switch {
	case s.RetentionDays < 0:
		return model.Settings{}, fmt.Errorf("%w", ErrInvalidRetentionDays)
	case s.RetentionDays > model.MaxRetentionDays:
		return model.Settings{}, fmt.Errorf("%w", ErrInvalidRetentionDays)
	default:
		// Stored as the tier that will actually expire the audio, not as the
		// number that was typed. Only a fixed set of retentions can be enforced
		// — an S3 lifecycle rule carries its own ExpirationInDays and cannot
		// read one out of a note — so storing 45 would mean returning 45 to a
		// client while deleting on day 30. The response echoes what was stored,
		// which is the value the system will honour.
		out.RetentionDays = model.RetentionTierFor(s.RetentionDays)
	}

	switch s.Theme {
	case "":
		out.Theme = model.ThemeInk
	case model.ThemeInk, model.ThemeNocturne, model.ThemeSystem:
		out.Theme = s.Theme
	default:
		return model.Settings{}, fmt.Errorf("%w", ErrInvalidTheme)
	}

	return out, nil
}

// NormalizeSettings fills in the fields a record written before v2 does not
// carry, so a GET always answers the shape the contract declares.
func NormalizeSettings(s model.Settings) model.Settings {
	if s.CleanupMode == "" {
		s.CleanupMode = model.CleanupFaithful
	}
	if s.Theme == "" {
		s.Theme = model.ThemeInk
	}
	// A record written before retention was expressed in tiers can hold any
	// number. Reporting it verbatim would tell the user their audio expires on a
	// day nothing acts on, so a read resolves it the same way a write does.
	s.RetentionDays = model.RetentionTierFor(s.RetentionDays)
	return s
}
