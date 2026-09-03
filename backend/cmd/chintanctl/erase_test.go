package main

import (
	"context"
	"strings"
	"testing"
)

func TestEraseDryRunPlansEverythingAndDeletesNothing(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	seedTenant(t, part, blobs, "tenantB")

	res, err := runErase(ctx, e, "tenantA", false, "")
	if err != nil {
		t.Fatalf("erase dry run: %v", err)
	}
	if res.ItemsPlanned != 3 || res.ObjectsPlanned != 7 {
		t.Errorf("plan = %d items, %d objects; want 3 and 7", res.ItemsPlanned, res.ObjectsPlanned)
	}
	if res.ItemsDeleted != 0 || res.ObjectsDeleted != 0 {
		t.Errorf("dry run deleted something")
	}
	if part.deletes != 0 || blobs.deletes != 0 {
		t.Errorf("dry run reached the store: %d item deletes, %d object deletes", part.deletes, blobs.deletes)
	}
	for _, sk := range res.SortKeys {
		if !strings.HasPrefix(sk, "NOTE#") && !strings.HasPrefix(sk, "CAPTURE#") && sk != "SETTINGS" {
			t.Errorf("unexpected sort key in plan: %s", sk)
		}
	}
}

func TestEraseRefusesWithoutTheTypedConfirmation(t *testing.T) {
	ctx := context.Background()

	t.Run("wrong --confirm", func(t *testing.T) {
		e, part, blobs := newTestEnv(nil)
		seedTenant(t, part, blobs, "tenantA")
		if _, err := runErase(ctx, e, "tenantA", true, "tenantB"); err == nil {
			t.Fatal("erase proceeded with a mismatched --confirm")
		}
		if part.deletes != 0 || blobs.deletes != 0 {
			t.Error("a refused erase deleted something")
		}
	})

	t.Run("wrong typed answer", func(t *testing.T) {
		e, part, blobs := newTestEnv(strings.NewReader("yes\n"))
		seedTenant(t, part, blobs, "tenantA")
		if _, err := runErase(ctx, e, "tenantA", true, ""); err == nil {
			t.Fatal("erase accepted \"yes\" instead of the tenant id")
		}
		if part.deletes != 0 || blobs.deletes != 0 {
			t.Error("a refused erase deleted something")
		}
	})

	t.Run("no answer at all", func(t *testing.T) {
		e, part, blobs := newTestEnv(strings.NewReader(""))
		seedTenant(t, part, blobs, "tenantA")
		if _, err := runErase(ctx, e, "tenantA", true, ""); err == nil {
			t.Fatal("erase proceeded on an empty stdin")
		}
		if part.deletes != 0 || blobs.deletes != 0 {
			t.Error("a refused erase deleted something")
		}
	})
}

func TestEraseRemovesOneTenantAndProvesIt(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(strings.NewReader("tenantA\n"))
	seedTenant(t, part, blobs, "tenantA")
	seedTenant(t, part, blobs, "tenantB")

	res, err := runErase(ctx, e, "tenantA", true, "")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if res.ItemsDeleted != res.ItemsPlanned || res.ObjectsDeleted != res.ObjectsPlanned {
		t.Errorf("deleted %d/%d items and %d/%d objects",
			res.ItemsDeleted, res.ItemsPlanned, res.ObjectsDeleted, res.ObjectsPlanned)
	}
	if _, ok := part.items[tenantPK("tenantA")]["NOTE#n1"]; ok {
		t.Error("tenantA still has index rows")
	}
	if blobs.has("tenants/tenantA/notes/n1/note.md") {
		t.Error("tenantA still has objects")
	}
	if !blobs.has("tenants/tenantB/notes/n1/note.md") {
		t.Error("erase reached tenantB")
	}
	if len(part.items[tenantPK("tenantB")]) != 3 {
		t.Errorf("tenantB has %d index rows, want 3", len(part.items[tenantPK("tenantB")]))
	}
}

func TestEraseRejectsATenantIdThatCouldEscapeThePrefix(t *testing.T) {
	ctx := context.Background()
	e, _, _ := newTestEnv(nil)
	for _, bad := range []string{"../tenantB", "tenant/A", "tenant#A", ""} {
		if _, err := runErase(ctx, e, bad, false, ""); err == nil {
			t.Errorf("erase accepted tenant id %q", bad)
		}
	}
}
