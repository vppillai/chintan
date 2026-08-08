package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/obs"
)

type exportOptions struct {
	Out     string
	Tenants []string
}

type exportTenantResult struct {
	TenantID string `json:"tenant_id"`
	Notes    int    `json:"notes"`
	Captures int    `json:"captures"`
	Objects  int    `json:"objects"`
	// UnknownObjects is objects under the tenant prefix whose shape this
	// build does not recognise. They are exported verbatim under _raw/.
	UnknownObjects int `json:"unknown_objects"`
	// UnrenderedItems is index items with no markdown rendering — settings,
	// tags, usage, and any kind added after this build. They are exported
	// verbatim under _items/.
	UnrenderedItems int `json:"unrendered_items"`
}

type exportResult struct {
	Target         target               `json:"target"`
	Out            string               `json:"out"`
	Format         string               `json:"format"`
	Tenants        []exportTenantResult `json:"tenants"`
	ObjectsCopied  int                  `json:"objects_copied"`
	ObjectsSkipped int                  `json:"objects_skipped"`
	BytesCopied    int64                `json:"bytes_copied"`
}

func (r *exportResult) human(w io.Writer) {
	fmt.Fprintf(w, "export %s (%s) -> %s [%s]\n", r.Target.Instance, r.Target.Environment, r.Out, r.Format)
	for _, t := range r.Tenants {
		fmt.Fprintf(w, "  tenant %s: %d notes, %d captures, %d objects", t.TenantID, t.Notes, t.Captures, t.Objects)
		if t.UnknownObjects > 0 {
			fmt.Fprintf(w, ", %d unrecognised objects kept under _raw/", t.UnknownObjects)
		}
		if t.UnrenderedItems > 0 {
			fmt.Fprintf(w, ", %d index items kept under _items/", t.UnrenderedItems)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  %d objects written, %d unchanged and skipped, %s transferred\n",
		r.ObjectsCopied, r.ObjectsSkipped, humanBytes(r.BytesCopied))
}

func cmdExport(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var o exportOptions
	fs := newFlagSet("export", stderr)
	g.register(fs, true, false)
	fs.StringVar(&o.Out, "out", "", "output directory, or a path ending in .tar.gz (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.Out == "" {
		return fmt.Errorf("--out is required")
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	o.Tenants = g.tenants
	res, err := runExport(ctx, e, o)
	if err != nil {
		return err
	}
	return report(stdout, g.jsonOut, res)
}

// ledgerEntry records what an earlier export wrote, so a re-run can skip an
// object whose source has not changed.
type ledgerEntry struct {
	Dest string `json:"dest"`
	ETag string `json:"etag,omitempty"`
	Size int64  `json:"size"`
	// Total is the byte count actually written, which differs from Size for a
	// note: the destination is front matter plus the body.
	Total int64 `json:"total"`
	// Derived is a hash of anything chintanctl generated for this destination
	// rather than copied. A title edited in DynamoDB changes the front matter
	// without changing the object, and must still force a rewrite.
	Derived string `json:"derived,omitempty"`
}

const ledgerName = ".chintanctl-export.json"

func loadLedger(root string) map[string]ledgerEntry {
	out := map[string]ledgerEntry{}
	body, err := os.ReadFile(filepath.Join(root, ledgerName))
	if err != nil {
		return out
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return map[string]ledgerEntry{}
	}
	return out
}

func saveLedger(root string, entries map[string]ledgerEntry) error {
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ledgerName), body, 0o644)
}

// runExport writes an Obsidian-friendly tree: one markdown file per note with
// YAML front matter, each capture's artifacts beside it, and everything this
// build does not recognise carried verbatim rather than dropped.
func runExport(ctx context.Context, e *env, o exportOptions) (*exportResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, o.Tenants)
	if err != nil {
		return nil, err
	}

	res := &exportResult{Target: e.Target, Out: o.Out, Format: "directory"}

	var sink exportSink
	var ledger map[string]ledgerEntry
	if isArchivePath(o.Out) {
		res.Format = "tar.gz"
		s, err := newTarSink(o.Out)
		if err != nil {
			return nil, err
		}
		sink = s
	} else {
		s, err := newDirSink(o.Out)
		if err != nil {
			return nil, err
		}
		sink = s
		ledger = loadLedger(o.Out)
	}
	defer func() { _ = sink.Close() }()

	next := map[string]ledgerEntry{}

	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)
		tr, err := exportTenant(tctx, e, sink, tenantID, ledger, next, res)
		if err != nil {
			return nil, err
		}
		res.Tenants = append(res.Tenants, tr)
	}

	if err := sink.Close(); err != nil {
		return nil, err
	}
	if ledger != nil {
		if err := saveLedger(o.Out, next); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func exportTenant(ctx context.Context, e *env, sink exportSink, tenantID string,
	ledger, next map[string]ledgerEntry, res *exportResult) (exportTenantResult, error) {

	tr := exportTenantResult{TenantID: tenantID}

	// Half one: the DynamoDB partition, walked whole. Items with no markdown
	// rendering are written out verbatim as they are seen, so nothing has to
	// be held and nothing new can fall out. See enumerate.go.
	idx, err := buildIndex(ctx, e.Part, tenantID, func(it Item) error {
		sk := it.SK()
		if strings.HasPrefix(sk, "NOTE#") || strings.HasPrefix(sk, "CAPTURE#") {
			return nil
		}
		body, err := json.MarshalIndent(it, "", "  ")
		if err != nil {
			return fmt.Errorf("encode item %s: %w", sk, err)
		}
		body = append(body, '\n')
		rel := path.Join(tenantID, "_items", sanitizeSegment(sk)+".json")
		if err := writeAll(sink, rel, body); err != nil {
			return err
		}
		tr.UnrenderedItems++
		return nil
	})
	if err != nil {
		return tr, err
	}
	tr.Notes = len(idx.Notes)
	tr.Captures = len(idx.Captures)

	// Half two: the S3 prefix, walked whole. Every key under the tenant gets a
	// destination — a recognised shape gets a friendly one, anything else is
	// mirrored under _raw/.
	bodiesSeen := map[string]bool{}
	err = e.Blobs.List(ctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
		tr.Objects++
		ref := parseObjectKey(info.Key)

		var rel string
		var derived []byte
		switch {
		case ref.Group == "notes" && ref.File == "note.md":
			bodiesSeen[ref.EntityID] = true
			rel = path.Join(tenantID, noteFileName(idx, ref.EntityID))
			derived = renderFrontMatter(idx, ref.EntityID)
		case ref.Group == "notes":
			rel = path.Join(tenantID, "attachments", "notes", ref.EntityID, ref.File)
		case ref.Group == "captures":
			rel = path.Join(tenantID, "attachments", "captures", ref.EntityID, ref.File)
		default:
			tr.UnknownObjects++
			rel = path.Join(tenantID, "_raw", ref.Rest)
		}

		return copyObject(ctx, e, sink, info, rel, derived, ledger, next, res)
	})
	if err != nil {
		return tr, err
	}

	// A note whose body object is missing still gets a file. The front matter
	// is the only remaining record of its title and tags, and an export that
	// silently omits it would look complete.
	missing := make([]string, 0)
	for id := range idx.Notes {
		if !bodiesSeen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		rel := path.Join(tenantID, noteFileName(idx, id))
		body := append(renderFrontMatter(idx, id), []byte("\n> The body object for this note was not present in the bucket.\n")...)
		if err := writeAll(sink, rel, body); err != nil {
			return tr, err
		}
		res.ObjectsCopied++
		res.BytesCopied += int64(len(body))
	}

	obs.Log(ctx).Info("exported tenant",
		slog.Int("notes", tr.Notes),
		slog.Int("captures", tr.Captures),
		slog.Int("objects", tr.Objects),
		slog.Int("unknown_objects", tr.UnknownObjects),
		slog.Int("unrendered_items", tr.UnrenderedItems),
	)
	return tr, nil
}

// copyObject streams one object to the sink, prefixed by any generated bytes,
// skipping the transfer when an earlier run already wrote exactly this.
func copyObject(ctx context.Context, e *env, sink exportSink, info ObjectInfo, rel string,
	derived []byte, ledger, next map[string]ledgerEntry, res *exportResult) error {

	total := info.Size + int64(len(derived))
	entry := ledgerEntry{Dest: rel, ETag: info.ETag, Size: info.Size, Total: total}
	if len(derived) > 0 {
		sum := sha256.Sum256(derived)
		entry.Derived = hex.EncodeToString(sum[:])
	}

	if sink.Resumable() && ledger != nil {
		if prev, ok := ledger[info.Key]; ok && prev == entry && info.ETag != "" {
			if size, exists := sink.Existing(rel); exists && size == total {
				next[info.Key] = entry
				res.ObjectsSkipped++
				return nil
			}
		}
	}

	src, err := e.Blobs.Open(ctx, info.Key)
	if err != nil {
		return fmt.Errorf("open %s: %w", info.Key, err)
	}
	defer func() { _ = src.Close() }()

	dst, err := sink.Create(rel, total)
	if err != nil {
		return err
	}
	if len(derived) > 0 {
		if _, err := dst.Write(derived); err != nil {
			_ = dst.Close()
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	n, err := io.Copy(dst, src)
	if err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy %s: %w", info.Key, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close %s: %w", rel, err)
	}

	next[info.Key] = entry
	res.ObjectsCopied++
	res.BytesCopied += n + int64(len(derived))
	return nil
}

// writeAll writes generated bytes — an item, or front matter with no body.
func writeAll(sink exportSink, rel string, body []byte) error {
	w, err := sink.Create(rel, int64(len(body)))
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return w.Close()
}

// noteFileName is the markdown file one note exports to. The id is always
// part of it: two notes may share a title, and a stable name is what makes a
// second export idempotent.
func noteFileName(idx *tenantIndex, noteID string) string {
	title := ""
	if n, ok := idx.Notes[noteID]; ok {
		title = n.Title
	}
	slug := sanitizeSegment(title)
	if slug == "" {
		slug = "untitled"
	}
	if len(slug) > 80 {
		slug = strings.TrimSpace(slug[:80])
	}
	return "notes/" + slug + "-" + sanitizeSegment(noteID) + ".md"
}

// renderFrontMatter builds the YAML block that heads an exported note.
func renderFrontMatter(idx *tenantIndex, noteID string) []byte {
	n, known := idx.Notes[noteID]
	var b bytes.Buffer
	b.WriteString("---\n")
	b.WriteString("id: " + yamlString(noteID) + "\n")
	if !known {
		b.WriteString("orphaned: true\n")
		b.WriteString("# No index row was found for this note; the body was recovered from the bucket alone.\n")
	}
	b.WriteString("title: " + yamlString(n.Title) + "\n")
	writeYAMLList(&b, "aliases", n.Aliases)
	writeYAMLList(&b, "tags", n.Tags)
	if n.UpdatedAt != "" {
		b.WriteString("updated: " + yamlString(n.UpdatedAt) + "\n")
	}
	if n.DeletedAt != "" {
		b.WriteString("archived: true\n")
		b.WriteString("archived_at: " + yamlString(n.DeletedAt) + "\n")
	}
	if n.PurgeAfter != "" {
		b.WriteString("purge_after: " + yamlString(n.PurgeAfter) + "\n")
	}
	if known {
		b.WriteString("version: " + strconv.FormatInt(n.Version, 10) + "\n")
	}

	captures := idx.NoteCaptures[noteID]
	if len(captures) > 0 {
		b.WriteString("captures:\n")
		for _, id := range captures {
			c := idx.Captures[id]
			b.WriteString("  - id: " + yamlString(c.ID) + "\n")
			if c.CreatedAt != "" {
				b.WriteString("    created_at: " + yamlString(c.CreatedAt) + "\n")
			}
			if c.Status != "" {
				b.WriteString("    status: " + yamlString(string(c.Status)) + "\n")
			}
			if c.DurationMS > 0 {
				b.WriteString("    duration_ms: " + strconv.FormatInt(c.DurationMS, 10) + "\n")
			}
			b.WriteString("    path: " + yamlString("attachments/captures/"+c.ID) + "\n")
		}
	}
	b.WriteString("---\n\n")
	return b.Bytes()
}

func writeYAMLList(b *bytes.Buffer, name string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(name + ":\n")
	for _, v := range values {
		b.WriteString("  - " + yamlString(v) + "\n")
	}
}

// yamlString emits a double-quoted YAML scalar. Go's quoting escapes the same
// characters YAML 1.2 accepts in a double-quoted scalar, so a title holding a
// colon, a quote, or a newline cannot break the document.
func yamlString(s string) string {
	return strconv.Quote(s)
}

// sanitizeSegment turns arbitrary text into one safe filename component. It is
// applied to titles, sort keys and ids, all of which may contain anything.
func sanitizeSegment(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == 0:
			continue
		case unicode.IsControl(r):
			continue
		case r < 0x20 || strings.ContainsRune(`<>:"|?*`, r):
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		case unicode.IsSpace(r):
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			b.WriteRune(r)
			lastDash = false
		}
	}
	out := strings.Trim(b.String(), "-. ")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}
