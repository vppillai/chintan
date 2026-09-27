package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
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
	store := dynamofake.NewStore()
	at := time.Date(2026, 9, 24, 23, 59, 0, 0, time.UTC)
	svc := NewDeviceService(store).WithClock(func() time.Time { return at })

	if _, _, err := svc.CreateDevice(ctx, "u1", "  ", nil); !errors.Is(err, ErrDeviceNameRequired) {
		t.Fatalf("blank name: %v", err)
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", strings.Repeat("x", model.MaxDeviceNameRunes+1), nil); !errors.Is(err, ErrDeviceNameTooLong) {
		t.Fatalf("long name: %v", err)
	}
	device, key, err := svc.CreateDevice(ctx, "u1", "  Kitchen   watch ", nil)
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
		if _, _, err := svc.CreateDevice(ctx, "u1", "Extra", nil); err != nil {
			t.Fatalf("device %d: %v", i, err)
		}
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", "Eleventh", nil); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("eleventh device: %v", err)
	}
	live, err := svc.ListDevices(ctx, "u1")
	if err != nil || len(live) != model.MaxDevicesPerTenant {
		t.Fatalf("ListDevices = %d, %v", len(live), err)
	}
	// Revoke one of the extras, never the device whose key the rest of the
	// test presents: with one clock every row shares a CreatedAt, so the
	// listing's order among them is by id, which is random.
	victim := live[0]
	if victim.ID == device.ID {
		victim = live[1]
	}
	if err := svc.RevokeDevice(ctx, "u1", victim.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateDevice(ctx, "u1", "Room again", nil); err != nil {
		t.Fatalf("after a revoke the slot is free: %v", err)
	}
	live, _ = svc.ListDevices(ctx, "u1")
	for _, d := range live {
		if d.Revoked() {
			t.Fatalf("a revoked device is listed: %+v", d)
		}
	}

	// Authenticate: the right key counts the request, its bytes and the
	// month; a wrong secret, an unknown id, a foreign tenant's guess at the
	// id and a revoked key all read as unknown on the wire, and each says
	// why underneath, for the metric.
	got, err := svc.Authenticate(ctx, key, "203.0.113.7:4321", 100)
	if err != nil || got.ID != device.ID || got.TenantID != "u1" || got.RequestsDay != 1 || got.LastUsedAt == "" {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
	// Where from, as a neighbourhood: never the address itself.
	if got.LastUsedFrom != "203.0.113.x" {
		t.Fatalf("last_used_from = %q, want 203.0.113.x", got.LastUsedFrom)
	}
	if got.RequestsMonth != 1 || got.BytesMonth != 100 || got.Month != "2026-09" {
		t.Fatalf("month counters after one request of 100 bytes = %+v", got)
	}
	// Flip the last character so the "wrong secret" can never equal the key.
	wrongSecret := key[:len(key)-1] + "0"
	if strings.HasSuffix(key, "0") {
		wrongSecret = key[:len(key)-1] + "1"
	}
	for bad, want := range map[string]DeviceRefusal{
		wrongSecret: {Reason: "wrong_secret", DeviceID: device.ID},
		"ck_dev_000000000000_" + strings.Repeat("a", 48): {Reason: "unknown", DeviceID: "dev_000000000000"},
		"not a key": {Reason: "malformed"},
		"":          {Reason: "malformed"},
	} {
		_, err := svc.Authenticate(ctx, bad, "203.0.113.7", 5)
		if !errors.Is(err, ErrDeviceKeyUnknown) || err.Error() != ErrDeviceKeyUnknown.Error() {
			t.Errorf("Authenticate(%q) = %v, want unknown", bad, err)
		}
		var ref *DeviceRefusal
		if !errors.As(err, &ref) || ref.Reason != want.Reason || ref.DeviceID != want.DeviceID {
			t.Errorf("Authenticate(%q) refusal = %+v, want %+v", bad, ref, want)
		}
	}
	// A refusal counts nothing.
	if after, _ := store.GetDevice(ctx, "u1", device.ID); after.RequestsMonth != 1 || after.BytesMonth != 100 {
		t.Fatalf("a refused request was counted: %+v", after)
	}

	// The day's counter: the 200th request passes, the 201st is refused
	// before anything is counted, and the next UTC day starts over.
	stored, _ = store.GetDevice(ctx, "u1", device.ID)
	stored.RequestsDay = model.DeviceDailyRequestLimit - 1
	if _, err := store.PutDevice(ctx, "u1", stored); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Authenticate(ctx, key, "203.0.113.7", 200); err != nil || got.RequestsDay != model.DeviceDailyRequestLimit || got.RequestsMonth != 2 || got.BytesMonth != 300 {
		t.Fatalf("200th request: %+v, %v", got, err)
	}
	atLimit, _ := store.GetDevice(ctx, "u1", device.ID)
	_, err = svc.Authenticate(ctx, key, "203.0.113.7", 50)
	if !errors.Is(err, ErrDeviceDailyLimit) {
		t.Fatalf("201st request: %v, want the daily limit", err)
	}
	var limited *DeviceRefusal
	if !errors.As(err, &limited) || limited.Reason != "daily_limit" || limited.DeviceID != device.ID {
		t.Fatalf("201st request's refusal = %+v", limited)
	}
	// Refused without a write: the row is as the 200th request left it.
	if after, _ := store.GetDevice(ctx, "u1", device.ID); after.Version != atLimit.Version || after.RequestsDay != model.DeviceDailyRequestLimit || after.BytesMonth != 300 {
		t.Fatalf("the refused request wrote the row: version %d → %d, requests_day %d, bytes_month %d", atLimit.Version, after.Version, after.RequestsDay, after.BytesMonth)
	}
	at = at.Add(2 * time.Minute) // past midnight UTC, same month
	if got, err := svc.Authenticate(ctx, key, "203.0.113.7", 0); err != nil || got.RequestsDay != 1 || got.RequestsDayDate != "2026-09-25" {
		t.Fatalf("first request of the next day: %+v, %v", got, err)
	} else if got.RequestsMonth != 3 || got.BytesMonth != 300 || got.Month != "2026-09" {
		t.Fatalf("month counters after 100, 200 and 0 bytes = %+v", got)
	}
	// The list shows the month's counters while it is the month, and
	// nothing once it is not; the row keeps them until its next request.
	listed := func() model.Device {
		t.Helper()
		live, err := svc.ListDevices(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range live {
			if d.ID == device.ID {
				return d
			}
		}
		t.Fatal("the device is not listed")
		return model.Device{}
	}
	if d := listed(); d.Month != "2026-09" || d.RequestsMonth != 3 || d.BytesMonth != 300 {
		t.Fatalf("listed in September: %+v", d)
	}
	at = time.Date(2026, 10, 1, 0, 0, 1, 0, time.UTC)
	if d := listed(); d.Month != "" || d.RequestsMonth != 0 || d.BytesMonth != 0 {
		t.Fatalf("listed in October before any request: %+v; want September's counters cleared", d)
	}
	if got, err := svc.Authenticate(ctx, key, "203.0.113.7", 7); err != nil || got.RequestsMonth != 1 || got.BytesMonth != 7 || got.Month != "2026-10" {
		t.Fatalf("first request of October: %+v, %v", got, err)
	}

	// Revoked: unknown on the wire from then on. The revoke drops the row's
	// index keys, so the lookup no longer finds it; for the moment in which
	// GSI1 still carries the entry, the row reads as revoked underneath.
	// Revoking again is not an error.
	if err := svc.RevokeDevice(ctx, "u1", device.ID); err != nil {
		t.Fatal(err)
	}
	var refusal *DeviceRefusal
	if _, err := svc.Authenticate(ctx, key, "203.0.113.7", 0); !errors.Is(err, ErrDeviceKeyUnknown) || !errors.As(err, &refusal) || refusal.Reason != "unknown" {
		t.Fatalf("revoked key once the index dropped it: %v (refusal %+v)", err, refusal)
	}
	lagging := NewDeviceService(indexLagStore{Store: store, tenantID: "u1", deviceID: device.ID}).WithClock(func() time.Time { return at })
	refusal = nil
	if _, err := lagging.Authenticate(ctx, key, "203.0.113.7", 0); !errors.Is(err, ErrDeviceKeyUnknown) || !errors.As(err, &refusal) || refusal.Reason != "revoked" || refusal.DeviceID != device.ID {
		t.Fatalf("revoked key while the index still carries it: %v (refusal %+v)", err, refusal)
	}
	if err := svc.RevokeDevice(ctx, "u1", device.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := svc.RevokeDevice(ctx, "u2", device.ID); err == nil {
		t.Fatal("another tenant revoked the device")
	}
}

// indexLagStore is the moment after a revoke in which GSI1 still carries the
// key's entry: the lookup finds the row and hands it over as it is, revoked_at
// and all, the way the real index does until the write has propagated.
type indexLagStore struct {
	repository.Store
	tenantID, deviceID string
}

func (s indexLagStore) LookupDeviceKey(ctx context.Context, _ string) (model.Device, error) {
	return s.GetDevice(ctx, s.tenantID, s.deviceID)
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
	mem := dynamofake.NewStore()
	device, key, err := NewDeviceService(mem).CreateDevice(ctx, "u1", "Watch", nil)
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
	if _, err := NewDeviceService(mem).Authenticate(ctx, key, "203.0.113.7", 0); !errors.Is(err, ErrDeviceKeyUnknown) {
		t.Fatalf("revoked key: %v", err)
	}
}

// An expiry is optional and bounded; past it the key reads as unknown on
// the wire and expired underneath, and the device stays listed so the card
// can say so (WH-A).
func TestDeviceKeyExpiry(t *testing.T) {
	ctx := context.Background()
	store := dynamofake.NewStore()
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	svc := NewDeviceService(store).WithClock(func() time.Time { return at })

	for _, days := range []int{0, -1, model.MaxDeviceExpiryDays + 1} {
		if _, _, err := svc.CreateDevice(ctx, "u1", "Script", &days); !errors.Is(err, ErrDeviceExpiryOutOfRange) {
			t.Fatalf("expires_in_days %d: %v, want out of range", days, err)
		}
	}
	perpetual, perpetualKey, err := svc.CreateDevice(ctx, "u1", "Ring", nil)
	if err != nil || perpetual.ExpiresAt != "" {
		t.Fatalf("a key with no expiry: %+v, %v", perpetual, err)
	}
	thirty := 30
	device, key, err := svc.CreateDevice(ctx, "u1", "Script", &thirty)
	if err != nil || device.ExpiresAt != "2026-10-24T12:00:00.000000000Z" {
		t.Fatalf("a thirty-day key: %+v, %v", device, err)
	}
	if _, err := svc.Authenticate(ctx, key, "203.0.113.7", 0); err != nil {
		t.Fatalf("before expiry: %v", err)
	}

	at = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) // the instant itself is past
	_, err = svc.Authenticate(ctx, key, "203.0.113.7", 0)
	var ref *DeviceRefusal
	if !errors.Is(err, ErrDeviceKeyUnknown) || err.Error() != ErrDeviceKeyUnknown.Error() || !errors.As(err, &ref) || ref.Reason != "expired" || ref.DeviceID != device.ID {
		t.Fatalf("expired key: %v (refusal %+v)", err, ref)
	}
	if _, err := svc.Authenticate(ctx, perpetualKey, "203.0.113.7", 0); err != nil {
		t.Fatalf("the perpetual key still works: %v", err)
	}
	// Still listed, with its date, so the card can say Expired and offer Remove.
	live, err := svc.ListDevices(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, d := range live {
		listed = listed || (d.ID == device.ID && d.ExpiresAt == device.ExpiresAt)
	}
	if !listed {
		t.Fatalf("the expired device is not listed: %+v", live)
	}
}

// The neighbourhood is the /24 or the /48, from a bare address or ip:port,
// and nothing at all for what is not an address (WH-B).
func TestNeighbourhoodIsCoarse(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":               "203.0.113.x",
		"203.0.113.7:4321":          "203.0.113.x",
		"192.0.2.1:1234":            "192.0.2.x",
		"::ffff:203.0.113.7":        "203.0.113.x",
		"2001:db8:1:2:3:4:5:6":      "2001:db8:1::x",
		"[2001:db8:1:2::6]:443":     "2001:db8:1::x",
		"2001:db8::1":               "2001:db8::x",
		"":                          "",
		"not an address":            "",
		"203.0.113.7, 198.51.100.1": "",
	} {
		if got := neighbourhood(in); got != want {
			t.Errorf("neighbourhood(%q) = %q, want %q", in, got, want)
		}
	}
}
