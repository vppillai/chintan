package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/version"
)

const exportUsage = `chintanctl export --tenant <id> --out <dir> [flags]

Portability export for one tenant (§9.3). Produces a complete archive — every
record in the tenant's partition and every current object under its S3 prefix,
which covers markdown, alignment sidecars, L0/L1/L2 transcripts, audio, rules and
metadata — plus a manifest with a SHA-256 per file.

"Must be complete enough to satisfy a data-portability request and to migrate off
the product entirely" (§9.3). Completeness is by construction: nothing here
enumerates by entity type, so a type added in a later phase is exported without
this code changing. §Phase 7's full-corpus export uses this same code path — one
implementation, not two.

--dry-run is the DEFAULT (§11.3): without --apply the plan and its cost basis are
printed and nothing is read or written. --apply writes the archive.

Live run:      --instance <name> --account <id> --region <r>
Test run:      --fixtures <path>   (no AWS, no credentials — §11.5)

Examples:
  chintanctl export --tenant personal --instance prod --account 123456789012 \
      --region ca-central-1 --out /tmp/personal-export
  chintanctl export --tenant personal --fixtures ../scripts/test/fixtures/tenant-data \
      --out /tmp/x --json --apply
`

// archiveFormat is the manifest's format tag.
//
// Versioned from the first archive because the archive is a portability artifact: whoever
// reads it years from now needs to know which layout they have, and §Phase 7 adds a second
// (the item-type-aware vault layout) alongside this one.
const archiveFormat = "chintan-tenant-export/1"

// exportResult is the --json document, and the human report is rendered from the same
// struct so the two cannot disagree.
type exportResult struct {
	OK        bool   `json:"ok"`
	Operation string `json:"operation"`

	// DryRun is true when nothing was written. Named the same way in both operations, so
	// a caller checks one field rather than inferring from what is absent.
	DryRun bool `json:"dry_run"`

	Tenant string `json:"tenant"`

	// Plan is the inventory. **Identical between a dry run and an --apply of the same
	// state** — that is §11.5's central assertion, and it holds because both paths render
	// this field from one takeInventory call.
	Plan inventory `json:"plan"`

	Destination string `json:"destination"`

	// Files and Bytes are what --apply wrote, and what a dry run says it would write.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`

	// Cost is the basis an operator prices the run against (§11.3).
	Cost costBasis `json:"cost_estimate"`

	Notes []string `json:"notes"`
}

// costBasis is what the operation will cost, expressed in the units AWS bills rather
// than in dollars.
//
// **No dollar figure, deliberately.** §11.3 requires a cost estimate before --apply, and
// the honest form of one here is the billable quantities: AWS request and data-transfer
// rates are not in config (§7.1 carries provider rates only), so a dollar figure would
// mean a price hardcoded in Go — drifting silently, and contradicting the way every other
// rate in this system is sourced. Requests and bytes are exact and priceable against the
// current rate card.
type costBasis struct {
	// GetRequests is one per object copied, plus the listings the enumeration made.
	GetRequests int `json:"get_requests"`

	// TransferBytes is what leaves S3. Free within the same region, charged out of it —
	// which is why the number is reported rather than judged.
	TransferBytes int64 `json:"transfer_bytes"`

	// ProviderSpend records that no third-party provider is called, so §11.3's
	// spend-breaker clause (§10.5.9) does not apply. Stated rather than omitted: the
	// breaker guards provider spend, and an operator reading a cost estimate is entitled
	// to know which budget it lands in.
	ProviderSpend string `json:"provider_spend"`
}

func runExport(args []string) int {
	fs := newFlagSet("export", exportUsage)
	var o dataOpts
	registerDataFlags(fs, &o)
	out := fs.String("out", "", "directory to write the archive into (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, exportUsage)
		return 2
	}
	if err := export(context.Background(), o, *out); err != nil {
		return fail(err, o.asJSON)
	}
	return 0
}

func export(ctx context.Context, o dataOpts, out string) error {
	if strings.TrimSpace(out) == "" {
		// Required even for a dry run, so the plan states exactly where the archive would
		// land. A dry run that omits the destination is not a description of what --apply
		// does (§11.5).
		return usageErrorf("--out is required: the plan must name the destination the archive would be written to")
	}
	td, err := openTenantData(ctx, o, newCLILogger())
	if err != nil {
		return err
	}
	return exportTenant(ctx, td, out, o.apply, o.asJSON)
}

// exportTenant is the operation itself, separated from argument handling for the same
// reason as eraseTenant: a test supplies its own stores, and §Phase 7's handler will supply
// its own sink.
func exportTenant(ctx context.Context, td *tenantData, out string, apply, asJSON bool) error {
	inv, err := takeInventory(ctx, td)
	if err != nil {
		return err
	}
	current := inv.currentObjects()

	res := exportResult{
		OK:          true,
		Operation:   "export",
		DryRun:      !apply,
		Tenant:      string(td.tenant),
		Plan:        inv,
		Destination: out,
		Files:       len(inv.Records) + len(current) + manifestFileCount,
		Bytes:       inv.CurrentBytes,
		Cost: costBasis{
			GetRequests:   len(current) + inv.APIRequests,
			TransferBytes: inv.CurrentBytes,
			ProviderSpend: "none — no provider is called, so the daily spend breaker (§10.5.9) does not apply",
		},
		Notes: exportNotes(td, inv),
	}

	// Before, not after (§9.3, and audit.Record's own contract). A dry run writes one too:
	// §11.3 requires an audit record for every invocation of a data script, and a plan is
	// produced by reading the tenant's inventory — which is an access.
	action := "tenant.export"
	if !apply {
		action = "tenant.export.plan"
	}
	if err := recordAccess(ctx, td, action); err != nil {
		return err
	}

	if apply {
		sink, err := newDirSink(out)
		if err != nil {
			return err
		}
		written, bytes, err := writeArchive(ctx, td, inv, sink, res.Notes)
		if err != nil {
			return err
		}
		res.Files, res.Bytes = written, bytes
		res.Destination = sink.Describe()
	}

	// Metered after the work, because the quantity is the call count actually made (I12).
	if err := meterRequests(ctx, td, action, td.objects.Requests()); err != nil {
		return err
	}
	res.Cost.GetRequests = td.objects.Requests()

	if asJSON {
		emitReportJSON(res)
		return nil
	}
	printExport(res)
	return nil
}

// manifestFileCount is the two files the archive carries beyond the data itself: the
// manifest and the README that makes the layout readable without this source.
const manifestFileCount = 2

// exportNotes are the caveats the archive and the report both carry.
//
// Written as data rather than printed inline because the manifest embeds them: an archive
// that travels to whoever requested it should state its own limits, and a caveat that
// exists only in the terminal output of the run that produced it is a caveat nobody sees.
func exportNotes(td *tenantData, inv inventory) []string {
	notes := []string{
		"Records are the complete contents of this tenant's DynamoDB partition, one JSON file each, with attributes verbatim. Nothing is filtered by entity type, so an entity type added in a later phase is included without a code change.",
		"Objects are the CURRENT version of every object under the tenant's S3 prefix. Noncurrent versions are not exported: they are superseded copies, not content the tenant authored.",
		"The audit record (I13) and metering record (I12) written by this export are not in the archive — they postdate the inventory it was taken from.",
		"**The archive is NOT encrypted.** I8 covers data at rest in AWS; a copy on local disk is outside it. Files are written 0600 and directories 0700, which is a permission, not encryption. Put it on an encrypted volume, and delete it when the portability request is satisfied.",
	}
	if inv.DeleteMarkers > 0 {
		notes = append(notes, fmt.Sprintf("%d object key(s) under this prefix have a delete marker as their current version and are therefore not exported. Their earlier versions still exist in S3 (G-021) but are not this tenant's current content.", inv.DeleteMarkers))
	}
	if td.fixtures {
		notes = append(notes, "FIXTURE RUN: no AWS account was read. Counts describe the fixture set, not a deployment.")
	}
	return notes
}

// ---------------------------------------------------------------------------
// Archive writing
// ---------------------------------------------------------------------------

// manifest is the archive's index and its verification data.
type manifest struct {
	Format    string `json:"format"`
	Layout    string `json:"layout"`
	Tenant    string `json:"tenant"`
	Generated string `json:"generated_at"`

	// Producer records which build wrote the archive, so a defect in an export can be
	// traced to a version rather than guessed at (§0.6).
	Producer string `json:"producer"`

	Table        string `json:"table"`
	ObjectStore  string `json:"object_store"`
	ObjectPrefix string `json:"object_prefix"`

	// KMSKeyID and CryptoShreddable record the encryption terms the source data was held
	// under. Present because the archive outlives the deployment, and because "was this
	// held under a customer key" is the first question a later erasure or audit asks
	// (§9.3).
	KMSKeyID         string `json:"kms_key_id"`
	CryptoShreddable bool   `json:"crypto_shreddable"`

	Records []manifestEntry `json:"records"`
	Objects []manifestEntry `json:"objects"`
	Notes   []string        `json:"notes"`
}

// manifestEntry is one file in the archive, with the checksum that makes it verifiable.
type manifestEntry struct {
	Path      string `json:"path"`
	SK        string `json:"sk,omitempty"`
	Key       string `json:"key,omitempty"`
	VersionID string `json:"version_id,omitempty"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// writeArchive writes every record, every current object, the manifest and the README.
//
// Records first, then objects, then the manifest — the manifest carries checksums, so it
// can only be written once everything it describes exists. An archive whose manifest was
// written first would list files that a failed run never produced, and a manifest that
// lies about its own contents is worse than none.
func writeArchive(ctx context.Context, td *tenantData, inv inventory, sink archiveSink, notes []string) (files int, total int64, err error) {
	m := manifest{
		Format:           archiveFormat,
		Layout:           "raw",
		Tenant:           string(td.tenant),
		Generated:        clock.RFC3339UTC(clock.System{}.Now()),
		Producer:         "chintanctl " + version.Display(),
		Table:            td.table,
		ObjectStore:      inv.ObjectStore,
		ObjectPrefix:     inv.ObjectPrefix,
		KMSKeyID:         td.scope.Ref().ID(),
		CryptoShreddable: td.scope.CryptoShreddable(),
		Notes:            notes,
	}

	byKey := make(map[string]repository.Item, len(inv.items))
	for _, it := range inv.items {
		byKey[it.Key.SK] = it
	}
	for _, r := range inv.Records {
		it, ok := byKey[r.SK]
		if !ok {
			// Unreachable unless the inventory and its items disagree, which would mean
			// the archive silently omitted a record the plan promised. Refuse rather than
			// export an incomplete corpus (§9.3).
			return 0, 0, fmt.Errorf("record %s is in the plan but not in the item set; the archive would be incomplete", r.SK)
		}
		body, err := json.MarshalIndent(exportedRecord{
			PK:     it.Key.PK,
			SK:     it.Key.SK,
			Attrs:  it.Attrs,
			GSI1PK: it.GSI1PK,
			GSI1SK: it.GSI1SK,
			TTL:    it.TTL,
		}, "", "  ")
		if err != nil {
			return 0, 0, fmt.Errorf("encoding record %s: %w", r.SK, err)
		}
		p := path.Join("records", r.Class, url.PathEscape(r.SK)+".json")
		n, sum, err := sink.Put(p, strings.NewReader(string(body)))
		if err != nil {
			return 0, 0, err
		}
		m.Records = append(m.Records, manifestEntry{Path: p, SK: r.SK, Bytes: n, SHA256: sum})
		files++
		total += n
	}

	for _, v := range inv.currentObjects() {
		rel, err := archiveObjectPath(inv.ObjectPrefix, v.Key)
		if err != nil {
			return 0, 0, err
		}
		body, err := td.objects.GetObject(ctx, v.Key, v.VersionID)
		if err != nil {
			return 0, 0, err
		}
		n, sum, err := sink.Put(rel, body)
		closeErr := body.Close()
		if err != nil {
			return 0, 0, err
		}
		if closeErr != nil {
			return 0, 0, fmt.Errorf("closing %s: %w", v.Key, closeErr)
		}
		if n != v.Bytes && v.Bytes > 0 {
			// The listing said one size and the body was another, which means the object
			// changed under the export. Refused rather than recorded: a portability
			// archive that silently contains a partially-written object is worse than a
			// failed export, because the failure is discovered by whoever migrated onto it.
			return 0, 0, fmt.Errorf("object %s was listed as %d bytes but %d were read; it changed during the export", v.Key, v.Bytes, n)
		}
		m.Objects = append(m.Objects, manifestEntry{Path: rel, Key: v.Key, VersionID: v.VersionID, Bytes: n, SHA256: sum})
		files++
		total += n
	}

	readme := archiveREADME(m)
	n, _, err := sink.Put("README.md", strings.NewReader(readme))
	if err != nil {
		return 0, 0, err
	}
	files++
	total += n

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, 0, fmt.Errorf("encoding manifest: %w", err)
	}
	n, _, err = sink.Put("manifest.json", strings.NewReader(string(body)))
	if err != nil {
		return 0, 0, err
	}
	files++
	total += n
	return files, total, nil
}

// exportedRecord is one DynamoDB item as the archive stores it.
//
// The key is included. It is what makes the archive restorable and what makes the tenant
// scoping of every record visible to whoever inspects it — an archive of attribute maps
// with no keys is a set of documents nobody can place.
type exportedRecord struct {
	PK     string         `json:"pk"`
	SK     string         `json:"sk"`
	Attrs  map[string]any `json:"attrs"`
	GSI1PK string         `json:"gsi1pk,omitempty"`
	GSI1SK string         `json:"gsi1sk,omitempty"`
	TTL    int64          `json:"ttl,omitempty"`
}

// archiveObjectPath maps an S3 key to a path inside the archive.
//
// **This is a path-traversal boundary, not a formatting helper.** The keys package
// validates every key it constructs, but an export reads whatever the bucket returns, and
// a key containing "../" would place a written file outside the archive directory —
// overwriting whatever is there, with the operator's own credentials. So the prefix must
// match exactly and the remainder must be a plain relative path, or the export refuses.
// Refusing costs a re-run; the alternative costs files outside the destination.
func archiveObjectPath(prefix, key string) (string, error) {
	if !strings.HasPrefix(key, prefix) {
		// A listing that returned a key outside the tenant prefix means the scoping this
		// operation rests on did not hold (I11). Nothing about that is safe to continue
		// past.
		return "", fmt.Errorf("object key %q is outside the tenant prefix %q; refusing to export it (I11)", key, prefix)
	}
	rel := strings.TrimPrefix(key, prefix)
	if rel == "" {
		return "", fmt.Errorf("object key %q is the tenant prefix itself and names no object", key)
	}
	if strings.ContainsAny(rel, "\x00\\") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("object key %q is not a usable archive path", key)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return "", fmt.Errorf("object key %q contains a path segment that would escape the archive directory", key)
		}
	}
	return path.Join("objects", rel), nil
}

// archiveREADME is the archive's own documentation.
//
// §9.3 requires the export to be "complete enough to ... migrate off the product
// entirely", and completeness is not only about bytes: an archive of opaque JSON with no
// explanation of the layout is a migration nobody can perform. This file is what makes the
// tree readable without this source code.
func archiveREADME(m manifest) string {
	var b strings.Builder
	b.WriteString("# Chintan tenant export\n\n")
	b.WriteString("Format: `" + m.Format + "` (layout `" + m.Layout + "`)\n")
	b.WriteString("Generated: " + m.Generated + " by " + m.Producer + "\n\n")
	b.WriteString("This archive is a complete copy of one tenant's data, taken under the\n")
	b.WriteString("portability guarantee in §9.3 of the Chintan specification.\n\n")
	b.WriteString("## Layout\n\n")
	b.WriteString("- `manifest.json` — every file with its SHA-256, byte count, and source key.\n")
	b.WriteString("  Verify the archive against it before relying on it.\n")
	b.WriteString("- `records/<CLASS>/<sort-key>.json` — one file per stored record. `pk` and `sk`\n")
	b.WriteString("  are the DynamoDB composite key; `attrs` are the attributes verbatim. The\n")
	b.WriteString("  class directory is the sort key's leading segment, so related records sit\n")
	b.WriteString("  together. Sort keys are percent-encoded in filenames.\n")
	b.WriteString("- `objects/...` — the object store contents, with the per-tenant prefix\n")
	b.WriteString("  stripped. Markdown documents, alignment sidecars, L0/L1/L2 transcripts and\n")
	b.WriteString("  audio all live here, at the paths §6.2 of the specification describes.\n\n")
	b.WriteString("Markdown is the source of truth for user-facing content, and timestamp\n")
	b.WriteString("alignment lives in a sidecar keyed by `^block-id` anchors rather than by\n")
	b.WriteString("character offsets — so the markdown can be edited in any editor without\n")
	b.WriteString("breaking the audio alignment.\n\n")
	b.WriteString("## Notes\n\n")
	for _, n := range m.Notes {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Directory sink
// ---------------------------------------------------------------------------

// dirSink writes an archive into a local directory.
type dirSink struct{ root string }

// newDirSink prepares the destination.
//
// Refuses a non-empty directory that is not already one of our archives. The archive is
// written with the operator's own credentials, and "export into ~/Documents" must not
// scatter files among someone's own. A directory holding a manifest of this format is
// overwritten, because §Phase 7 requires export to be "idempotent and re-runnable".
func newDirSink(root string) (*dirSink, error) {
	info, err := os.Stat(root)
	switch {
	case err == nil && !info.IsDir():
		return nil, usageErrorf("--out %s exists and is not a directory", root)
	case err == nil:
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", root, err)
		}
		if len(entries) > 0 {
			if _, statErr := os.Stat(filepath.Join(root, "manifest.json")); statErr != nil {
				return nil, usageErrorf("--out %s is not empty and holds no %s manifest; refusing to write an archive into it", root, archiveFormat)
			}
		}
	case os.IsNotExist(err):
		// 0700: the archive is plaintext user content of the most sensitive kind (§9.2),
		// so it is not group- or world-readable even for the moment before anything is
		// written into it.
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", root, err)
		}
	default:
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	return &dirSink{root: root}, nil
}

// Describe names the destination.
func (d *dirSink) Describe() string { return d.root }

// Put writes one file, hashing as it goes.
//
// Streamed through io.Copy rather than read into memory: an audio segment is small, but a
// device-import batch is not (§Phase 6A moves multi-hundred-MB files), and an export that
// buffered a whole object would fail on exactly the corpus that most needs exporting.
func (d *dirSink) Put(rel string, r io.Reader) (int64, string, error) {
	clean := filepath.FromSlash(rel)
	full := filepath.Join(d.root, clean)
	// Second line of defence behind archiveObjectPath: whatever the caller passed, the
	// resulting path must still be inside the archive root.
	if !strings.HasPrefix(full, filepath.Clean(d.root)+string(os.PathSeparator)) {
		return 0, "", fmt.Errorf("archive path %q resolves outside the destination directory", rel)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return 0, "", fmt.Errorf("creating %s: %w", filepath.Dir(full), err)
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("creating %s: %w", full, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	closeErr := f.Close()
	if err != nil {
		return 0, "", fmt.Errorf("writing %s: %w", full, err)
	}
	if closeErr != nil {
		// Checked rather than deferred-and-ignored: on a full or unwritable filesystem the
		// write error surfaces at close, and an export that reported success on a
		// truncated file is a portability archive with a hole in it.
		return 0, "", fmt.Errorf("closing %s: %w", full, closeErr)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// Human-readable report
// ---------------------------------------------------------------------------

func printExport(res exportResult) {
	if res.DryRun {
		fmt.Fprintf(reportOut, "DRY RUN — nothing was read or written.\n\n")
	}
	fmt.Fprintf(reportOut, "tenant:       %s\n", res.Tenant)
	fmt.Fprintf(reportOut, "table:        %s\n", res.Plan.Table)
	fmt.Fprintf(reportOut, "object store: %s\n", res.Plan.ObjectStore)
	fmt.Fprintf(reportOut, "destination:  %s\n\n", res.Destination)

	verb := "would export"
	if !res.DryRun {
		verb = "exported"
	}
	fmt.Fprintf(reportOut, "%s %d record(s) in %d class(es) and %d object(s), %s:\n",
		verb, len(res.Plan.Records), len(res.Plan.RecordClasses), res.Plan.CurrentObjects, humanBytes(res.Plan.CurrentBytes))
	for _, c := range res.Plan.RecordClasses {
		fmt.Fprintf(reportOut, "  %-12s %d\n", c.Class, c.Count)
	}
	if res.Plan.NoncurrentVersions > 0 || res.Plan.DeleteMarkers > 0 {
		fmt.Fprintf(reportOut, "\nnot exported: %d noncurrent version(s), %d delete marker(s)\n",
			res.Plan.NoncurrentVersions, res.Plan.DeleteMarkers)
	}
	fmt.Fprintf(reportOut, "\ncost basis:   %d request(s), %s transferred; no provider spend\n",
		res.Cost.GetRequests, humanBytes(res.Cost.TransferBytes))
	fmt.Fprintf(reportOut, "\nnotes:\n")
	for _, n := range res.Notes {
		fmt.Fprintf(reportOut, "  - %s\n", n)
	}
	if res.DryRun {
		fmt.Fprintf(reportOut, "\nRe-run with --apply to write the archive.\n")
		return
	}
	fmt.Fprintf(reportOut, "\nwrote %d file(s), %s.\n", res.Files, humanBytes(res.Bytes))
}

// humanBytes formats a byte count for a person, without pretending to precision it does
// not have. The exact figure is in --json for anything that needs to compute with it.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1024*1024*1024))
	}
}
