package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestDeviceKeyShapeAndHashing(t *testing.T) {
	id, key, err := newDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "ck_"+id+"_") || !strings.HasPrefix(id, "dev_") {
		t.Fatalf("key %q does not carry its id %q", key, id)
	}
	gotID, ok := parseDeviceKey(key)
	if !ok || gotID != id {
		t.Fatalf("parseDeviceKey(%q) = %q, %v", key, gotID, ok)
	}
	for _, bad := range []string{"", "ck_", "ck_dev_abc", "ck_dev_abc_", "ck_abc_secret", "dev_abc_secret", "ck_dev_ab c_secret",
		"ck_dev_abc_" + strings.Repeat("f", 200), "ck_dev_abc_" + strings.Repeat("f", 48), "ck_dev_ABCDEF012345_" + strings.Repeat("f", 48)} {
		if _, ok := parseDeviceKey(bad); ok {
			t.Errorf("parseDeviceKey(%q) accepted a malformed key", bad)
		}
	}

	hash := hashDeviceKey(key)
	if len(hash) != 64 || hash == key || strings.Contains(hash, key[len(key)-8:]) {
		t.Fatalf("hash %q is not a hex digest of the key", hash)
	}
	if !deviceKeyMatches(hash, key) {
		t.Fatal("the key does not match its own hash")
	}
	// One character off, the same id, the same length: refused. The compare
	// is constant-time (crypto/subtle), which a test cannot time but the
	// implementation is pinned to by name.
	flipped := key[:len(key)-1] + map[bool]string{true: "0", false: "f"}[key[len(key)-1] == 'f']
	if deviceKeyMatches(hash, flipped) {
		t.Fatal("a key one character off matched")
	}
	if deviceKeyMatches("", key) || deviceKeyMatches(hash, "") {
		t.Fatal("an empty hash or key matched")
	}
}

func TestDeviceServiceIssuesListsRevokesAndCounts(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	at := time.Date(2026, 9, 24, 23, 59, 0, 0, time.UTC)
	svc := NewDeviceService(store).WithClock(func() time.Time { return at })

	if _, _, err := svc.CreateDevice(ctx, "u1", "  "); !errors.Is(err, ErrDeviceNameRequired) {
		t.Fatalf("blank name: %v", err)
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", strings.Repeat("x", model.MaxDeviceNameRunes+1)); !errors.Is(err, ErrDeviceNameTooLong) {
		t.Fatalf("long name: %v", err)
	}
	device, key, err := svc.CreateDevice(ctx, "u1", "  Kitchen   watch ")
	if err != nil {
		t.Fatal(err)
	}
	if device.Name != "Kitchen watch" || device.KeyHash == "" || device.KeyHash == key {
		t.Fatalf("device = %+v", device)
	}
	stored, err := store.GetDevice(ctx, "u1", device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.KeyHash != hashDeviceKey(key) {
		t.Fatal("the stored hash is not the key's")
	}

	// The limit counts live devices only.
	for i := 1; i < model.MaxDevicesPerTenant; i++ {
		if _, _, err := svc.CreateDevice(ctx, "u1", "Extra"); err != nil {
			t.Fatalf("device %d: %v", i, err)
		}
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", "Eleventh"); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("eleventh device: %v", err)
	}
	live, err := svc.ListDevices(ctx, "u1")
	if err != nil || len(live) != model.MaxDevicesPerTenant {
		t.Fatalf("ListDevices = %d, %v", len(live), err)
	}
	if err := svc.RevokeDevice(ctx, "u1", live[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", "Room again"); err != nil {
		t.Fatalf("after a revoke the slot is free: %v", err)
	}
	live, _ = svc.ListDevices(ctx, "u1")
	for _, d := range live {
		if d.Revoked() {
			t.Fatalf("a revoked device is listed: %+v", d)
		}
	}

	// Authenticate: the right key counts; a wrong secret, an unknown id, a
	// foreign tenant's guess at the id and a revoked key all read as unknown.
	got, err := svc.Authenticate(ctx, key)
	if err != nil || got.ID != device.ID || got.TenantID != "u1" || got.RequestsDay != 1 || got.LastUsedAt == "" {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
	for _, bad := range []string{key[:len(key)-1] + "0", "ck_dev_000000000000_" + strings.Repeat("a", 48), "not a key", ""} {
		if _, err := svc.Authenticate(ctx, bad); !errors.Is(err, ErrDeviceKeyUnknown) {
			t.Errorf("Authenticate(%q) = %v, want unknown", bad, err)
		}
	}

	// The day's counter: the 200th request passes, the 201st is refused, and
	// the next UTC day starts over.
	stored, _ = store.GetDevice(ctx, "u1", device.ID)
	stored.RequestsDay = model.DeviceDailyRequestLimit - 1
	if _, err := store.PutDevice(ctx, "u1", stored); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Authenticate(ctx, key); err != nil || got.RequestsDay != model.DeviceDailyRequestLimit {
		t.Fatalf("200th request: %+v, %v", got, err)
	}
	atLimit, _ := store.GetDevice(ctx, "u1", device.ID)
	if _, err := svc.Authenticate(ctx, key); !errors.Is(err, ErrDeviceDailyLimit) {
		t.Fatalf("201st request: %v, want the daily limit", err)
	}
	// Refused without a write: the row is as the 200th request left it.
	if after, _ := store.GetDevice(ctx, "u1", device.ID); after.Version != atLimit.Version || after.RequestsDay != model.DeviceDailyRequestLimit {
		t.Fatalf("the refused request wrote the row: version %d → %d, requests_day %d", atLimit.Version, after.Version, after.RequestsDay)
	}
	at = at.Add(2 * time.Minute) // past midnight UTC
	if got, err := svc.Authenticate(ctx, key); err != nil || got.RequestsDay != 1 || got.RequestsDayDate != "2026-09-25" {
		t.Fatalf("first request of the next day: %+v, %v", got, err)
	}

	// Revoked: unknown from then on, and revoking again is not an error.
	if err := svc.RevokeDevice(ctx, "u1", device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, key); !errors.Is(err, ErrDeviceKeyUnknown) {
		t.Fatalf("revoked key: %v", err)
	}
	if err := svc.RevokeDevice(ctx, "u1", device.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := svc.RevokeDevice(ctx, "u2", device.ID); err == nil {
		t.Fatal("another tenant revoked the device")
	}
}

// racingRevokeStore is a store in which, between RevokeDevice's read and
// its write, the inbox counts a request against the same row — once.
type racingRevokeStore struct {
	repository.Store
	raced bool
}

func (s *racingRevokeStore) GetDevice(ctx context.Context, tenantID, deviceID string) (model.Device, error) {
	d, err := s.Store.GetDevice(ctx, tenantID, deviceID)
	if err != nil || s.raced {
		return d, err
	}
	s.raced = true
	counted := d
	counted.RequestsDay++
	if _, err := s.PutDevice(ctx, tenantID, counted); err != nil {
		return model.Device{}, err
	}
	return d, nil
}

// A revoke that loses to a counter write is retried rather than failed: the
// key stops working, and the counter write it raced with is kept.
func TestRevokeDeviceOutlivesACounterWriteItRacedWith(t *testing.T) {
	ctx := context.Background()
	mem := memory.NewStore()
	device, key, err := NewDeviceService(mem).CreateDevice(ctx, "u1", "Watch")
	if err != nil {
		t.Fatal(err)
	}
	racing := &racingRevokeStore{Store: mem}
	if err := NewDeviceService(racing).RevokeDevice(ctx, "u1", device.ID); err != nil {
		t.Fatalf("RevokeDevice against a counter write: %v", err)
	}
	stored, err := mem.GetDevice(ctx, "u1", device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !racing.raced || !stored.Revoked() || stored.RequestsDay != 1 {
		t.Fatalf("stored = %+v (raced %v); want revoked with the counter write kept", stored, racing.raced)
	}
	if _, err := NewDeviceService(mem).Authenticate(ctx, key); !errors.Is(err, ErrDeviceKeyUnknown) {
		t.Fatalf("revoked key: %v", err)
	}
}
