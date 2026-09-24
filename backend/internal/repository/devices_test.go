package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// A device row on the real store: written under its version, found by its
// key id through the sparse index from no tenant at all, listed with its
// tenant's others, and unfindable once revoked.
func TestDeviceRowsRoundTripAndTheKeyIndexIsSparse(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()

	stored, err := store.PutDevice(ctx, "tenant-a", model.Device{
		ID: "dev_0001", Name: "Watch", KeyHash: "ab" + "cd", CreatedAt: "2026-09-24T08:00:00.000000000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || stored.TenantID != "tenant-a" {
		t.Fatalf("stored = %+v", stored)
	}
	// The same id again with version 0 is a lost race, not an overwrite.
	if _, err := store.PutDevice(ctx, "tenant-a", model.Device{ID: "dev_0001", Name: "Impostor"}); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("second create: %v", err)
	}

	found, err := store.LookupDeviceKey(ctx, "dev_0001")
	if err != nil {
		t.Fatalf("LookupDeviceKey: %v", err)
	}
	if found.TenantID != "tenant-a" || found.KeyHash != "abcd" || found.Name != "Watch" {
		t.Fatalf("found = %+v", found)
	}
	if _, err := store.LookupDeviceKey(ctx, "dev_never"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}

	// The counter write carries the version forward.
	found.RequestsDay, found.RequestsDayDate, found.LastUsedAt = 1, "2026-09-24", "2026-09-24T09:00:00.000000000Z"
	counted, err := store.PutDevice(ctx, found.TenantID, found)
	if err != nil || counted.Version != 2 {
		t.Fatalf("count: %+v, %v", counted, err)
	}
	if _, err := store.PutDevice(ctx, found.TenantID, found); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("a stale write was accepted: %v", err)
	}
	again, err := store.GetDevice(ctx, "tenant-a", "dev_0001")
	if err != nil || again.RequestsDay != 1 || again.LastUsedAt == "" {
		t.Fatalf("GetDevice = %+v, %v", again, err)
	}

	if _, err := store.PutDevice(ctx, "tenant-a", model.Device{ID: "dev_0002", Name: "Ring", CreatedAt: "2026-09-24T08:01:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDevice(ctx, "tenant-b", model.Device{ID: "dev_0003", Name: "Theirs", CreatedAt: "2026-09-24T08:02:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	devices, err := store.ListDevices(ctx, "tenant-a")
	if err != nil || len(devices) != 2 || devices[0].ID != "dev_0001" || devices[1].ID != "dev_0002" {
		t.Fatalf("ListDevices = %+v, %v", devices, err)
	}

	// Revoked: the row stays, with a TTL and without its index keys, so the
	// lookup cannot see it.
	again.RevokedAt = "2026-09-24T10:00:00.000000000Z"
	if _, err := store.PutDevice(ctx, "tenant-a", again); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupDeviceKey(ctx, "dev_0001"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("revoked key found: %v", err)
	}
	row := api.items["USER#tenant-a"]["DEVICE#dev_0001"]
	if _, indexed := row["gsi1pk"]; indexed {
		t.Fatal("a revoked row still carries its index key")
	}
	if _, ok := row["ttl"]; !ok {
		t.Fatal("a revoked row has no TTL")
	}
	revoked, err := store.GetDevice(ctx, "tenant-a", "dev_0001")
	if err != nil || !revoked.Revoked() {
		t.Fatalf("GetDevice after revoke = %+v, %v", revoked, err)
	}
}
