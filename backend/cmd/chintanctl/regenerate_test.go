package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/pipeline"
)

// seedRegenerateTenant lays down a tenant with four notes: a plain note that
// keeps a cleaned view, with two appended recordings and one that failed, a
// checklist with one, a verbatim note with one, and an archived note with
// one; every appended recording has a raw transcript in the bucket, and the
// plain note a body.
func seedRegenerateTenant(t *testing.T, part *fakePartition, blobs *fakeBlobs, tenant string) {
	t.Helper()
	blobs.seed(t, "tenants/"+tenant+"/notes/plain/note.md", strings.Repeat("body ", 200), "text/markdown")
	notes := []model.NoteIndex{
		{ID: "plain", Title: "Roof", UpdatedAt: "2026-01-01T09:00:00.000000000Z", S3MarkdownKey: "tenants/" + tenant + "/notes/plain/note.md", S3MetaKey: "tenants/" + tenant + "/notes/plain/meta.json", CleanedBody: "# Roof"},
		{ID: "list", Title: "Shopping", Kind: model.NoteKindChecklist, UpdatedAt: "2026-01-01T09:00:00.000000000Z", S3MarkdownKey: "tenants/" + tenant + "/notes/list/note.md", S3MetaKey: "tenants/" + tenant + "/notes/list/meta.json"},
		{ID: "verbatim", Title: "Quote", Verbatim: true, UpdatedAt: "2026-01-01T09:00:00.000000000Z", S3MarkdownKey: "tenants/" + tenant + "/notes/verbatim/note.md", S3MetaKey: "tenants/" + tenant + "/notes/verbatim/meta.json"},
		{ID: "gone", Title: "Archived", DeletedAt: "2026-01-02T09:00:00.000000000Z", UpdatedAt: "2026-01-01T09:00:00.000000000Z", S3MarkdownKey: "tenants/" + tenant + "/notes/gone/note.md", S3MetaKey: "tenants/" + tenant + "/notes/gone/meta.json"},
	}
	for _, n := range notes {
		put(t, part, noteItem(tenant, n))
	}
	captures := []struct {
		id, note string
		status   model.CaptureStatus
		raw      string
	}{
		{"c1", "plain", model.StatusAppended, strings.Repeat("word ", 80)},
		{"c2", "plain", model.StatusAppended, strings.Repeat("word ", 40)},
		{"c3", "plain", model.StatusFailed, ""},
		{"c4", "list", model.StatusAppended, "eggs and milk"},
		{"c5", "verbatim", model.StatusAppended, "as spoken"},
		{"c6", "gone", model.StatusAppended, "archived words"},
	}
	for _, c := range captures {
		capture := model.CaptureIndex{ID: c.id, UserID: tenant, NoteID: c.note, Status: c.status, CreatedAt: "2026-01-01T09:00:00.000000000Z"}
		if c.raw != "" {
			capture.RawKey = "tenants/" + tenant + "/captures/" + c.id + "/raw.txt"
			blobs.seed(t, capture.RawKey, c.raw, "text/plain")
		}
		put(t, part, captureItem(capture))
	}
}

// The dry run: every active, non-verbatim note with an appended recording
// that has a transcript, priced per recording as the worker reserves for
// the cleanup call, and nothing queued.
func TestRegeneratePlansFromTheRowsAndPricesFromTheTable(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedRegenerateTenant(t, part, blobs, "tenantA")
	worker := &fakeWorker{}
	e.Worker = worker

	res, err := runRegenerate(ctx, e, []string{"tenantA"}, regenerateOptions{all: true, model: "MiniMax-M3"})
	if err != nil {
		t.Fatalf("runRegenerate: %v", err)
	}
	if res.Notes != 2 || res.Captures != 3 {
		t.Fatalf("plan = %d notes, %d recordings; want the plain note's two and the list's one (%+v)", res.Notes, res.Captures, res.Tenants)
	}
	byID := map[string]regenerateNote{}
	for _, n := range res.Tenants[0].Notes {
		byID[n.NoteID] = n
	}
	if byID["plain"].Captures != 2 || byID["list"].Captures != 1 || byID["list"].Kind != "checklist" {
		t.Errorf("notes = %+v", byID)
	}
	// c1's transcript is 400 bytes: 101 tokens in, 101 out, at MiniMax's
	// $0.30 / $1.20 per million; c2's is 200; the 1,000-byte body is the
	// whole-note call the note's cleaned view costs on top.
	perBytes := func(n int64) int64 {
		tokens := float64(n)/4 + 1
		return meter.DefaultPrices.Cost("openai", "MiniMax-M3", meter.Quantities{meter.UnitInputTokens: tokens, meter.UnitOutputTokens: tokens})
	}
	if want := perBytes(400) + perBytes(200) + perBytes(1000); want == 0 || byID["plain"].CostMicros != want || byID["plain"].TranscriptBytes != 600 || !byID["plain"].CleanedView || byID["plain"].BodyBytes != 1000 {
		t.Errorf("plain note = %+v; want two recordings over 600 bytes and the whole-note call over 1000, %d in all", byID["plain"], want)
	}
	if byID["list"].CleanedView || byID["list"].CostMicros != perBytes(13) {
		t.Errorf("list = %+v; want one recording's call and no whole-note call", byID["list"])
	}
	if len(worker.payloads) != 0 {
		t.Errorf("a dry run queued %v", worker.payloads)
	}

	// One note by id; an archived one is refused.
	one, err := runRegenerate(ctx, e, []string{"tenantA"}, regenerateOptions{noteID: "list", model: "MiniMax-M3"})
	if err != nil || one.Notes != 1 || one.Captures != 1 {
		t.Errorf("--note list = %+v, %v", one, err)
	}
	if _, err := runRegenerate(ctx, e, []string{"tenantA"}, regenerateOptions{noteID: "gone", model: "MiniMax-M3"}); err == nil {
		t.Error("an archived note was planned")
	}
}

// --apply queues one task per note with something to regenerate, naming the
// tenant and the note and no capture ids, after the operator types yes; the
// task name is the worker's.
func TestRegenerateQueuesTheWorkersTask(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(strings.NewReader("yes\n"))
	seedRegenerateTenant(t, part, blobs, "tenantA")
	worker := &fakeWorker{}
	e.Worker = worker

	res, err := runRegenerate(ctx, e, []string{"tenantA"}, regenerateOptions{all: true, model: "MiniMax-M3", apply: true})
	if err != nil {
		t.Fatalf("runRegenerate --apply: %v", err)
	}
	if len(worker.payloads) != 2 {
		t.Fatalf("payloads = %v, want one per note", worker.payloads)
	}
	for _, raw := range worker.payloads {
		var inv pipeline.Invocation
		if err := json.Unmarshal([]byte(raw), &inv); err != nil {
			t.Fatal(err)
		}
		if inv.Task != pipeline.TaskRegenerateNote || inv.TenantID != "tenantA" || inv.NoteID == "" || len(inv.CaptureIDs) != 0 {
			t.Errorf("payload %s does not address the worker's task with no ids", raw)
		}
	}
	for _, n := range res.Tenants[0].Notes {
		if !n.Queued {
			t.Errorf("note %s not marked queued", n.NoteID)
		}
	}

	// Without the word, nothing is queued.
	e2, part2, blobs2 := newTestEnv(strings.NewReader("no\n"))
	seedRegenerateTenant(t, part2, blobs2, "tenantA")
	worker2 := &fakeWorker{}
	e2.Worker = worker2
	if _, err := runRegenerate(ctx, e2, []string{"tenantA"}, regenerateOptions{all: true, model: "MiniMax-M3", apply: true}); err == nil {
		t.Error("runRegenerate --apply proceeded without confirmation")
	}
	if len(worker2.payloads) != 0 {
		t.Errorf("a refused confirmation queued %v", worker2.payloads)
	}
	// --yes skips the prompt.
	if _, err := runRegenerate(ctx, e2, []string{"tenantA"}, regenerateOptions{all: true, model: "MiniMax-M3", apply: true, yes: true}); err != nil || len(worker2.payloads) != 2 {
		t.Errorf("--yes: %v, payloads %v", err, worker2.payloads)
	}
}
