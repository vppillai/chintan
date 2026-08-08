package main

import (
	"context"
	"strconv"
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

	// A credential, and the global mirror row repository dual-writes with it.
	put(t, part, Item{
		"pk": StringAttr(tenantPK("tenantA")), "sk": StringAttr("WACRED#cred1"),
		"data": StringAttr(`{"credential_id":"cred1"}`),
	})
	put(t, part, Item{
		"pk": StringAttr(credentialListPK), "sk": StringAttr("WACRED#cred1"),
		"data": StringAttr(`{"credential_id":"cred1"}`),
	})
	put(t, part, Item{
		"pk": StringAttr(credentialListPK), "sk": StringAttr("WACRED#other"),
		"data": StringAttr(`{"credential_id":"other"}`),
	})

	res, err := runErase(ctx, e, "tenantA", true, "")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if res.ItemsDeleted != res.ItemsPlanned || res.ObjectsDeleted != res.ObjectsPlanned {
		t.Errorf("deleted %d/%d items and %d/%d objects",
			res.ItemsDeleted, res.ItemsPlanned, res.ObjectsDeleted, res.ObjectsPlanned)
	}
	if len(res.MirrorSortKeys) != 1 || res.MirrorSortKeys[0] != "WACRED#cred1" {
		t.Errorf("mirror rows = %v, want the tenant's credential only", res.MirrorSortKeys)
	}

	if _, ok := part.items[tenantPK("tenantA")]["NOTE#n1"]; ok {
		t.Error("tenantA still has index rows")
	}
	if _, ok := part.items[credentialListPK]["WACRED#cred1"]; ok {
		t.Error("the global credential mirror survived the erase")
	}
	if _, ok := part.items[credentialListPK]["WACRED#other"]; !ok {
		t.Error("erase deleted another tenant's credential mirror")
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

func TestUsageAggregatesMeteringRecords(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	usageItem := func(sk, provider, mdl, op, unit string, quantity float64, micros int64) Item {
		return Item{
			"pk":          StringAttr(tenantPK("tenantA")),
			"sk":          StringAttr(sk),
			"type":        StringAttr("usage"),
			"provider":    StringAttr(provider),
			"model":       StringAttr(mdl),
			"op":          StringAttr(op),
			"unit":        StringAttr(unit),
			"quantity":    AttrValue{N: ptr(formatFloat(quantity))},
			"cost_micros": NumberAttr(micros),
		}
	}
	put(t, part, usageItem("USAGE#2026-08-05#a", "groq", "whisper-large-v3", "transcribe", "audio_seconds", 60, 120))
	put(t, part, usageItem("USAGE#2026-08-07#b", "groq", "whisper-large-v3", "transcribe", "audio_seconds", 30, 60))
	put(t, part, usageItem("USAGE#2026-08-07#c", "openai", "gpt", "cleanup", "input_tokens", 1200, 1200))

	all, err := runUsage(ctx, e, nil, "")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if all.Records != 3 || len(all.Rows) != 2 {
		t.Fatalf("records=%d rows=%d, want 3 and 2", all.Records, len(all.Rows))
	}
	if all.CostMicros != 1380 {
		t.Errorf("total cost = %d micros, want 1380", all.CostMicros)
	}
	if all.Rows[0].Provider != "groq" || all.Rows[0].Calls != 2 || all.Rows[0].Quantity != 90 {
		t.Errorf("groq row = %+v", all.Rows[0])
	}

	since, err := runUsage(ctx, e, nil, "2026-08-07")
	if err != nil {
		t.Fatalf("usage --since: %v", err)
	}
	if since.Records != 2 || since.CostMicros != 1260 {
		t.Errorf("--since 2026-08-07: records=%d cost=%d, want 2 and 1260", since.Records, since.CostMicros)
	}
}

func ptr[T any](v T) *T { return &v }

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
