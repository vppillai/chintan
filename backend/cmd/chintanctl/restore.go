package main

// `chintanctl restore` — the implementation behind scripts/restore.sh (§11.2, §11.4).
//
// # Restore is strictly additive. There is no overwrite mode, and that is the point
//
// §11.4 calls this "restore from a snapshot into a NAMED tenant", so the target may
// be a tenant that already holds data. Overwriting a raw-transcript object in that
// tenant would be an I1 violation committed by an operational script rather than by
// application code — and the letter of I1 permits only the tenant-erasure path to
// remove L0 (§9.3), while nothing at all permits replacing it. A "restore" that
// silently replaced one raw transcript with another would also destroy the L0→L2
// diff that is the training signal for the correction system (§6.1), which is
// unrecoverable rather than merely wrong.
//
// So this command never replaces anything:
//
//	absent at the target          written
//	present, byte-identical       skipped — provably the same bytes, so a re-run
//	                              after an interruption resumes rather than refuses
//	present and different, L0     REFUSED, always, in every mode. Two different raw
//	                              transcripts under one key is a state no operator
//	                              should resolve by accident
//	present and different, other  refused by default; skipped with --on-conflict skip
//
// Records go in through repository.PutOnce, so "never overwrite" holds at the
// conditional write rather than only in this planning code — and objects are created
// with O_EXCL for the same reason. A check-then-write would have a window in which
// two concurrent restores both believe a key is free.
//
// The way to get a clean target is therefore not a flag here: restore into a fresh
// tenant id, or erase the tenant first with the separately permissioned erasure
// operation (§9.3), which is the only thing allowed to remove L0.
//
// # Re-keying is mandatory and goes through internal/keys
//
// The target tenant may differ from the source, and I11 binds "admin and migration
// scripts" explicitly. Two things carry a tenant:
//
//   - the partition key of every record, and the per-tenant prefix of every object.
//     The archive stores neither (see snapshot.go), so the only way to produce one
//     here is to ask keys.Tenant and keys.S3TenantPrefix for the TARGET tenant.
//   - references to either INSIDE stored attributes — a capture's s3_prefix is the
//     obvious one (§6.3). These are rewritten through the same helpers, and any
//     residual mention of the source tenant that this code cannot classify refuses
//     the restore rather than being written as a dangling cross-tenant reference.
//
// Sort keys are carried verbatim, because §6.3 gives every entity a tenant-free sort
// key. That includes the identifiers inside them: a user record keeps its user id, a
// capture keeps its capture id. Restore does not invent identities. During the
// personal phase tenant_id == user_id, so a restore into a different tenant leaves
// user records naming the SOURCE user id — the dry-run says so, and that user still
// has to be provisioned with users.sh.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/repository"
)

const restoreUsage = `chintanctl restore --tenant <id> --from <snapshot dir> [flags]

Restore a snapshot into a NAMED tenant (§11.4, Phase 0). Destructive by nature, so
--dry-run is the default and --apply executes (§11.3).

IT NEVER OVERWRITES ANYTHING
  absent at the target          written
  present, byte-identical       skipped, so an interrupted restore resumes
  present and different, L0     REFUSED, always. Raw transcripts are immutable
                                (I1); only tenant erasure may remove one (§9.3),
                                and nothing may replace one.
  present and different, other  refused by default; --on-conflict skip skips them
  There is no --overwrite flag. To restore over existing data, restore into a fresh
  tenant id, or erase the tenant first with erase-tenant.sh — the separately
  permissioned operation that is allowed to remove content.

RE-KEYING
  The archive carries no tenant: records are stored by sort key and objects by a
  path relative to the tenant prefix. Every key written here is built for the TARGET
  tenant through the one key helper (I11, which binds admin scripts too), and
  tenant references inside stored attributes are rewritten through the same helper.
  A residual reference to the source tenant that cannot be classified refuses the
  restore, unless --allow-source-tenant-refs is given.
  Identifiers inside sort keys are preserved — restore does not invent identities.
  In the personal phase tenant_id == user_id, so restoring into a different tenant
  leaves user records naming the source user id; provision the real user with
  users.sh.

WHAT PITR IS FOR
  In-place recovery inside one account is PITR's job (table) and bucket versioning's
  (objects), and both are atomic where this is not. Use this for portability:
  moving a tenant, or seeding a new instance.

Flags of note:
  --tenant <id>                 required; the TARGET tenant (I11, §11.3)
  --from <dir>                  snapshot directory written by backup
  --store <dir>                 operate against a local store tree instead of live AWS
  --instance <name>             instance whose table and bucket to write (default dev)
  --on-conflict refuse|skip     what to do about records or objects already present
  --allow-source-tenant-refs    accept attributes still naming the source tenant
  --as <id>                     actor recorded in the audit record (I13)
  --json                        machine-readable plan
  --apply                       execute; the default is a dry run

Exit codes: 0 planned or applied, 1 failure, 2 bad arguments, 3 refused.

Examples:
  chintanctl restore --tenant bob --from /mnt/enc/snap-2026-08-05
  chintanctl restore --tenant bob --from /mnt/enc/snap-2026-08-05 --apply
`

func runRestore(args []string) int {
	flags := newFlagSet("restore", restoreUsage)
	tenantFlag := flags.String("tenant", "", "target tenant id (required, I11)")
	from := flags.String("from", "", "snapshot directory to restore")
	storeDir := flags.String("store", "", "local store tree to write instead of live AWS")
	instance := flags.String("instance", "dev", "instance whose table and bucket to write")
	onConflict := flags.String("on-conflict", "refuse", "refuse|skip — what to do about keys already present at the target")
	allowRefs := flags.Bool("allow-source-tenant-refs", false, "accept attributes that still name the source tenant")
	actor := flags.String("as", "script:restore", "actor recorded in the audit record (I13)")
	asJSON := flags.Bool("json", false, "machine-readable output")
	apply := flags.Bool("apply", false, "execute the plan; the default is a dry run (§11.3)")
	dryRun := flags.Bool("dry-run", false, "explicit form of the default")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dryRun && *apply {
		fmt.Fprintln(os.Stderr, "chintanctl restore: --dry-run and --apply are mutually exclusive")
		return 2
	}
	if *tenantFlag == "" {
		fmt.Fprintln(os.Stderr, "chintanctl restore: --tenant is required: no data operation runs untenanted (I11, §11.3)")
		return 2
	}
	if *from == "" {
		fmt.Fprintln(os.Stderr, "chintanctl restore: --from is required")
		return 2
	}
	if *onConflict != "refuse" && *onConflict != "skip" {
		fmt.Fprintf(os.Stderr, "chintanctl restore: --on-conflict must be refuse or skip, not %q\n", *onConflict)
		return 2
	}

	err := restore(context.Background(), restoreOptions{
		tenant:     keys.TenantID(*tenantFlag),
		from:       *from,
		storeDir:   *storeDir,
		instance:   *instance,
		onConflict: *onConflict,
		allowRefs:  *allowRefs,
		actor:      *actor,
		asJSON:     *asJSON,
		apply:      *apply,
	})
	return snapshotExitCode("restore", err)
}

type restoreOptions struct {
	tenant     keys.TenantID
	from       string
	storeDir   string
	instance   string
	onConflict string
	allowRefs  bool
	actor      string
	asJSON     bool
	apply      bool
}

// snapshotTenantRefs is the set of tenant-bearing strings for one tenant, all of
// them produced by the key helper.
type snapshotTenantRefs struct {
	tenant   string
	pk       string
	s3Prefix string
}

func snapshotRefsFor(t keys.TenantID) (snapshotTenantRefs, error) {
	pk, err := snapshotTenantPK(t)
	if err != nil {
		return snapshotTenantRefs{}, err
	}
	prefix, err := keys.S3TenantPrefix(t)
	if err != nil {
		return snapshotTenantRefs{}, err
	}
	return snapshotTenantRefs{tenant: string(t), pk: pk, s3Prefix: prefix}, nil
}

// restorePlannedItem pairs a record with the key it will be written under.
type restorePlannedItem struct {
	item repository.Item
	ref  string
}

// restorePlannedObject pairs an archived object with its target key.
type restorePlannedObject struct {
	rel    string
	target string
	entry  snapshotObjectEntry
}

func restore(ctx context.Context, opt restoreOptions) error {
	manifest, err := snapshotReadManifest(opt.from)
	if err != nil {
		return err
	}
	target, err := snapshotRefsFor(opt.tenant)
	if err != nil {
		return err
	}
	// The source tenant is read from the manifest for provenance and for the rewrite
	// rules below — never to construct a key. Its refs go through the same helper so
	// that "what a source-tenant reference looks like" is not a second guess at the
	// key layout.
	source, err := snapshotRefsFor(keys.TenantID(manifest.SourceTenant))
	if err != nil {
		return fmt.Errorf("manifest records an unusable source tenant: %w", err)
	}
	// Fatal rather than best-effort: without this classification restore cannot prove
	// it is not about to replace a raw transcript, and "cannot prove" is not a state
	// in which to write to a corpus (I1).
	l0Fragment, err := snapshotL0Fragment()
	if err != nil {
		return err
	}

	corpus, err := snapshotOpenCorpus(opt.storeDir, opt.instance)
	if err != nil {
		return err
	}

	// Archive integrity comes before anything else, and in BOTH modes. A snapshot
	// that lost bytes on the way to this disk would otherwise graft a silently
	// incomplete corpus onto a tenant — and the records it lost are exactly the ones
	// nobody notices are missing.
	itemsPath := filepath.Join(opt.from, snapshotItemsFile)
	itemsHash, _, err := snapshotHashFile(itemsPath)
	if err != nil {
		return err
	}
	if itemsHash != manifest.Items.SHA256 {
		return snapshotRefuse("the records file does not match its manifest hash; this archive is damaged or truncated. Nothing was written")
	}

	// The audit record for this invocation, written against the TARGET tenant before
	// anything is read or written, in both modes (I13, §11.3, and audit.Record's
	// "call it before the access"). The snapshot id ties the log line to the archive
	// on disk; the manifest is what records which tenant the data came from, because
	// §6.3's audit entity has no field for a second tenant and gaining one for this
	// caller's benefit would make the record a shape audit.sh cannot query.
	if _, err := snapshotAudit(ctx, corpus, opt.tenant,
		snapshotActionName("tenant.restore", opt.apply), opt.actor, "corpus:"+manifest.SnapshotID); err != nil {
		return err
	}

	res := &snapshotResult{
		Operation:    "restore",
		Mode:         snapshotModeName(opt.apply),
		Store:        corpus.label,
		SnapshotID:   manifest.SnapshotID,
		SnapshotPath: opt.from,
		SourceTenant: manifest.SourceTenant,
		TargetTenant: string(opt.tenant),
		Notices: []string{
			"nothing is ever overwritten: a key already present is skipped when byte-identical and refused otherwise, and a raw transcript is refused unconditionally (I1)",
			"one audit record was written to the target tenant for this invocation, in both modes (I13, §11.3)",
			"in-place recovery is PITR's job (table) and bucket versioning's (objects); this is the portable path (§6.3, §6.2)",
		},
	}
	if manifest.SourceTenant != string(opt.tenant) {
		res.Notices = append(res.Notices,
			"the target tenant differs from the source: identifiers inside sort keys are preserved, so user records still name the source user id — provision the real user with users.sh")
	}

	// ---- existing state at the target --------------------------------------
	existing := map[string]repository.Item{}
	current, err := corpus.repo.QueryPrefix(ctx, target.pk, "", 0)
	if err != nil {
		return fmt.Errorf("reading the target tenant's existing records: %w", err)
	}
	for _, it := range current {
		existing[it.Key.SK] = it
	}

	// ---- plan: records -----------------------------------------------------
	archived, err := restoreReadItems(itemsPath)
	if err != nil {
		return err
	}
	if len(archived) != manifest.Items.Count {
		return snapshotRefuse("the archive holds %d records but its manifest declares %d; refusing a restore whose completeness it cannot vouch for",
			len(archived), manifest.Items.Count)
	}
	// One reading of the clock for the whole plan, so every expiry note in one run is
	// judged against the same instant.
	now := clock.System{}.Now().Unix()
	plannedItems := make([]restorePlannedItem, 0, len(archived))
	for _, a := range archived {
		if err := restoreCheckSortKey(a.SK); err != nil {
			return snapshotRefuse("%v", err)
		}
		attrs, rewrites, residual, err := restoreRekeyAttrs(a.Attrs, source, target)
		if err != nil {
			return err
		}
		if len(residual) > 0 && !opt.allowRefs {
			res.Plan = append(res.Plan, snapshotPlanEntry{
				Kind: "item", Action: snapActionRefuseRef, Ref: a.SK,
				Note: "(mentions the source tenant at " + strings.Join(residual, ", ") +
					" — writing it could leave a cross-tenant reference (I11); the match is by substring, so judge the named attribute and use --allow-source-tenant-refs if it is legitimate)",
			})
			continue
		}

		// The ONLY tenant-bearing component of the key comes from the helper; the
		// sort key is tenant-free by §6.3 and is carried as data. There is no key
		// constructor that takes an arbitrary sort key, and inventing one would be a
		// constructor that could build any key at all — the opposite of what the
		// helper's monopoly is for.
		item := repository.Item{
			Key:   keys.DynamoKey{PK: target.pk, SK: a.SK},
			Attrs: attrs,
			TTL:   a.TTL,
		}
		if a.GSI1SK != "" {
			// GSI1's partition key is the tenant's, so it is rebuilt rather than
			// carried. A record restored with the source tenant's index partition key
			// would be stored correctly and never appear in the target's listing
			// (§6.3) — which reads as data loss, not as a key bug.
			item.GSI1PK = target.pk
			item.GSI1SK = a.GSI1SK
		}

		note := ""
		if rewrites > 0 {
			note = fmt.Sprintf("(%d tenant reference(s) re-keyed)", rewrites)
		}
		if a.TTL > 0 && a.TTL <= now {
			// Worth saying out loud: usage and audit records carry a TTL (§6.3), and
			// one restored past its expiry is swept by DynamoDB shortly after it
			// lands. An operator who does not know that reads it as a failed restore.
			note += " (ttl already expired — the store will sweep this record)"
		}

		if prev, ok := existing[a.SK]; ok {
			if restoreItemsIdentical(prev, item) {
				res.Plan = append(res.Plan, snapshotPlanEntry{
					Kind: "item", Action: snapActionSkipIdentical, Ref: a.SK,
					Note: "(already present, identical)",
				})
				continue
			}
			action := snapActionRefuseExists
			if opt.onConflict == "skip" {
				action = snapActionSkipExists
			}
			res.Plan = append(res.Plan, snapshotPlanEntry{
				Kind: "item", Action: action, Ref: a.SK,
				Note: "(already present and different — nothing is ever overwritten)",
			})
			continue
		}

		plannedItems = append(plannedItems, restorePlannedItem{item: item, ref: a.SK})
		res.Plan = append(res.Plan, snapshotPlanEntry{Kind: "item", Action: snapActionPut, Ref: a.SK, Note: strings.TrimSpace(note)})
	}

	// ---- plan: objects -----------------------------------------------------
	archiveObjects := snapshotFSObjects{root: filepath.Join(opt.from, snapshotObjectsDir)}
	plannedObjects := make([]restorePlannedObject, 0, len(manifest.Objects))
	for _, entry := range manifest.Objects {
		// Shape-checked before it is joined to anything: a traversal segment must
		// never reach a key, and an archive may have come from anywhere.
		if err := snapshotCheckKeyShape(entry.Path); err != nil {
			return snapshotRefuse("%v", err)
		}
		// Verified against the manifest in both modes, for the same reason the
		// records file is.
		body, err := archiveObjects.Open(ctx, entry.Path)
		if err != nil {
			return snapshotRefuse("the manifest lists %q but the archive does not contain it; this archive is incomplete", entry.Path)
		}
		sum, n, err := snapshotHashReader(body)
		body.Close()
		if err != nil {
			return fmt.Errorf("reading archived object: %w", err)
		}
		if sum != entry.SHA256 || n != entry.Bytes {
			return snapshotRefuse("archived object %q does not match its manifest hash; this archive is damaged. Nothing was written", entry.Path)
		}

		// The target key: the tenant prefix from the helper, plus the archive's
		// tenant-free relative path.
		targetKey := target.s3Prefix + entry.Path
		// Classified from the layout as well as trusted from the manifest. The
		// manifest was written by some other build, and I1 protection that depends on
		// a field in a file an operator could edit is not protection.
		isL0 := entry.L0 || snapshotIsL0Path(entry.Path, l0Fragment)

		present, err := corpus.objects.Exists(ctx, targetKey)
		if err != nil {
			return fmt.Errorf("checking the target object: %w", err)
		}
		if present {
			same, err := restoreObjectIdentical(ctx, corpus.objects, targetKey, entry.SHA256)
			if err != nil {
				return err
			}
			switch {
			case same:
				res.Plan = append(res.Plan, snapshotPlanEntry{
					Kind: "object", Action: snapActionSkipIdentical, Ref: entry.Path, Bytes: entry.Bytes,
					Note: "(already present, identical bytes)",
				})
			case isL0:
				// The refusal this command exists to get right. Not overridable by any
				// flag: L0 is immutable (I1), the erasure path is the only thing that
				// may remove one (§9.3), and nothing may replace one. Two different raw
				// transcripts under one key is a state for an operator to investigate,
				// not for a script to resolve.
				res.Plan = append(res.Plan, snapshotPlanEntry{
					Kind: "object", Action: snapActionRefuseL0, Ref: entry.Path, Bytes: entry.Bytes,
					Note: "(a DIFFERENT raw transcript is already stored under this key — L0 is immutable (I1) and this is never overwritten)",
				})
			case opt.onConflict == "skip":
				res.Plan = append(res.Plan, snapshotPlanEntry{
					Kind: "object", Action: snapActionSkipExists, Ref: entry.Path, Bytes: entry.Bytes,
					Note: "(already present and different — left alone)",
				})
			default:
				res.Plan = append(res.Plan, snapshotPlanEntry{
					Kind: "object", Action: snapActionRefuseExists, Ref: entry.Path, Bytes: entry.Bytes,
					Note: "(already present and different — nothing is ever overwritten)",
				})
			}
			continue
		}

		note := ""
		if isL0 {
			note = "(raw transcript — immutable once written, I1)"
		}
		plannedObjects = append(plannedObjects, restorePlannedObject{rel: entry.Path, target: targetKey, entry: entry})
		res.Plan = append(res.Plan, snapshotPlanEntry{
			Kind: "object", Action: snapActionPut, Ref: entry.Path, Bytes: entry.Bytes, Note: note,
		})
	}

	snapshotSortPlan(res.Plan)
	res.Summary = snapshotSummarize(res.Plan)
	res.Refused = res.Summary.Refusals > 0

	if !opt.asJSON {
		snapshotRenderPlan(res)
		for _, n := range res.Notices {
			fmt.Printf("NOTE  %s\n", n)
		}
		snapshotPrintCostEstimate(os.Stdout, res.Summary, false)
	}

	if res.Refused {
		if opt.asJSON {
			emitJSON(res)
		}
		// Counted by kind, because the three refusals call for different next actions
		// and a single "N refusals" would make an operator re-read the plan to find out
		// which they got.
		var l0, exists, refs int
		for _, e := range res.Plan {
			switch e.Action {
			case snapActionRefuseL0:
				l0++
			case snapActionRefuseExists:
				exists++
			case snapActionRefuseRef:
				refs++
			}
		}
		msg := fmt.Sprintf("nothing was written. %d refusal(s):", res.Summary.Refusals)
		if l0 > 0 {
			msg += fmt.Sprintf(" %d raw transcript(s) already stored under the same key with DIFFERENT bytes — that is never overwritten (I1) and no flag changes it; investigate before restoring;", l0)
		}
		if exists > 0 {
			msg += fmt.Sprintf(" %d key(s) already present and different — restore into a fresh tenant id, or --on-conflict skip to write only what is missing;", exists)
		}
		if refs > 0 {
			msg += fmt.Sprintf(" %d record(s) still mention the source tenant in an attribute this tool cannot re-key — read the plan, then --allow-source-tenant-refs if the mention is legitimate;", refs)
		}
		return snapshotRefuse("%s", strings.TrimSuffix(msg, ";"))
	}

	if !opt.apply {
		if opt.asJSON {
			emitJSON(res)
		} else {
			fmt.Println("DRY RUN — nothing was written to the target. Re-run with --apply to execute.")
		}
		return nil
	}

	// ---- apply: exactly the plan above -------------------------------------
	var applied snapshotSummary
	for _, p := range plannedItems {
		// PutOnce, never Put: "never overwrite" is then enforced by the conditional
		// write rather than by the planning above having been right. A key that
		// appeared between the plan and now is a concurrent writer, and stopping is
		// the only safe response — this operation has no idea what the other writer
		// intended.
		if err := corpus.repo.PutOnce(ctx, p.item); err != nil {
			if errors.Is(err, repository.ErrAlreadyExists) {
				return fmt.Errorf("record %q appeared at the target after the plan was computed; "+
					"another writer is active. %d record(s) and %d object(s) were written before this point",
					p.ref, applied.ItemsWrite, applied.ObjectsWrite)
			}
			return fmt.Errorf("writing record %q: %w", p.ref, err)
		}
		applied.Items++
		applied.ItemsWrite++
	}
	for _, p := range plannedObjects {
		body, err := archiveObjects.Open(ctx, p.rel)
		if err != nil {
			return fmt.Errorf("reading archived object %q: %w", p.rel, err)
		}
		hr := newSnapshotHashingReader(body)
		err = corpus.objects.Create(ctx, p.target, hr)
		body.Close()
		if err != nil {
			return fmt.Errorf("writing object %q: %w", p.rel, err)
		}
		sum, n := hr.result()
		if sum != p.entry.SHA256 {
			// Verified again on the way out: the bytes that landed are the bytes the
			// manifest describes, or the operator hears about it now rather than from
			// verify.sh months later (§11.6).
			return fmt.Errorf("object %q was copied but its hash does not match the manifest; the target now holds a copy this command cannot vouch for", p.rel)
		}
		applied.Objects++
		applied.ObjectsWrite++
		applied.Bytes += n
	}
	if corpus.flush != nil {
		if err := corpus.flush(); err != nil {
			return err
		}
	}

	res.Applied = &applied
	if opt.asJSON {
		emitJSON(res)
	} else {
		fmt.Printf("APPLIED items=%d objects=%d bytes=%d\n", applied.ItemsWrite, applied.ObjectsWrite, applied.Bytes)
	}
	return nil
}

// restoreReadItems reads items.jsonl.
//
// A json.Decoder over the stream rather than a line scanner: a record may be up to
// the 400KB item ceiling (§3A.4 puts oversized bodies in S3, but a body just under
// the ceiling is legal), and a scanner has a token limit that would truncate one.
func restoreReadItems(pathname string) ([]snapshotItem, error) {
	f, err := os.Open(pathname)
	if err != nil {
		return nil, fmt.Errorf("reading archived records: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.UseNumber()
	var out []snapshotItem
	for {
		var raw struct {
			SK     string          `json:"sk"`
			Attrs  json.RawMessage `json:"attrs"`
			GSI1SK string          `json:"gsi1sk"`
			TTL    json.Number     `json:"ttl"`
		}
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading archived records: %w", err)
		}
		it := snapshotItem{SK: raw.SK, GSI1SK: raw.GSI1SK}
		if raw.TTL != "" {
			ttl, err := raw.TTL.Int64()
			if err != nil {
				return nil, fmt.Errorf("archived record %q has an unusable ttl", raw.SK)
			}
			it.TTL = ttl
		}
		if len(raw.Attrs) > 0 {
			var attrs map[string]any
			ad := json.NewDecoder(strings.NewReader(string(raw.Attrs)))
			ad.UseNumber()
			if err := ad.Decode(&attrs); err != nil {
				return nil, fmt.Errorf("archived record %q has unreadable attributes: %w", raw.SK, err)
			}
			normalized, err := snapshotNormalizeNumbers(attrs)
			if err != nil {
				return nil, fmt.Errorf("archived record %q: %w", raw.SK, err)
			}
			if m, ok := normalized.(map[string]any); ok {
				it.Attrs = m
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// restoreCheckSortKey refuses a sort key that could not have come from the key
// helper.
//
// The archive is data, and data from outside is not trusted just because a manifest
// vouched for its hash. An empty or control-character-bearing sort key would be
// refused by the repository layer anyway; refusing here means the failure names the
// archive rather than surfacing as an opaque marshalling error halfway through a
// restore.
func restoreCheckSortKey(sk string) error {
	if strings.TrimSpace(sk) == "" {
		return fmt.Errorf("the archive holds a record with an empty sort key")
	}
	if len(sk) > 1024 {
		// DynamoDB's sort key limit. Reported as a length, never quoted: an
		// over-length key is a body that reached the wrong field (§9.2).
		return fmt.Errorf("the archive holds a record whose sort key is %d bytes, over DynamoDB's 1024-byte limit", len(sk))
	}
	if strings.ContainsFunc(sk, unicode.IsControl) {
		return fmt.Errorf("the archive holds a record whose sort key contains a control character (length %d)", len(sk))
	}
	return nil
}

// restoreRekeyAttrs rewrites tenant references inside stored attributes, and reports
// any it could not classify.
//
// This is the part that is easy to skip and expensive to skip. §6.3 stores a
// capture's s3_prefix as an attribute, so a record restored verbatim into another
// tenant would point at the SOURCE tenant's objects — a cross-tenant reference
// written by an admin script, which is exactly what I11's "including admin and
// migration scripts" is about. The rewrite is driven by the key helper's own output
// for both tenants, so it cannot disagree with the layout.
//
// Anything else that still contains the source tenant id is returned as residual
// rather than rewritten. Guessing at an unrecognised shape is how a subtly wrong
// value gets stored; refusing puts the decision in front of the operator, who can
// accept it with --allow-source-tenant-refs once they have looked. The common
// legitimate case is the personal phase's tenant_id == user_id, where an
// owner_user_id genuinely equals the source tenant id.
func restoreRekeyAttrs(attrs map[string]any, source, target snapshotTenantRefs) (map[string]any, int, []string, error) {
	if len(attrs) == 0 {
		return nil, 0, nil, nil
	}
	rewrites := 0
	var residual []string
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		nv, n, res, err := restoreRekeyValue(v, k, source, target)
		if err != nil {
			return nil, 0, nil, err
		}
		out[k] = nv
		rewrites += n
		residual = append(residual, res...)
	}
	sort.Strings(residual)
	return out, rewrites, residual, nil
}

// snapshotChangedCount reports 1 when a rewrite changed the value and 0 when it did
// not.
func snapshotChangedCount(before, after string) int {
	if before == after {
		return 0
	}
	return 1
}

func restoreRekeyValue(v any, path string, source, target snapshotTenantRefs) (any, int, []string, error) {
	switch t := v.(type) {
	case string:
		// A rewrite is counted only when the value actually changes, so a restore into
		// the same tenant reports zero re-keyings rather than a count of values it
		// rebuilt to the identical string. The count is there to tell an operator how
		// much moved; a count that is non-zero on a no-op move teaches them to ignore
		// it.
		switch {
		case t == source.pk:
			return target.pk, snapshotChangedCount(t, target.pk), nil, nil
		case strings.HasPrefix(t, source.s3Prefix):
			rebased := target.s3Prefix + strings.TrimPrefix(t, source.s3Prefix)
			return rebased, snapshotChangedCount(t, rebased), nil, nil
		case source.tenant != target.tenant && strings.Contains(t, source.tenant):
			return t, 0, []string{path}, nil
		}
		return t, 0, nil, nil
	case []any:
		total := 0
		var residual []string
		out := make([]any, len(t))
		for i, e := range t {
			nv, n, res, err := restoreRekeyValue(e, fmt.Sprintf("%s[%d]", path, i), source, target)
			if err != nil {
				return nil, 0, nil, err
			}
			out[i] = nv
			total += n
			residual = append(residual, res...)
		}
		return out, total, residual, nil
	case map[string]any:
		total := 0
		var residual []string
		out := make(map[string]any, len(t))
		for k, e := range t {
			nv, n, res, err := restoreRekeyValue(e, path+"."+k, source, target)
			if err != nil {
				return nil, 0, nil, err
			}
			out[k] = nv
			total += n
			residual = append(residual, res...)
		}
		return out, total, residual, nil
	default:
		return v, 0, nil, nil
	}
}

// restoreItemsIdentical reports whether a record already at the target is the same
// record the archive holds.
//
// Compared through the JSON encoding rather than field by field: encoding/json sorts
// map keys, so two attribute maps that differ only in iteration order compare equal,
// and a nested map or list is covered without this function knowing the shape of
// every entity in §6.3.
func restoreItemsIdentical(a, b repository.Item) bool {
	type comparable struct {
		SK     string         `json:"sk"`
		Attrs  map[string]any `json:"attrs"`
		GSI1SK string         `json:"gsi1sk"`
		TTL    int64          `json:"ttl"`
	}
	ja, err1 := json.Marshal(comparable{SK: a.Key.SK, Attrs: a.Attrs, GSI1SK: a.GSI1SK, TTL: a.TTL})
	jb, err2 := json.Marshal(comparable{SK: b.Key.SK, Attrs: b.Attrs, GSI1SK: b.GSI1SK, TTL: b.TTL})
	if err1 != nil || err2 != nil {
		// Unrepresentable either side: treat as different, because "identical" is the
		// answer that licenses skipping and a skip must never be a guess.
		return false
	}
	return string(ja) == string(jb)
}

// restoreObjectIdentical reports whether the object already at the target has the
// archived object's bytes.
//
// By hash, not by size: two different transcripts of the same audio are very often
// the same length, and the whole value of this check is that it licenses a skip.
func restoreObjectIdentical(ctx context.Context, store snapshotObjectStore, key, want string) (bool, error) {
	body, err := store.Open(ctx, key)
	if err != nil {
		return false, fmt.Errorf("reading the existing target object: %w", err)
	}
	defer body.Close()
	sum, _, err := snapshotHashReader(body)
	if err != nil {
		return false, fmt.Errorf("hashing the existing target object: %w", err)
	}
	return sum == want, nil
}
