package main

import (
	"context"
	"encoding/json"
	"io"
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

	res, err := runReconcile(ctx, e, nil, false, nil)
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
	res, err := runReconcile(ctx, e, nil, true, nil)
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

	res, err := runReconcile(ctx, e, nil, true, nil)
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

	res, err := runReconcile(ctx, e, nil, false, nil)
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

	res, err := runReconcile(ctx, e, nil, false, nil)
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

	res, err := runReconcile(ctx, e, nil, false, nil)
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
	res, err := runReconcile(ctx, e, nil, true, nil)
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

	again, err := runReconcile(ctx, e, nil, false, nil)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(again.Findings) != 0 {
		t.Errorf("findings after repair = %+v, want none", again.Findings)
	}
}

// legacyItem is a row as the August 2026 code wrote it: key, type and the
// record blob, with nothing promoted — and so, for a capture, no GSI1 keys.
func legacyItem(tenantID, sk, typ string, record any) Item {
	blob, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	return Item{
		"pk":   StringAttr(tenantPK(tenantID)),
		"sk":   StringAttr(sk),
		"type": StringAttr(typ),
		"data": StringAttr(string(blob)),
	}
}

func scanOne(t *testing.T, part *fakePartition, tenantID, sk string) Item {
	t.Helper()
	var row Item
	if err := part.Scan(context.Background(), tenantPK(tenantID), sk, func(it Item) error { row = it; return nil }); err != nil {
		t.Fatalf("scan %s: %v", sk, err)
	}
	if row == nil {
		t.Fatalf("no row %s", sk)
	}
	return row
}

// TestReconcileFindsALegacyCaptureWhoseNoteIsGone is the production shape of
// 2026-09-05: thirteen captures written in August, each a blob-only row, each
// filed into a note that "delete forever" removed without seeing them. The
// note_id is read off the blob, the row is dangling, and --apply removes it
// with its objects.
func TestReconcileFindsALegacyCaptureWhoseNoteIsGone(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	audio := "tenants/tenantA/captures/c_aug/audio.webm"
	put(t, part, legacyItem("tenantA", "CAPTURE#c_aug", "capture", model.CaptureIndex{
		ID: "c_aug", NoteID: "note_purged", UserID: "tenantA", Status: model.StatusAppended,
		AudioKey: audio, CreatedAt: "2026-08-07T09:00:00Z",
	}))
	blobs.seed(t, audio, "OPUS", "audio/webm")

	res, err := runReconcile(ctx, e, nil, false, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	dangling := findingsOfKind(res, findingDanglingCapture)
	if len(dangling) != 1 || dangling[0].SK != "CAPTURE#c_aug" || dangling[0].Owner != "NOTE#note_purged" {
		t.Fatalf("dangling_capture findings = %+v, want the legacy row", dangling)
	}
	if got := findingsOfKind(res, findingUnindexedCapture); len(got) != 0 {
		t.Errorf("a dangling row is deleted, not re-indexed; unindexed_capture findings = %+v", got)
	}

	res, err = runReconcile(ctx, e, nil, true, nil)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	if res.Repaired != 1 || blobs.has(audio) {
		t.Errorf("repaired = %d, audio present = %v; want the row and its audio gone", res.Repaired, blobs.has(audio))
	}
	if _, ok, _ := part.Get(ctx, tenantPK("tenantA"), "CAPTURE#c_aug"); ok {
		t.Error("the legacy capture row survived --apply")
	}
}

// TestReconcileRePromotesALegacyNoteRow: a blob-only NOTE row is listed only
// through a second read and ordered by whatever the blob says. --apply writes
// the promoted attributes the store would have written, and nothing else.
func TestReconcileRePromotesALegacyNoteRow(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	put(t, part, legacyItem("tenantA", "NOTE#n_aug", "note", model.NoteIndex{
		ID: "n_aug", Title: "Written in August", Aliases: []string{"aug"}, Tags: []string{"old"},
		CreatedAt:     "2026-08-07T10:00:00Z",
		S3MarkdownKey: "tenants/tenantA/notes/n_aug/note.md",
		S3MetaKey:     "tenants/tenantA/notes/n_aug/meta.json",
		Version:       2,
	}))
	blobs.seed(t, "tenants/tenantA/notes/n_aug/note.md", "# August", "text/markdown")
	blobs.seed(t, "tenants/tenantA/notes/n_aug/meta.json", `{}`, "application/json")
	before := scanOne(t, part, "tenantA", "NOTE#n_aug")

	res, err := runReconcile(ctx, e, nil, false, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	unlisted := findingsOfKind(res, findingUnlistedNote)
	if len(unlisted) != 1 || unlisted[0].SK != "NOTE#n_aug" || !unlisted[0].Repairable {
		t.Fatalf("unlisted_note findings = %+v", unlisted)
	}
	if !strings.Contains(unlisted[0].Detail, "sorts last") {
		t.Errorf("detail = %q, want it to say what the missing updated_at does to the list", unlisted[0].Detail)
	}
	// The modern note seeded beside it is not a finding.
	for _, f := range res.Findings {
		if f.SK == "NOTE#n1" {
			t.Errorf("a promoted row was reported: %+v", f)
		}
	}
	if part.updates != 0 {
		t.Fatalf("dry run wrote %d update(s)", part.updates)
	}

	res, err = runReconcile(ctx, e, nil, true, nil)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	if res.Repaired != 1 || part.updates != 1 {
		t.Fatalf("repaired = %d, updates = %d; want one conditional SET", res.Repaired, part.updates)
	}
	after := scanOne(t, part, "tenantA", "NOTE#n_aug")
	// The attributes the list projects and filters on, spelled as the store
	// spells them.
	for name, want := range map[string]string{
		"note_id": "n_aug", "title": "Written in August", "deleted_at": "", "created_at": "2026-08-07T10:00:00Z",
		"s3_markdown_key": "tenants/tenantA/notes/n_aug/note.md", "type": "note",
	} {
		if got := after.Str(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if after.Num("version") != 2 {
		t.Errorf("version = %d, want the blob's 2", after.Num("version"))
	}
	if after.Str("data") != before.Str("data") {
		t.Error("the record blob was rewritten; the repair must only promote")
	}
	if _, ok := after["search_text"]; ok {
		t.Error("search_text was invented for a row that had none")
	}

	// Idempotent: the row is promoted now, so a second run finds nothing.
	res, err = runReconcile(ctx, e, nil, false, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := findingsOfKind(res, findingUnlistedNote); len(got) != 0 {
		t.Errorf("after the repair, unlisted_note findings = %+v", got)
	}
}

// TestReconcileRePromotesALegacyCaptureRow: a blob-only CAPTURE row of a note
// that still exists is invisible to every read that goes through the note.
// --apply gives it the GSI1 keys and the promoted attributes the store writes.
func TestReconcileRePromotesALegacyCaptureRow(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	audio := "tenants/tenantA/captures/c_aug/audio.webm"
	put(t, part, legacyItem("tenantA", "CAPTURE#c_aug", "capture", model.CaptureIndex{
		ID: "c_aug", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
		AudioKey: audio, CreatedAt: "2026-08-07T09:30:00Z", Version: 1,
	}))
	blobs.seed(t, audio, "OPUS", "audio/webm")

	res, err := runReconcile(ctx, e, nil, true, nil)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	found := findingsOfKind(res, findingUnindexedCapture)
	if len(found) != 1 || found[0].SK != "CAPTURE#c_aug" || found[0].Owner != "NOTE#n1" || !found[0].Repaired {
		t.Fatalf("unindexed_capture findings = %+v", found)
	}
	if got := findingsOfKind(res, findingDanglingCapture); len(got) != 0 {
		t.Errorf("its note exists; dangling_capture findings = %+v", got)
	}
	after := scanOne(t, part, "tenantA", "CAPTURE#c_aug")
	for name, want := range map[string]string{
		"gsi1pk": "TENANT#tenantA#NOTE#n1", "gsi1sk": "CAPTURE#2026-08-07T09:30:00Z",
		"capture_id": "c_aug", "note_id": "n1", "status": "appended", "audio_key": audio,
	} {
		if got := after.Str(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if blobs.has(audio) != true {
		t.Error("re-promoting a capture must not touch its objects")
	}
}

// TestReconcileReportsCapturesWhoseSizeIsUnknown: the worker has stamped
// audio_bytes since 2026-09-05; every capture before it reads zero and the
// storage summary added them up to "0.0 MB". The size is in the listing the
// command already walks, so the finding carries it and --apply can write it.
func TestReconcileReportsCapturesWhoseSizeIsUnknown(t *testing.T) {
	base := "tenants/tenantA/captures/"
	cases := []struct {
		name           string
		capture        model.CaptureIndex
		audio          string // object body, or "" for no object
		wantFinding    bool
		wantRepairable bool
		wantBytes      int64
	}{
		{
			name: "a size the row already records is not a finding",
			capture: model.CaptureIndex{ID: "c_sized", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
				AudioKey: base + "c_sized/audio.webm", AudioBytes: 4, CreatedAt: "2026-09-05T09:00:00.000000000Z"},
			audio: "OPUS",
		},
		{
			name: "a row that records no size, whose audio is in the bucket",
			capture: model.CaptureIndex{ID: "c_old", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
				AudioKey: base + "c_old/audio.webm", CreatedAt: "2026-08-07T09:00:00.000000000Z"},
			audio:          "OPUSOPUSOPUSOPUS",
			wantFinding:    true,
			wantRepairable: true,
			wantBytes:      16,
		},
		{
			name: "a row that records no size, whose audio is gone: reported, not repaired",
			capture: model.CaptureIndex{ID: "c_lost", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
				AudioKey: base + "c_lost/audio.webm", CreatedAt: "2026-08-07T09:00:00.000000000Z"},
			wantFinding: true,
		},
		{
			name: "a row that records no size, whose audio is empty: reported, not repaired",
			capture: model.CaptureIndex{ID: "c_empty", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
				AudioKey: base + "c_empty/audio.webm", CreatedAt: "2026-08-07T09:00:00.000000000Z"},
			audio:       "\x00",
			wantFinding: true,
		},
		{
			name: "a row with no audio key has no size to know",
			capture: model.CaptureIndex{ID: "c_noaudio", NoteID: "n1", UserID: "tenantA", Status: model.StatusFailed,
				CreatedAt: "2026-08-07T09:00:00.000000000Z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			e, part, blobs := newTestEnv(nil)
			seedTenant(t, part, blobs, "tenantA")
			put(t, part, captureItem(tc.capture))
			if tc.audio != "" {
				body := tc.audio
				if body == "\x00" {
					body = ""
				}
				blobs.seed(t, tc.capture.AudioKey, body, "audio/webm")
			}

			res, err := runReconcile(ctx, e, nil, false, nil)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			found := findingsOfKind(res, findingCaptureSizeUnknown)
			if !tc.wantFinding {
				if len(found) != 0 {
					t.Fatalf("capture_size_unknown findings = %+v, want none", found)
				}
				return
			}
			if len(found) != 1 || found[0].SK != "CAPTURE#"+tc.capture.ID || found[0].Owner != tc.capture.AudioKey {
				t.Fatalf("capture_size_unknown findings = %+v, want one for %s", found, tc.capture.ID)
			}
			if found[0].Repairable != tc.wantRepairable || found[0].Bytes != tc.wantBytes {
				t.Errorf("finding = %+v, want repairable %v with %d bytes", found[0], tc.wantRepairable, tc.wantBytes)
			}
		})
	}
}

// --apply writes the size into the record blob, where audio_bytes lives, and
// nothing else: the version the API compares, the promoted attributes and a
// blob field this build has never heard of all survive.
func TestReconcileApplyRecordsTheAudioSizeFromTheBucket(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	audio := "tenants/tenantA/captures/c_old/audio.webm"
	row := captureItem(model.CaptureIndex{ID: "c_old", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
		AudioKey: audio, CreatedAt: "2026-08-07T09:00:00.000000000Z", Version: 3, AppendedAt: 1754557200})
	// A field from a newer or older build, and a big integer, both in the blob.
	row["data"] = StringAttr(strings.TrimSuffix(row.Str("data"), "}") + `,"future_field":{"x":1},"big":9007199254740993}`)
	put(t, part, row)
	blobs.seed(t, audio, "OPUSOPUSOPUSOPUS", "audio/webm")

	res, err := runReconcile(ctx, e, nil, true, nil)
	if err != nil {
		t.Fatalf("reconcile --apply: %v", err)
	}
	found := findingsOfKind(res, findingCaptureSizeUnknown)
	if len(found) != 1 || !found[0].Repaired {
		t.Fatalf("findings = %+v, want the one, repaired", found)
	}
	after := scanOne(t, part, "tenantA", "CAPTURE#c_old")
	c, err := captureFromItem(after)
	if err != nil {
		t.Fatalf("decode repaired row: %v", err)
	}
	if c.AudioBytes != 16 || c.AppendedAt != 1754557200 || c.AudioKey != audio {
		t.Errorf("repaired capture = %+v, want audio_bytes 16 and everything else as it was", c)
	}
	if after.Num("version") != 3 {
		t.Errorf("version = %d, want 3 (unchanged)", after.Num("version"))
	}
	for _, want := range []string{`"future_field":{"x":1}`, `"big":9007199254740993`, `"audio_bytes":16`} {
		if !strings.Contains(after.Str("data"), want) {
			t.Errorf("blob lacks %s: %s", want, after.Str("data"))
		}
	}

	// Recorded now, so the next run has nothing to say.
	res, err = runReconcile(ctx, e, nil, false, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := findingsOfKind(res, findingCaptureSizeUnknown); len(got) != 0 {
		t.Errorf("after the repair, findings = %+v", got)
	}
}

// --only narrows --apply to the named kinds: the rest are still reported, as
// not repairable in this run, and are not touched.
func TestReconcileOnlyRestrictsWhatApplyRepairs(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")
	seedDanglingCapture(t, part, blobs, "tenantA", "c_dangling")
	unsized := "tenants/tenantA/captures/c_old/audio.webm"
	put(t, part, captureItem(model.CaptureIndex{ID: "c_old", NoteID: "n1", UserID: "tenantA", Status: model.StatusAppended,
		AudioKey: unsized, CreatedAt: "2026-08-07T09:00:00.000000000Z"}))
	blobs.seed(t, unsized, "OPUS", "audio/webm")

	res, err := runReconcile(ctx, e, nil, true, []string{findingDanglingCapture})
	if err != nil {
		t.Fatalf("reconcile --apply --only dangling_capture: %v", err)
	}
	if res.Repaired != 1 {
		t.Errorf("repaired = %d, want only the dangling capture", res.Repaired)
	}
	if _, ok, _ := part.Get(ctx, tenantPK("tenantA"), "CAPTURE#c_dangling"); ok {
		t.Error("the dangling capture survived")
	}
	sizes := findingsOfKind(res, findingCaptureSizeUnknown)
	if len(sizes) != 1 || sizes[0].Repairable || sizes[0].Repaired {
		t.Errorf("capture_size_unknown = %+v, want reported and left alone", sizes)
	}
	if !strings.Contains(res.repairPlan(), "1 dangling capture row(s)") || strings.Contains(res.repairPlan(), "audio size") {
		t.Errorf("repair plan = %q, want only the dangling repair announced", res.repairPlan())
	}
	if c, _ := captureFromItem(scanOne(t, part, "tenantA", "CAPTURE#c_old")); c.AudioBytes != 0 {
		t.Errorf("--only dangling_capture recorded a size: %+v", c)
	}

	// An unknown kind is refused before anything is dialled.
	err = run(ctx, []string{"reconcile", "--instance", "dev", "--apply", "--only", "everything"}, io.Discard, io.Discard, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "not a repairable finding") {
		t.Errorf("--only everything: err = %v", err)
	}
}
