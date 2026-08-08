package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readExported(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func TestExportLayoutIsObsidianFriendly(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	out := t.TempDir()
	res, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(res.Tenants) != 1 || res.Tenants[0].Notes != 1 || res.Tenants[0].Captures != 1 {
		t.Fatalf("unexpected summary: %+v", res.Tenants)
	}

	md := readExported(t, out, "tenantA/notes/Kitchen-Rebuild-Plan-n1.md")
	front, body, ok := strings.Cut(md, "---\n\n")
	if !ok {
		t.Fatalf("note has no front matter block:\n%s", md)
	}
	for _, want := range []string{
		`id: "n1"`,
		`title: "Kitchen: Rebuild Plan"`,
		`  - "kitchen"`,
		`  - "house"`,
		`updated: "2026-08-07T10:00:00.000000000Z"`,
		`version: 3`,
		`  - id: "c1"`,
		`    duration_ms: 42000`,
		`    path: "attachments/captures/c1"`,
	} {
		if !strings.Contains(front, want) {
			t.Errorf("front matter missing %q:\n%s", want, front)
		}
	}
	if body != "# Kitchen\n\nCounter depth is 24 inches.\n" {
		t.Errorf("note body was rewritten: %q", body)
	}

	// Every per-capture artifact named in the design lands beside the note.
	for _, rel := range []string{
		"tenantA/attachments/captures/c1/audio.webm",
		"tenantA/attachments/captures/c1/raw.txt",
		"tenantA/attachments/captures/c1/clean.txt",
		"tenantA/attachments/captures/c1/segments.json",
		"tenantA/attachments/captures/c1/peaks.json",
		"tenantA/attachments/notes/n1/meta.json",
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing exported artifact %s: %v", rel, err)
		}
	}

	// A settings row has no markdown rendering, and is still exported.
	if got := readExported(t, out, "tenantA/_items/SETTINGS.json"); !strings.Contains(got, "faithful") {
		t.Errorf("settings item not exported verbatim: %s", got)
	}
}

// TestExportCapturesUnknownKinds is the regression test for the property the
// whole command is built around: enumeration is by prefix and partition, so a
// kind added after this build still lands in the export.
func TestExportCapturesUnknownKinds(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	// A whole object group nobody has written a renderer for.
	blobs.seed(t, "tenants/tenantA/attachments/att1/scan.pdf", "%PDF-1.7 fake", "application/pdf")
	// A new artifact inside a group that IS known.
	blobs.seed(t, "tenants/tenantA/captures/c1/embeddings.bin", "\x00\x01\x02", "application/octet-stream")
	// A key that fits no shape at all.
	blobs.seed(t, "tenants/tenantA/loose-file.txt", "loose", "text/plain")
	// And an index item of a kind this build has never heard of.
	put(t, part, Item{
		"pk":     StringAttr(tenantPK("tenantA")),
		"sk":     StringAttr("REMINDER#r1"),
		"type":   StringAttr("reminder"),
		"data":   StringAttr(`{"id":"r1","due":"2026-09-01"}`),
		"due_at": NumberAttr(1788220800),
	})

	out := t.TempDir()
	res, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if got := readExported(t, out, "tenantA/_raw/attachments/att1/scan.pdf"); got != "%PDF-1.7 fake" {
		t.Errorf("unknown object group not exported verbatim: %q", got)
	}
	if got := readExported(t, out, "tenantA/_raw/loose-file.txt"); got != "loose" {
		t.Errorf("unshaped key not exported verbatim: %q", got)
	}
	if got := readExported(t, out, "tenantA/attachments/captures/c1/embeddings.bin"); got != "\x00\x01\x02" {
		t.Errorf("new per-capture artifact not exported: %q", got)
	}
	if got := readExported(t, out, "tenantA/_items/REMINDER#r1.json"); !strings.Contains(got, "2026-09-01") {
		t.Errorf("unknown index kind not exported: %s", got)
	}
	if res.Tenants[0].UnknownObjects != 2 {
		t.Errorf("unknown object count = %d, want 2", res.Tenants[0].UnknownObjects)
	}
	if res.Tenants[0].UnrenderedItems != 2 { // SETTINGS and REMINDER#r1
		t.Errorf("unrendered item count = %d, want 2", res.Tenants[0].UnrenderedItems)
	}
}

func TestExportSkipsUnchangedObjectsOnRerun(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	out := t.TempDir()
	first, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if first.ObjectsSkipped != 0 {
		t.Fatalf("first export skipped %d objects", first.ObjectsSkipped)
	}
	opensAfterFirst := blobs.opens

	second, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if second.ObjectsSkipped != 7 {
		t.Errorf("second export skipped %d objects, want all 7", second.ObjectsSkipped)
	}
	if second.ObjectsCopied != 0 || second.BytesCopied != 0 {
		t.Errorf("second export re-downloaded: copied=%d bytes=%d", second.ObjectsCopied, second.BytesCopied)
	}
	if blobs.opens != opensAfterFirst {
		t.Errorf("second export opened %d more objects", blobs.opens-opensAfterFirst)
	}

	// A title change alters only the generated front matter, and must still
	// force the note to be rewritten.
	blobs.seed(t, "tenants/tenantA/notes/n1/note.md", "# Kitchen\n\nCounter depth is 24 inches.\n", "text/markdown")
	third, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("third export: %v", err)
	}
	if third.ObjectsSkipped != 7 {
		t.Errorf("rewriting identical bytes should not invalidate the ledger, skipped=%d", third.ObjectsSkipped)
	}
}

func TestExportToArchive(t *testing.T) {
	ctx := context.Background()
	e, part, blobs := newTestEnv(nil)
	seedTenant(t, part, blobs, "tenantA")

	out := filepath.Join(t.TempDir(), "export.tar.gz")
	res, err := runExport(ctx, e, exportOptions{Out: out})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if res.Format != "tar.gz" {
		t.Fatalf("format = %q", res.Format)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	found := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar body %s: %v", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			t.Errorf("%s: header says %d bytes, entry holds %d", hdr.Name, hdr.Size, len(body))
		}
		found[hdr.Name] = hdr.Size
	}
	for _, want := range []string{
		"tenantA/notes/Kitchen-Rebuild-Plan-n1.md",
		"tenantA/attachments/captures/c1/audio.webm",
		"tenantA/_items/SETTINGS.json",
	} {
		if _, ok := found[want]; !ok {
			t.Errorf("archive is missing %s (has %v)", want, found)
		}
	}
}

func TestSafeRelPathRefusesEscape(t *testing.T) {
	for _, in := range []string{"../etc/passwd", "tenants/../../x", "a/../../b"} {
		if _, err := safeRelPath(in); err == nil {
			t.Errorf("safeRelPath(%q) should have refused", in)
		}
	}
	got, err := safeRelPath("tenants/t1/notes/n1/note.md")
	if err != nil || got != "tenants/t1/notes/n1/note.md" {
		t.Errorf("safeRelPath rejected a legitimate key: %q %v", got, err)
	}
}
