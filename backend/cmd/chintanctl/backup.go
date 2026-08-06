package main

// `chintanctl backup` — the implementation behind scripts/backup.sh (§11.2, §11.4).
//
// The logic lives here rather than in bash because it is real logic: tenant-scoped
// enumeration, key re-basing through internal/keys, attribute-fidelity checks, and a
// plan that --apply must execute exactly. §11.2 is explicit that the bash wrapper
// owns "only argument parsing, confirmation prompts, and output formatting", and the
// reason is passbook's: two front-ends with their own copies of the operation drift,
// and the drift is invisible until one of them is the one you ran.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/ids"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/systemid"
)

const backupUsage = `chintanctl backup --tenant <id> --dest <dir> [flags]

Full snapshot of one tenant — DynamoDB records plus S3 objects — into a portable
snapshot directory (§11.4, Phase 0).

WHAT THIS IS FOR, AND WHAT PITR IS FOR
  PITR is enabled on the table and versioning on the bucket, so this is not the
  only recovery mechanism and does not try to be:
    PITR / versioning  in-place time travel inside one account, to any second in
                       the retention window. Use it for "the pipeline wrote
                       nonsense an hour ago". It is also the only ATOMIC cut.
    this snapshot      a portable, self-describing copy that outlives the account,
                       the table and the stack. Use it for moving a tenant to a
                       new instance, or keeping a copy before a risky migration.
  A snapshot is read record-side then object-side, so it is not a consistent cut
  across both stores. If you need one, use PITR.

WHAT IT WRITES WHERE
  Into the destination: every record in the tenant's partition, and every object
  under the tenant's prefix. Nothing else — anything belonging to another tenant is
  refused, not copied (I11).
  Into the store: exactly one audit record, for this invocation (I13). That is the
  only write this command makes to the corpus, in either mode. It cannot modify the
  corpus and it can never remove anything.

THE SNAPSHOT CONTAINS PLAINTEXT USER CONTENT
  Verbatim transcripts and raw audio. §9.2 treats the audio corpus as among the
  most sensitive content a product can hold. The at-rest encryption I8 requires is
  a property of the bucket and the table, NOT of these files: once written they are
  plaintext on whatever filesystem you named, with no key to revoke and so no
  crypto-shredding path (§9.3). Files are created 0600 and directories 0700, and
  --apply to a filesystem destination requires --accept-plaintext-copy so that the
  choice is explicit and recorded.

Flags of note:
  --tenant <id>              required; no data operation runs untenanted (I11, §11.3)
  --dest <dir>               snapshot directory; must not already hold anything
  --store <dir>              operate against a local store tree instead of live AWS
  --instance <name>          instance whose table and bucket to read (default dev)
  --accept-plaintext-copy    acknowledge the paragraph above; required with --apply
  --as <id>                  actor recorded in the audit record (I13)
  --json                     machine-readable plan
  --apply                    execute; --dry-run is the default (§11.3)

Exit codes: 0 planned or applied, 1 failure, 2 bad arguments, 3 refused.

Examples:
  chintanctl backup --tenant alice --dest /mnt/enc/snap-2026-08-05
  chintanctl backup --tenant alice --dest /mnt/enc/snap --apply --accept-plaintext-copy
`

// runBackup is the entry point registered in main.go.
func runBackup(args []string) int {
	flags := newFlagSet("backup", backupUsage)
	tenantFlag := flags.String("tenant", "", "tenant id (required, I11)")
	dest := flags.String("dest", "", "snapshot destination directory")
	storeDir := flags.String("store", "", "local store tree to read instead of live AWS")
	instance := flags.String("instance", "dev", "instance whose table and bucket to read")
	actor := flags.String("as", "script:backup", "actor recorded in the audit record (I13)")
	acceptPlaintext := flags.Bool("accept-plaintext-copy", false, "acknowledge that the snapshot is plaintext user content outside the encrypted store (§9.2)")
	asJSON := flags.Bool("json", false, "machine-readable output")
	apply := flags.Bool("apply", false, "execute the plan; the default is a dry run (§11.3)")
	dryRun := flags.Bool("dry-run", false, "explicit form of the default")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dryRun && *apply {
		fmt.Fprintln(os.Stderr, "chintanctl backup: --dry-run and --apply are mutually exclusive")
		return 2
	}
	if *tenantFlag == "" {
		// The refusal I11 requires, worded so the reason travels with it.
		fmt.Fprintln(os.Stderr, "chintanctl backup: --tenant is required: no data operation runs untenanted (I11, §11.3)")
		return 2
	}
	if *dest == "" {
		fmt.Fprintln(os.Stderr, "chintanctl backup: --dest is required")
		return 2
	}

	err := backup(context.Background(), backupOptions{
		tenant:          keys.TenantID(*tenantFlag),
		dest:            *dest,
		storeDir:        *storeDir,
		instance:        *instance,
		actor:           *actor,
		acceptPlaintext: *acceptPlaintext,
		asJSON:          *asJSON,
		apply:           *apply,
	})
	return snapshotExitCode("backup", err)
}

// snapshotExitCode maps an error to the exit code both commands use.
//
// A refusal is 3, distinct from a failure's 1, for the reason cleanup-aws.sh gives:
// "nothing happened" and "something half-happened" call for different next actions,
// and an operator reading an exit code at 2am should not have to guess which.
func snapshotExitCode(cmd string, err error) int {
	if err == nil {
		return 0
	}
	var refusal *snapshotRefusal
	if errors.As(err, &refusal) {
		fmt.Fprintf(os.Stderr, "chintanctl %s: refused: %v\n", cmd, refusal)
		return 3
	}
	fmt.Fprintf(os.Stderr, "chintanctl %s: %v\n", cmd, err)
	return 1
}

// snapshotRefusal marks the outcome "nothing happened, deliberately".
type snapshotRefusal struct{ msg string }

func (r *snapshotRefusal) Error() string { return r.msg }

func snapshotRefuse(format string, args ...any) error {
	return &snapshotRefusal{msg: fmt.Sprintf(format, args...)}
}

type backupOptions struct {
	tenant          keys.TenantID
	dest            string
	storeDir        string
	instance        string
	actor           string
	acceptPlaintext bool
	asJSON          bool
	apply           bool
}

// backupPlannedObject is one object the plan intends to copy.
type backupPlannedObject struct {
	key  string // full store key, only ever used to read
	rel  string // path relative to the tenant prefix, which is what is archived
	isL0 bool
}

func backup(ctx context.Context, opt backupOptions) error {
	pk, err := snapshotTenantPK(opt.tenant)
	if err != nil {
		return err
	}
	tenantPrefix, err := keys.S3TenantPrefix(opt.tenant)
	if err != nil {
		return err
	}
	// Derived, not hardcoded, and fatal if it cannot be derived: the classification
	// is what restore's I1 protection keys on, and a snapshot that mislabels raw
	// transcripts would disarm that protection a year later (see snapshotL0Fragment).
	l0Fragment, err := snapshotL0Fragment()
	if err != nil {
		return err
	}

	destAbs, err := filepath.Abs(opt.dest)
	if err != nil {
		return fmt.Errorf("resolving --dest: %w", err)
	}
	if strings.HasPrefix(opt.dest, "s3://") {
		return snapshotRefuse("an s3:// destination is not wired yet: writing objects needs Put on the " +
			"bucket and internal/awsclient exposes GetObject only. Name a filesystem directory")
	}
	if err := backupCheckDestination(destAbs); err != nil {
		return err
	}

	// The acknowledgement is required before --apply rather than before the plan:
	// planning writes nothing to the destination, so an operator may safely look
	// before deciding.
	if opt.apply && !opt.acceptPlaintext {
		return snapshotRefuse("--apply to a filesystem destination requires --accept-plaintext-copy. " +
			"The snapshot is verbatim transcripts and raw audio in plaintext; the bucket's and table's " +
			"at-rest encryption does not follow it (I8, §9.2), and there is no key to revoke afterwards (§9.3)")
	}

	// Every argument-level refusal above happens BEFORE the corpus is opened, and that
	// ordering is deliberate: §11.3 requires an audit record for every invocation
	// (I13), and these refusals accessed nothing to record. Everything past this line
	// reads user content, so the record goes first.
	corpus, err := snapshotOpenCorpus(opt.storeDir, opt.instance)
	if err != nil {
		return err
	}

	// The audit record goes first, in both modes, and a failure to write it aborts
	// (I13, §11.3) — see snapshotAudit. The keys it wrote come back so the plan can
	// exclude them: an archive containing the record of its own creation is an
	// archive whose contents depend on when it was taken, which would make the §11.5
	// comparison of a dry run against an --apply impossible to assert.
	snapshotID := ids.NewGenerator(clock.System{}).NewID()
	auditKeys, err := snapshotAudit(ctx, corpus, opt.tenant,
		snapshotActionName("tenant.backup", opt.apply), opt.actor, "corpus:"+snapshotID)
	if err != nil {
		return err
	}
	excludedSKs := make(map[string]bool, len(auditKeys))
	for _, k := range auditKeys {
		excludedSKs[k.SK] = true
	}

	res := &snapshotResult{
		Operation:    "backup",
		Mode:         snapshotModeName(opt.apply),
		Store:        corpus.label,
		SnapshotID:   snapshotID,
		SnapshotPath: destAbs,
		SourceTenant: string(opt.tenant),
		Notices: []string{
			"the snapshot is plaintext user content outside the encrypted store (I8, §9.2) — keep it on encrypted media and delete it when done",
			"one audit record was written to the store for this invocation, in both modes (I13, §11.3); it is the only write this command makes to the corpus",
			"records are read before objects, so a snapshot is not an atomic cut across both stores — PITR is (§6.3)",
		},
	}

	// ---- plan: records -----------------------------------------------------
	//
	// One query, qualified by the tenant's partition key. repository offers no Scan
	// and no index read, so there is no expressible query here that is not
	// tenant-scoped (I11) — including from an admin command, which is the case I11
	// names explicitly.
	items, err := corpus.repo.QueryPrefix(ctx, pk, "", 0)
	if err != nil {
		return fmt.Errorf("reading tenant records: %w", err)
	}
	archiveItems := make([]snapshotItem, 0, len(items))
	for _, item := range items {
		if excludedSKs[item.Key.SK] {
			continue
		}
		if item.Key.PK != pk {
			// Unreachable through QueryPrefix, and asserted anyway: the whole safety
			// argument of this command is that everything it copies belongs to the
			// named tenant, and an assertion is cheaper than trusting the query.
			return snapshotRefuse("record %q came back from the tenant partition query under a different partition key; refusing to copy it (I11)", item.Key.SK)
		}
		if item.GSI1PK != "" && item.GSI1PK != pk {
			return snapshotRefuse("record %q carries an index partition key belonging to another tenant; refusing to copy it (I11)", item.Key.SK)
		}
		if item.GSI1PK != "" && item.GSI1SK == "" {
			return snapshotRefuse("record %q carries an index partition key with no index sort key; it would restore into a shape no listing returns (§6.3)", item.Key.SK)
		}
		if err := snapshotCheckRepresentable("attrs", any(item.Attrs)); err != nil {
			res.Plan = append(res.Plan, snapshotPlanEntry{
				Kind: "item", Action: snapActionRefuseAttr, Ref: item.Key.SK,
				Note: "(" + err.Error() + ")",
			})
			continue
		}
		archiveItems = append(archiveItems, snapshotItem{
			SK: item.Key.SK, Attrs: item.Attrs, GSI1SK: item.GSI1SK, TTL: item.TTL,
		})
		res.Plan = append(res.Plan, snapshotPlanEntry{Kind: "item", Action: snapActionCopy, Ref: item.Key.SK})
	}

	// ---- plan: objects -----------------------------------------------------
	//
	// Sizes come from the listing rather than from reading each object, so a dry run
	// costs one LIST and no GETs. Bytes are re-counted while copying under --apply
	// and reported in `applied`, because an object that changed between the plan and
	// the copy is a real possibility on a live store.
	objects, err := corpus.objects.List(ctx, tenantPrefix)
	if err != nil {
		return fmt.Errorf("listing tenant objects: %w", err)
	}
	planned := make([]backupPlannedObject, 0, len(objects))
	for _, ref := range objects {
		if !strings.HasPrefix(ref.Key, tenantPrefix) {
			return snapshotRefuse("the object listing returned a key outside the tenant prefix; refusing to copy it (I11)")
		}
		rel := strings.TrimPrefix(ref.Key, tenantPrefix)
		if rel == "" {
			continue
		}
		isL0 := snapshotIsL0Path(rel, l0Fragment)
		note := ""
		if isL0 {
			note = "(raw transcript — immutable, I1)"
		}
		planned = append(planned, backupPlannedObject{key: ref.Key, rel: rel, isL0: isL0})
		res.Plan = append(res.Plan, snapshotPlanEntry{
			Kind: "object", Action: snapActionCopy, Ref: rel, Bytes: ref.Bytes, Note: note,
		})
	}

	snapshotSortPlan(res.Plan)
	res.Summary = snapshotSummarize(res.Plan)
	res.Refused = res.Summary.Refusals > 0

	// ---- the plan, printed identically in both modes ------------------------
	if !opt.asJSON {
		snapshotRenderPlan(res)
		for _, n := range res.Notices {
			fmt.Printf("NOTE  %s\n", n)
		}
		snapshotPrintCostEstimate(os.Stdout, res.Summary, true)
	}

	if res.Refused {
		if opt.asJSON {
			emitJSON(res)
		}
		return snapshotRefuse("%d record(s) cannot be represented in the snapshot format; nothing was written", res.Summary.Refusals)
	}

	if !opt.apply {
		if opt.asJSON {
			emitJSON(res)
		} else {
			fmt.Println("DRY RUN — no snapshot was written. Re-run with --apply --accept-plaintext-copy to execute.")
		}
		return nil
	}

	// ---- apply: exactly the plan above -------------------------------------
	applied, entries, err := backupWrite(ctx, corpus, destAbs, archiveItems, planned)
	if err != nil {
		return err
	}
	itemsHash, _, err := snapshotHashFile(filepath.Join(destAbs, snapshotItemsFile))
	if err != nil {
		return err
	}
	manifest := snapshotManifest{
		FormatVersion: snapshotFormatVersion,
		SnapshotID:    snapshotID,
		SystemID:      systemid.ID,
		Instance:      opt.instance,
		CreatedAt:     clock.RFC3339UTC(clock.System{}.Now()),
		ToolVersion:   snapshotToolVersion(),
		SourceTenant:  string(opt.tenant),
		Notice:        snapshotNotice,
		Excluded: []string{
			"the audit record this invocation wrote (I13); excluded so the archive does not contain the record of its own creation",
		},
		Items: snapshotItemsSection{
			Count:  len(archiveItems),
			File:   snapshotItemsFile,
			SHA256: itemsHash,
		},
		Objects: entries,
	}
	// The manifest is written LAST, and that ordering is the completeness signal: a
	// directory with no manifest is an interrupted backup, and restore refuses it
	// rather than grafting a partial corpus onto a tenant (see snapshotReadManifest).
	if err := snapshotWriteJSON(filepath.Join(destAbs, snapshotManifestFile), manifest); err != nil {
		return err
	}

	res.Applied = &applied
	if opt.asJSON {
		emitJSON(res)
	} else {
		fmt.Printf("APPLIED items=%d objects=%d bytes=%d\n", applied.ItemsWrite, applied.ObjectsWrite, applied.Bytes)
		fmt.Printf("        snapshot written to %s\n", destAbs)
	}
	return nil
}

// backupCheckDestination refuses a destination that already holds something.
//
// Not idempotent, and it cannot be: a snapshot is a point-in-time copy, so a second
// run produces a different archive rather than converging on the same one — §11.3
// asks for idempotency "wherever the operation permits", and this operation does
// not. Refusing an occupied directory is the closest safe behaviour, because it
// means a mistaken re-run cannot merge two point-in-time copies into an archive that
// represents no instant at all.
func backupCheckDestination(dest string) error {
	entries, err := os.ReadDir(dest)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("checking --dest: %w", err)
	}
	if len(entries) > 0 {
		return snapshotRefuse("destination %s is not empty. A snapshot is a point-in-time copy, so writing a "+
			"second one into the same directory would produce an archive representing no single instant. "+
			"Name a new directory", dest)
	}
	return nil
}

// backupWrite performs exactly the planned copies.
func backupWrite(
	ctx context.Context,
	corpus *snapshotCorpus,
	dest string,
	items []snapshotItem,
	planned []backupPlannedObject,
) (snapshotSummary, []snapshotObjectEntry, error) {
	var applied snapshotSummary

	if err := os.MkdirAll(dest, snapshotDirMode); err != nil {
		return applied, nil, fmt.Errorf("creating snapshot directory: %w", err)
	}

	// items.jsonl: one record per line, so a large partition streams rather than
	// being held as a single JSON document.
	itemsPath := filepath.Join(dest, snapshotItemsFile)
	f, err := os.OpenFile(itemsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, snapshotFileMode)
	if err != nil {
		return applied, nil, fmt.Errorf("creating %s: %w", snapshotItemsFile, err)
	}
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			f.Close()
			return applied, nil, fmt.Errorf("writing record %q: %w", it.SK, err)
		}
		applied.Items++
		applied.ItemsWrite++
	}
	if err := f.Close(); err != nil {
		return applied, nil, fmt.Errorf("closing %s: %w", snapshotItemsFile, err)
	}

	archive := snapshotFSObjects{root: filepath.Join(dest, snapshotObjectsDir)}
	entries := make([]snapshotObjectEntry, 0, len(planned))
	for _, p := range planned {
		body, err := corpus.objects.Open(ctx, p.key)
		if err != nil {
			return applied, nil, fmt.Errorf("reading object: %w", err)
		}
		// Hashed while copying rather than in a second pass: a second read costs
		// another GET and, on a live store, may see different bytes — which would put
		// a hash in the manifest that does not describe the archived copy.
		hr := newSnapshotHashingReader(body)
		err = archive.Create(ctx, p.rel, hr)
		body.Close()
		if err != nil {
			return applied, nil, err
		}
		sum, n := hr.result()
		entries = append(entries, snapshotObjectEntry{Path: p.rel, Bytes: n, SHA256: sum, L0: p.isL0})
		applied.Objects++
		applied.ObjectsWrite++
		applied.Bytes += n
	}
	return applied, entries, nil
}

// snapshotHashingReader hashes and counts what it passes through, so one read
// produces both the copy and the manifest entry that describes it.
type snapshotHashingReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func newSnapshotHashingReader(r io.Reader) *snapshotHashingReader {
	return &snapshotHashingReader{r: r, h: sha256.New()}
}

func (s *snapshotHashingReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.h.Write(p[:n])
		s.n += int64(n)
	}
	return n, err
}

func (s *snapshotHashingReader) result() (string, int64) {
	return hex.EncodeToString(s.h.Sum(nil)), s.n
}

// Compile-time proof that the local store still satisfies the storage contract.
// Without it, a signature drift shows up as a nil-pointer panic at wiring time
// rather than as a build failure.
var _ repository.Repository = (*snapshotLocalRepo)(nil)
