package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Device errors. The sentences reach the user or the device as written; the
// two the inbox answers with are fixed so a probing client learns nothing
// from their wording.
var (
	ErrDeviceNameRequired = errors.New("name is required")
	ErrDeviceNameTooLong  = errors.New("name is longer than 60 characters")
	ErrDeviceLimit        = errors.New("you already have ten devices; remove one first")
	// ErrDeviceKeyUnknown is the inbox's one answer to a key it cannot use:
	// malformed, never issued, or revoked. It does not say which.
	ErrDeviceKeyUnknown = errors.New("unknown device key")
	// ErrDeviceDailyLimit is the inbox's answer to a key past
	// model.DeviceDailyRequestLimit for the UTC day.
	ErrDeviceDailyLimit = errors.New("this device has reached today's limit")
)

// Device key format: ck_<id>_<secret>, where the id is dev_<12 hex> and the
// secret is 24 random bytes in hex — hex rather than base64url so the
// secret can hold no underscore and the last underscore always separates the
// two. The id is public — it is the GSI1 key the inbox finds the tenant by —
// and the secret is what the stored hash covers together with it.
const (
	deviceKeyPrefix = "ck_"
	deviceIDPrefix  = "dev_"
	// maxDeviceKeyLen bounds what Authenticate will hash. A real key is 68
	// characters; a header many times that is not one.
	maxDeviceKeyLen = 128
)

var deviceKeyCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// newDeviceKey mints an id and its key.
func newDeviceKey() (id, key string, err error) {
	idBytes := make([]byte, 6)
	secret := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate device id: %w", err)
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("failed to generate device key: %w", err)
	}
	id = deviceIDPrefix + hex.EncodeToString(idBytes)
	return id, deviceKeyPrefix + id + "_" + hex.EncodeToString(secret), nil
}

// parseDeviceKey recovers the key id from a presented key, or reports that
// the string is not a key at all.
func parseDeviceKey(raw string) (id string, ok bool) {
	if len(raw) > maxDeviceKeyLen {
		return "", false
	}
	rest, hasPrefix := strings.CutPrefix(raw, deviceKeyPrefix)
	if !hasPrefix {
		return "", false
	}
	cut := strings.LastIndexByte(rest, '_')
	if cut <= 0 || cut == len(rest)-1 {
		return "", false
	}
	id, secret := rest[:cut], rest[cut+1:]
	if !strings.HasPrefix(id, deviceIDPrefix) || !deviceKeyCharset.MatchString(id) || !deviceKeyCharset.MatchString(secret) {
		return "", false
	}
	return id, true
}

// hashDeviceKey is what the row stores in place of the key.
func hashDeviceKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// deviceKeyMatches compares a presented key with a stored hash in constant
// time, so the comparison's duration says nothing about how many leading
// characters were right.
func deviceKeyMatches(storedHash, presented string) bool {
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashDeviceKey(presented))) == 1
}

// DeviceService issues, lists and revokes device keys, and authenticates the
// inbox's requests with them.
type DeviceService struct {
	store repository.Store
	now   func() time.Time
}

// NewDeviceService creates a device service.
func NewDeviceService(store repository.Store) *DeviceService {
	return &DeviceService{store: store, now: time.Now}
}

// WithClock replaces the wall clock, for tests.
func (s *DeviceService) WithClock(now func() time.Time) *DeviceService {
	s.now = now
	return s
}

// CreateDevice issues a key. The key is returned once, here, and only its
// hash is stored.
func (s *DeviceService) CreateDevice(ctx context.Context, userID, name string) (model.Device, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return model.Device{}, "", ErrDeviceNameRequired
	}
	if len([]rune(name)) > model.MaxDeviceNameRunes {
		return model.Device{}, "", ErrDeviceNameTooLong
	}
	live, err := s.ListDevices(ctx, userID)
	if err != nil {
		return model.Device{}, "", err
	}
	if len(live) >= model.MaxDevicesPerTenant {
		return model.Device{}, "", ErrDeviceLimit
	}
	id, key, err := newDeviceKey()
	if err != nil {
		return model.Device{}, "", err
	}
	stored, err := s.store.PutDevice(ctx, userID, model.Device{
		ID:        id,
		TenantID:  userID,
		Name:      name,
		KeyHash:   hashDeviceKey(key),
		CreatedAt: model.FormatTime(s.now()),
	})
	if err != nil {
		return model.Device{}, "", fmt.Errorf("failed to store device: %w", err)
	}
	return stored, key, nil
}

// ListDevices is the tenant's live devices, oldest first. Revoked rows stay
// in the table for the record and are not listed.
func (s *DeviceService) ListDevices(ctx context.Context, userID string) ([]model.Device, error) {
	all, err := s.store.ListDevices(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}
	live := make([]model.Device, 0, len(all))
	for _, d := range all {
		if !d.Revoked() {
			live = append(live, d)
		}
	}
	return live, nil
}

// deviceWriteAttempts bounds the retries of a device row write that loses
// to a concurrent write of the same row: the counter against another
// request on the same key, the revoke against the counter.
const deviceWriteAttempts = 5

// RevokeDevice makes the key unknown to the inbox from now on. Revoking a
// revoked device changes nothing; a device that is not there is ErrNotFound.
//
// The write is retried when it loses to Authenticate's counter write: the
// moment a revoke has to land is exactly when the key is being used.
func (s *DeviceService) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	for attempt := 0; attempt < deviceWriteAttempts; attempt++ {
		d, err := s.store.GetDevice(ctx, userID, deviceID)
		if err != nil {
			return err
		}
		if d.Revoked() {
			return nil
		}
		d.RevokedAt = model.FormatTime(s.now())
		_, err = s.store.PutDevice(ctx, userID, d)
		if errors.Is(err, repository.ErrVersionConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to revoke device: %w", err)
		}
		return nil
	}
	return fmt.Errorf("device revoke lost %d races in a row", deviceWriteAttempts)
}

// Authenticate resolves a presented key to its device and counts the
// request against the key's day. Every failure to use the key — malformed,
// unknown, revoked, wrong secret — is ErrDeviceKeyUnknown; a key at its
// daily limit is ErrDeviceDailyLimit, refused before anything is written,
// so a key hammered past the limit costs one read per request and no write.
//
// The counter and last_used_at are written under the row's version, so a
// revoke landing between the read and the write is not overwritten: the
// write loses, the row is read again, and the revoke is seen. Two requests
// from one device at the same moment lose to each other the same way and
// try again; a burst that loses every attempt is a fault, not a refusal.
func (s *DeviceService) Authenticate(ctx context.Context, rawKey string) (model.Device, error) {
	id, ok := parseDeviceKey(rawKey)
	if !ok {
		return model.Device{}, ErrDeviceKeyUnknown
	}
	for attempt := 0; attempt < deviceWriteAttempts; attempt++ {
		d, err := s.store.LookupDeviceKey(ctx, id)
		if errors.Is(err, repository.ErrNotFound) {
			return model.Device{}, ErrDeviceKeyUnknown
		}
		if err != nil {
			return model.Device{}, fmt.Errorf("failed to look up device key: %w", err)
		}
		if d.Revoked() || !deviceKeyMatches(d.KeyHash, rawKey) {
			return model.Device{}, ErrDeviceKeyUnknown
		}

		now := s.now().UTC()
		day := now.Format("2006-01-02")
		if d.RequestsDayDate != day {
			d.RequestsDayDate, d.RequestsDay = day, 0
		}
		if d.RequestsDay >= model.DeviceDailyRequestLimit {
			return d, ErrDeviceDailyLimit
		}
		d.RequestsDay++
		d.LastUsedAt = model.FormatTime(now)
		stored, err := s.store.PutDevice(ctx, d.TenantID, d)
		if errors.Is(err, repository.ErrVersionConflict) {
			continue
		}
		if err != nil {
			return model.Device{}, fmt.Errorf("failed to count the device's request: %w", err)
		}
		return stored, nil
	}
	return model.Device{}, fmt.Errorf("device usage write lost %d races in a row", deviceWriteAttempts)
}
