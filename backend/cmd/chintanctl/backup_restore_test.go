package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBackupRestoreRoundTripIsExact(t *testing.T) {
	ctx := context.Background()
	src, srcPart, srcBlobs := newTestEnv(nil)
	seedTenant(t, srcPart, srcBlobs, "tenantA")

	// A kind with no renderer, a binary attribute, a number set and an empty
	// list: the shapes a decode-through-map[string]any backup would flatten.
	yes := true
	put(t, srcPart, Item{
		"pk":      StringAttr(tenantPK("tenantA")),
		"sk":      StringAttr("REMINDER#r1"),
		"blob":    AttrValue{B: []byte{0x00, 0xff, 0x10}},
		"numbers": AttrValue{NS: []string{"1", "2.5"}},
		"flag":    AttrValue{BOOL: &yes},
		"empty":   AttrValue{L: []AttrValue{}},
		"nested":  AttrValue{M: map[string]AttrValue{"n": NumberAttr(9007199254740993)}},
	})
	put(t, srcPart, Item{
		"pk":   StringAttr(tenantPK("tenantA")),
		"sk":   StringAttr("WACRED#cred1"),
		"data": StringAttr(`{"credential_id":"cred1"}`),
	})
	// The global mirror repository dual-writes credentials into.
	put(t, srcPart, Item{
		"pk":   StringAttr(credentialListPK),
		"sk":   StringAttr("WACRED#cred1"),
		"data": StringAttr(`{"credential_id":"cred1"}`),
	})

	dir := t.TempDir()
	res, err := runBackup(ctx, src, dir, nil)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.ObjectCount != 7 {
		t.Errorf("backed up %d objects, want 7", res.ObjectCount)
	}
	if res.ItemCount != 6 {
		t.Errorf("backed up %d items, want 6 (4 tenant + credential + mirror)", res.ItemCount)
	}

	dst, dstPart, dstBlobs := newTestEnv(nil)
	restored, err := runRestore(ctx, dst, dir, true)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ItemsRestored != res.ItemCount || restored.ObjectsRestored != res.ObjectCount {
		t.Fatalf("restore counts %d/%d, backup had %d/%d",
			restored.ItemsRestored, restored.ObjectsRestored, res.ItemCount, res.ObjectCount)
	}

	if !reflect.DeepEqual(srcPart.items, dstPart.items) {
		t.Errorf("items did not round-trip exactly\n src: %#v\n dst: %#v", srcPart.items, dstPart.items)
	}
	if err := srcBlobs.List(ctx, "", func(info ObjectInfo) error {
		want, err := srcBlobs.store.Get(ctx, info.Key)
		if err != nil {
			return err
		}
		got, err := dstBlobs.store.Get(ctx, info.Key)
		if err != nil {
			t.Errorf("object %s missing after restore: %v", info.Key, err)
			return nil
		}
		if string(got) != string(want) {
			t.Errorf("object %s differs after restore", info.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesWhenAnObjectDoesNotMatchItsHash(t *testing.T) {
	ctx := context.Background()
	src, srcPart, srcBlobs := newTestEnv(nil)
	seedTenant(t, srcPart, srcBlobs, "tenantA")

	dir := t.TempDir()
	if _, err := runBackup(ctx, src, dir, nil); err != nil {
		t.Fatalf("backup: %v", err)
	}

	corrupt := filepath.Join(dir, backupObjectsDir, "tenants", "tenantA", "captures", "c1", "audio.webm")
	if err := os.WriteFile(corrupt, []byte("TAMPEREDWITH"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, dstPart, dstBlobs := newTestEnv(nil)
	res, err := runRestore(ctx, dst, dir, true)
	if !errors.Is(err, errManifestMismatch) {
		t.Fatalf("restore error = %v, want errManifestMismatch", err)
	}
	if len(res.Mismatches) != 1 || !strings.Contains(res.Mismatches[0], "audio.webm") {
		t.Errorf("mismatches = %v", res.Mismatches)
	}
	if dstPart.puts != 0 || dstBlobs.puts != 0 {
		t.Errorf("a refused restore wrote something: %d items, %d objects", dstPart.puts, dstBlobs.puts)
	}
}

func TestRestoreRefusesWhenTheManifestItselfWasEdited(t *testing.T) {
	ctx := context.Background()
	src, srcPart, srcBlobs := newTestEnv(nil)
	seedTenant(t, srcPart, srcBlobs, "tenantA")

	dir := t.TempDir()
	if _, err := runBackup(ctx, src, dir, nil); err != nil {
		t.Fatalf("backup: %v", err)
	}

	items := filepath.Join(dir, backupItemsName)
	body, err := os.ReadFile(items)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the last item, exactly as a truncated copy would.
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if err := os.WriteFile(items, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, dstPart, _ := newTestEnv(nil)
	res, err := runRestore(ctx, dst, dir, true)
	if !errors.Is(err, errManifestMismatch) {
		t.Fatalf("restore error = %v, want errManifestMismatch", err)
	}
	if len(res.Mismatches) == 0 || !strings.Contains(res.Mismatches[0], backupItemsName) {
		t.Errorf("mismatches = %v", res.Mismatches)
	}
	if dstPart.puts != 0 {
		t.Errorf("a refused restore wrote %d items", dstPart.puts)
	}
}

func TestRestoreRefusesABackupWithNoHeader(t *testing.T) {
	ctx := context.Background()
	dst, _, _ := newTestEnv(nil)
	if _, err := runRestore(ctx, dst, t.TempDir(), true); err == nil {
		t.Fatal("restore accepted a directory with no backup.json")
	}
}

func TestRestoreDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	src, srcPart, srcBlobs := newTestEnv(nil)
	seedTenant(t, srcPart, srcBlobs, "tenantA")

	dir := t.TempDir()
	if _, err := runBackup(ctx, src, dir, nil); err != nil {
		t.Fatalf("backup: %v", err)
	}

	dst, dstPart, dstBlobs := newTestEnv(nil)
	res, err := runRestore(ctx, dst, dir, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.ItemsPlanned == 0 || res.ObjectsPlanned == 0 {
		t.Fatalf("dry run planned nothing: %+v", res)
	}
	if res.ItemsRestored != 0 || res.ObjectsRestored != 0 {
		t.Errorf("dry run restored %d items and %d objects", res.ItemsRestored, res.ObjectsRestored)
	}
	if dstPart.puts != 0 || dstBlobs.puts != 0 || dstPart.deletes != 0 || dstBlobs.deletes != 0 {
		t.Errorf("dry run mutated the target")
	}
}
