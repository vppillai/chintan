package main

import (
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

func findingsOfKind(res *reconcileResult, kind string) []finding {
	var out []finding
	for _, f := range res.Findings {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// TestReconcileFindsOrphansInBothDirections covers the two failures the
// dual-write window and the TTL cascade actually produce: an object whose
// index row is gone, and an index row whose object is gone.
func TestReconcileFindsOrphansInBothDirections(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	// Direction one: audio left behind when TTL removed the capture row.
	blobs.seed(t, "tenants/tenantA/captures/ghost/audio.webm", "ORPHANED", "audio/webm")
	blobs.seed(t, "tenants/tenantA/notes/gone/note.md", "# gone", "text/markdown")

	// Direction two: a note row whose body was never written, or was deleted.
	put(t, part, noteItem("tenantA", model.NoteIndex{
		ID:            "n2",
		Title:         "Missing Body",
		UpdatedAt:     "2026-08-07T11:00:00.000000000Z",
		S3MarkdownKey: "tenants/tenantA/notes/n2/note.md",
		S3MetaKey:     "tenants/tenantA/notes/n2/meta.json",
	}))

	res, err := runReconcile(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	missing := findingsOfKind(res, findingMissingObject)
	if len(missing) != 2 {
		t.Errorf("missing_object findings = %d, want 2 (%+v)", len(missing), missing)
	}
	for _, f := range missing {
		if f.Repairable {
			t.Errorf("a missing object must never be repaired automatically: %+v", f)
		}
		if f.Owner != "NOTE#n2" {
			t.Errorf("missing object owner = %q", f.Owner)
		}
	}

	orphans := findingsOfKind(res, findingOrphanObject)
	if len(orphans) != 2 {
		t.Fatalf("orphan_object findings = %d, want 2 (%+v)", len(orphans), orphans)
	}
	for _, f := range orphans {
		if !f.Repairable {
			t.Errorf("orphan %s should be repairable", f.Key)
		}
	}

	// Nothing was touched: reporting is the default.
	if blobs.deletes != 0 || part.deletes != 0 || part.puts != 0 {
		t.Errorf("reconcile without --apply mutated: %d object deletes, %d item deletes",
			blobs.deletes, part.deletes)
	}
	if res.Repaired != 0 {
		t.Errorf("dry run reported %d repairs", res.Repaired)
	}
}

func TestReconcileApplyDeletesOnlyOrphanedObjects(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	blobs.seed(t, "tenants/tenantA/captures/ghost/audio.webm", "ORPHANED", "audio/webm")
	put(t, part, noteItem("tenantA", model.NoteIndex{
		ID:            "n2",
		Title:         "Missing Body",
		S3MarkdownKey: "tenants/tenantA/notes/n2/note.md",
	}))

	before := part.count()
	res, err := runReconcile(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	if res.Repaired != 1 {
		t.Fatalf("repaired = %d, want 1", res.Repaired)
	}
	if blobs.has("tenants/tenantA/captures/ghost/audio.webm") {
		t.Error("the orphaned object survived --apply")
	}
	if !blobs.has("tenants/tenantA/captures/c1/audio.webm") {
		t.Error("--apply deleted a referenced object")
	}
	if part.count() != before || part.deletes != 0 {
		t.Errorf("--apply deleted index rows: %d -> %d", before, part.count())
	}
}

func TestReconcileReportsNewArtifactsWithoutDeletingThem(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	// The shape a schema addition takes: a real capture gains an artifact no
	// attribute names yet. Deleting it would be data loss, so it is reported.
	blobs.seed(t, "tenants/tenantA/captures/c1/embeddings.bin", "\x01\x02", "application/octet-stream")
	blobs.seed(t, "tenants/tenantA/somewhere/else.txt", "x", "text/plain")

	res, err := runReconcile(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := findingsOfKind(res, findingUnreferencedObject); len(got) != 1 {
		t.Errorf("unreferenced_object findings = %d, want 1 (%+v)", len(got), got)
	}
	if got := findingsOfKind(res, findingUnknownObject); len(got) != 1 {
		t.Errorf("unknown_object findings = %d, want 1 (%+v)", len(got), got)
	}
	if !blobs.has("tenants/tenantA/captures/c1/embeddings.bin") ||
		!blobs.has("tenants/tenantA/somewhere/else.txt") {
		t.Error("--apply deleted an object it does not understand")
	}
}

func TestReconcileReportsStuckCaptures(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	put(t, part, captureItem(model.CaptureIndex{
		ID: "c2", NoteID: "n1", UserID: "tenantA",
		Status: model.StatusUploaded, CreatedAt: "2026-08-07T09:00:00.000000000Z",
	}))

	res, err := runReconcile(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stuck := findingsOfKind(res, findingStuckCapture)
	if len(stuck) != 1 || stuck[0].SK != "CAPTURE#c2" {
		t.Errorf("stuck_capture findings = %+v", stuck)
	}
}

func TestReconcileIsCleanOnAConsistentInstance(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	res, err := runReconcile(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("a consistent instance produced findings: %+v", res.Findings)
	}
}

// seedDanglingCapture lays down a capture filed into a note that has no row —
// the shape August's pre-cascade note deletion left behind — with every
// artifact the pipeline writes plus one no attribute names.
func seedDanglingCapture(t *testing.T, part *fakePartition, blobs *fakeBlobs, tenantID, captureID string) []string {
	t.Helper()
	base := "tenants/" + tenantID + "/captures/" + captureID
	put(t, part, captureItem(model.CaptureIndex{
		ID: captureID, NoteID: "note_deleted_in_august", UserID: tenantID,
		Status:    model.StatusAppended,
		AudioKey:  base + "/audio.webm",
		RawKey:    base + "/raw.txt",
		CleanKey:  base + "/clean.txt",
		CreatedAt: "2026-08-08T09:00:00.000000000Z",
	}))
	keys := []string{base + "/audio.webm", base + "/raw.txt", base + "/clean.txt", base + "/peaks.json"}
	for _, k := range keys {
		blobs.seed(t, k, "x", "application/octet-stream")
	}
	return keys
}

func TestReconcileReportsCapturesWhoseNoteIsGone(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	keys := seedDanglingCapture(t, part, blobs, "tenantA", "c_dangling")

	// Not dangling: a capture that has not been routed yet has no note to be
	// missing, and one whose note exists is the normal case.
	put(t, part, captureItem(model.CaptureIndex{
		ID: "c_unrouted", UserID: "tenantA", Status: model.StatusNeedsTarget,
		CreatedAt: "2026-08-08T10:00:00.000000000Z",
	}))

	res, err := runReconcile(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	dangling := findingsOfKind(res, findingDanglingCapture)
	if len(dangling) != 1 {
		t.Fatalf("dangling_capture findings = %+v, want exactly one", dangling)
	}
	f := dangling[0]
	if f.SK != "CAPTURE#c_dangling" || f.Owner != "NOTE#note_deleted_in_august" || !f.Repairable {
		t.Errorf("finding = %+v", f)
	}
	if len(f.Objects) != len(keys) {
		t.Errorf("finding names %d objects %v, want all %d under the capture", len(f.Objects), f.Objects, len(keys))
	}

	// Its objects are accounted for by this finding alone: not orphaned (the
	// row exists), not unreferenced (they go with the row), not missing.
	for _, kind := range []string{findingOrphanObject, findingUnreferencedObject, findingMissingObject} {
		if got := findingsOfKind(res, kind); len(got) != 0 {
			t.Errorf("%s findings = %+v, want none for a dangling capture's objects", kind, got)
		}
	}
	if blobs.deletes != 0 || part.deletes != 0 {
		t.Errorf("dry run mutated: %d object deletes, %d item deletes", blobs.deletes, part.deletes)
	}
	if !strings.Contains(res.repairPlan(), "1 dangling capture row(s) with 4 object(s)") {
		t.Errorf("repair plan = %q", res.repairPlan())
	}
}

func TestReconcileApplyDeletesDanglingCaptureRowAndObjects(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	keys := seedDanglingCapture(t, part, blobs, "tenantA", "c_dangling")

	before := part.count()
	res, err := runReconcile(ctx, e, nil, true)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	if res.Repaired != 1 {
		t.Fatalf("repaired = %d, want 1", res.Repaired)
	}
	for _, k := range keys {
		if blobs.has(k) {
			t.Errorf("%s survived --apply", k)
		}
	}
	if part.count() != before-1 || part.deletes != 1 {
		t.Errorf("index rows %d -> %d (%d deletes), want exactly the dangling row gone", before, part.count(), part.deletes)
	}
	// The healthy capture and its note are untouched.
	if !blobs.has("tenants/tenantA/captures/c1/audio.webm") || !blobs.has("tenants/tenantA/notes/n1/note.md") {
		t.Error("--apply deleted an object that belongs to a live note")
	}

	again, err := runReconcile(ctx, e, nil, false)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(again.Findings) != 0 {
		t.Errorf("findings after repair = %+v, want none", again.Findings)
	}
}
