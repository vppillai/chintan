package main

import (
	"context"
	"fmt"
	"os"

	"github.com/vppillai/chintan/backend/internal/kmsref"
)

// Tenant erasure (§9.3) — the one code path in this system permitted to delete an L0
// transcript.
//
// # The invariant collision, and how it is resolved
//
// I1 says a raw transcript "is never deleted by any application code path", because the
// raw↔edited pair is the training signal for the correction system and losing it to a bug
// is unrecoverable. §9.3 requires right-to-erasure. G-038 is explicit that these two
// "conflict directly and the conflict must be resolved deliberately, not discovered when
// the first erasure request arrives", and names the resolution: scope immutability to
// "never deleted by application code", and carve out a single separately-permissioned
// erasure path that writes an audit record before executing.
//
// This file is that carve-out, and it is the whole of it. What makes it a carve-out rather
// than a hole:
//
//   - It is not reachable from any handler, worker or pipeline stage. It is a subcommand
//     of the admin binary, invoked by one script.
//   - It writes its audit record BEFORE deleting anything, and a failure to write that
//     record aborts the run (recordAccess).
//   - It is separately permissioned: --apply requires the tenant id retyped through
//     --confirm, and erase-tenant.sh refuses to --apply as the agent principal or as root.
//   - It deletes only what it enumerated and printed, so --apply cannot exceed the plan.
//
// # Why the report is as long as it is
//
// G-021: "S3 versioning, DynamoDB PITR, and backups retain copies after an object-level
// delete... Only destroying the encryption key makes data genuinely unrecoverable
// immediately," and its symptom is that a "completed" erasure leaving recoverable data is
// "discovered during audit, not during testing". §9.3 adds that during the personal phase
// there is no customer-managed key (I8), "so crypto-shredding is unavailable. Erasure falls
// back to object deletion plus waiting out the PITR retention window."
//
// So the report cannot claim completeness, and the failure mode is not a missing feature —
// it is an accurate-sounding sentence. Everything this operation does NOT reach is
// enumerated below, in the dry run as well as after the fact, and the crypto-shredding
// sentence is quoted from kmsref rather than composed here so that the claim and the key
// classification cannot drift apart.

const eraseUsage = `chintanctl erase --tenant <id> [--confirm <id>] [flags]

Tenant erasure (§9.3). Deletes every record in the tenant's DynamoDB partition and
every object version under its S3 prefix. **This is the only code path in the
system permitted to delete an L0 transcript (I1, G-038).**

Separately permissioned, and irreversible in the sense that matters: what it
destroys cannot be recovered by this software. It writes an audit record BEFORE
executing, and reports both what it removed and — at least as importantly — what
SURVIVES. During the personal phase there is no customer-managed key (I8), so
crypto-shredding is unavailable and erasure is object deletion plus waiting out the
retention windows; the report says so rather than claiming completeness it does not
have (G-021).

--dry-run is the DEFAULT (§11.3). --apply executes, and additionally requires
--confirm <tenant-id> with the tenant id retyped.

Idempotent: re-running converges. The partition never becomes empty, by design —
this operation's own audit record (I13) and metering record (I12) are written after
the inventory is taken and therefore survive it.

Live run:      --instance <name> --account <id> --region <r>
Test run:      --fixtures <path>   (no AWS, no credentials — §11.5)

Examples:
  chintanctl erase --tenant personal --instance prod --account 123456789012 --region ca-central-1
  chintanctl erase --tenant personal --instance prod --account 123456789012 \
      --region ca-central-1 --confirm personal --apply
`

// eraseResult is the --json document; the human report renders from the same struct.
type eraseResult struct {
	OK        bool   `json:"ok"`
	Operation string `json:"operation"`
	DryRun    bool   `json:"dry_run"`
	Tenant    string `json:"tenant"`

	// Plan is the inventory: exactly what --apply will destroy, and nothing else.
	// Identical between a dry run and an --apply of the same state (§11.5).
	Plan inventory `json:"plan"`

	// Shredding is the honest answer to "is this unrecoverable", from kmsref.
	Shredding shredStatus `json:"crypto_shredding"`

	// Removed is zero in a dry run, and is what actually went in an --apply. Reported
	// separately from the plan because §9.3 requires erasure to report what it removed,
	// which is not the same as what it intended to remove.
	Removed removedCounts `json:"removed"`

	// Survives enumerates what remains reachable after this operation. The section that
	// matters most (G-021).
	Survives []string `json:"survives"`

	Failures []deleteFailure `json:"failures,omitempty"`
	Cost     costBasis       `json:"cost_estimate"`
	Notes    []string        `json:"notes"`
}

// shredStatus is kmsref's answer, carried into the report field by field.
//
// Copied out rather than summarised into a boolean: CryptoShreddable alone is "necessary,
// not sufficient" (kmsref), and a report that printed only the boolean would say
// "unrecoverable" for a key whose destruction reaches neither the tenant's pre-repoint
// objects nor any DynamoDB item.
type shredStatus struct {
	Available          bool   `json:"available"`
	KMSKeyID           string `json:"kms_key_id"`
	KeyKind            string `json:"key_kind"`
	CoversAllObjects   bool   `json:"covers_all_objects"`
	CoversDynamoDB     bool   `json:"covers_dynamodb"`
	PreFlipBoundary    string `json:"pre_flip_boundary,omitempty"`
	ProvenanceDefect   string `json:"provenance_defect,omitempty"`
	Caveat             string `json:"caveat"`
	KeyDestroyedByThis bool   `json:"key_destroyed_by_this_operation"`
}

// removedCounts is what an --apply actually destroyed.
type removedCounts struct {
	Records        int   `json:"records"`
	ObjectVersions int   `json:"object_versions"`
	DeleteMarkers  int   `json:"delete_markers"`
	Bytes          int64 `json:"bytes"`
}

func runErase(args []string) int {
	fs := newFlagSet("erase", eraseUsage)
	var o dataOpts
	registerDataFlags(fs, &o)
	confirm := fs.String("confirm", "", "the tenant id, retyped; required with --apply")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, eraseUsage)
		return 2
	}
	if err := erase(context.Background(), o, *confirm); err != nil {
		return fail(err, o.asJSON)
	}
	return 0
}

func erase(ctx context.Context, o dataOpts, confirm string) error {
	// The confirmation is checked BEFORE the stores are opened, so a mistyped invocation
	// costs nothing and reads nothing. It is checked here rather than only in
	// erase-tenant.sh because the binary is the implementation: a gate that lives solely
	// in the wrapper is a gate that calling the binary directly walks past (§11.2 puts
	// argument parsing in bash, not the safety property itself).
	if o.apply && confirm != o.tenant {
		return refusedErrorf("--apply requires --confirm with the tenant id retyped; got %q. "+
			"This deletes every record and every object version for the tenant, including L0 transcripts (I1, §9.3), and nothing in this software can restore them",
			confirm)
	}

	td, err := openTenantData(ctx, o, newCLILogger())
	if err != nil {
		return err
	}
	return eraseTenant(ctx, td, o.apply, o.asJSON)
}

// eraseTenant is the operation itself, separated from argument handling so that a test can
// supply its own stores — including ones that fail on demand, which is the only way to
// assert that a failed audit write deletes nothing (§11.5, I13).
func eraseTenant(ctx context.Context, td *tenantData, apply, asJSON bool) error {
	inv, err := takeInventory(ctx, td)
	if err != nil {
		return err
	}

	res := eraseResult{
		OK:        true,
		Operation: "erase",
		DryRun:    !apply,
		Tenant:    string(td.tenant),
		Plan:      inv,
		Shredding: shredStatusOf(td.scope),
		Survives:  survives(td, inv),
		Cost: costBasis{
			GetRequests:   inv.APIRequests,
			TransferBytes: 0,
			ProviderSpend: "none — no provider is called, so the daily spend breaker (§10.5.9) does not apply. Erasure reduces standing storage cost",
		},
		Notes: eraseNotes(td, inv),
	}

	// §9.3: "writes an audit record before executing". Not after, and not best-effort —
	// recordAccess aborts the run if the write fails, so an erasure with no record of it
	// cannot happen.
	action := "tenant.erase"
	if !apply {
		action = "tenant.erase.plan"
	}
	if err := recordAccess(ctx, td, action); err != nil {
		return err
	}

	if apply {
		// Objects before records. Both orders converge on a re-run — the enumerations are
		// by prefix and by partition, not by reference, so neither store's contents depend
		// on the other's — so the tiebreaker is which loss matters more if the run dies
		// half way: §9.2 calls voice recordings "among the most sensitive content
		// categories a product can hold", and audio and transcripts live in S3.
		failures, err := td.objects.DeleteVersions(ctx, inv.Objects)
		if err != nil {
			return err
		}
		res.Failures = append(res.Failures, failures...)
		failed := make(map[string]bool, len(failures))
		for _, f := range failures {
			failed[f.VersionID] = true
		}
		for _, v := range inv.Objects {
			if failed[v.VersionID] {
				continue
			}
			if v.DeleteMarker {
				res.Removed.DeleteMarkers++
				continue
			}
			res.Removed.ObjectVersions++
			res.Removed.Bytes += v.Bytes
		}

		for _, it := range inv.items {
			// **The key deleted is the key that was read** — it.Key, verbatim, from the
			// tenant-partition query. Not reassembled from a prefix and a sort key: the
			// keys package holds the monopoly on constructing keys (I11), this code is not
			// permitted to construct one, and rebuilding a key in order to delete it would
			// be exactly the hand-built key check-tenant-keys.sh exists to catch.
			//
			// The partition check is defence in depth against a future edit that widened
			// the read. Nothing today can produce an item from another partition — the
			// query is keyed on this tenant — and if one ever appeared, deleting it would
			// be the worst possible response.
			if it.Key.PK != td.pk {
				res.Failures = append(res.Failures, deleteFailure{
					Key:    it.Key.SK,
					Reason: "record is not in this tenant's partition; refusing to delete it (I11)",
				})
				continue
			}
			if err := td.repo.Delete(ctx, it.Key); err != nil {
				res.Failures = append(res.Failures, deleteFailure{Key: it.Key.SK, Reason: err.Error()})
				continue
			}
			res.Removed.Records++
		}
	}

	if err := meterRequests(ctx, td, action, td.objects.Requests()); err != nil {
		return err
	}
	res.Cost.GetRequests = td.objects.Requests()

	if len(res.Failures) > 0 {
		res.OK = false
	}
	if asJSON {
		emitReportJSON(res)
	} else {
		printErase(res)
	}
	if len(res.Failures) > 0 {
		// Non-zero, and specifically not the refusal code: something happened, partially.
		// Re-running converges, and the operator needs to know a run ended in that state
		// rather than reading "erased" and closing the ticket.
		return fmt.Errorf("%d deletion(s) failed; the erasure is incomplete — re-run to converge, and check the principal's permissions (§9.3)", len(res.Failures))
	}
	return nil
}

// shredStatusOf carries kmsref's answer into the report.
func shredStatusOf(scope kmsref.ErasureScope) shredStatus {
	return shredStatus{
		Available:        scope.CryptoShreddable(),
		KMSKeyID:         scope.Ref().ID(),
		KeyKind:          string(scope.Ref().Kind()),
		CoversAllObjects: scope.CoversAllObjects(),
		CoversDynamoDB:   scope.CoversDynamoDB(),
		PreFlipBoundary:  scope.PreFlipBoundary(),
		ProvenanceDefect: scope.ProvenanceDefect(),
		Caveat:           scope.Caveat(),
		// **False, always, and it is not a placeholder.** This operation deletes objects
		// and records; it does not call kms:ScheduleKeyDeletion. Destroying a key is a
		// different act with a different blast radius — it is irreversible for every
		// tenant pointed at that key, and in the personal phase every tenant points at the
		// same AWS-managed key that the account cannot delete anyway (I8). When a CMK
		// exists, scheduling its deletion belongs to a provisioning script that owns the
		// key's lifecycle, with its own dry run (I16). Reporting the field explicitly is
		// what stops "crypto-shredding is available" being read as "crypto-shredding
		// happened" — which is the G-021 over-claim in its most plausible form.
		KeyDestroyedByThis: false,
	}
}

// survives enumerates everything still reachable after this operation completes.
//
// **This list is the honesty requirement, and every entry is load-bearing.** G-021's
// symptom is a completed erasure that left recoverable data, "discovered during audit, not
// during testing" — which happens when a report states what was deleted and stays silent
// about the rest. Each entry below names a store, why this operation does not reach it, and
// what would.
func survives(td *tenantData, inv inventory) []string {
	var out []string

	// The crypto-shredding sentence, quoted from kmsref rather than composed here: the
	// classification and the claim must not be able to drift apart, and kmsref's own review
	// found that over-claiming at exactly this point is the easy mistake.
	out = append(out, "Crypto-shredding: "+td.scope.Caveat())

	out = append(out,
		"DynamoDB point-in-time recovery is ENABLED on this table (infrastructure/template.yaml). A restore to any instant inside the PITR window recovers every record this operation deleted. Erasure of the records is not final until that window lapses — the window is AWS's, up to 35 days, and nothing in this software shortens it (§9.3, G-021).",

		"DynamoDB on-demand backups, if any exist, are not enumerated or deleted here. This operation does not call ListBackups: a backup is a deliberate operator artifact and deleting one as a side effect of a tenant erasure is a decision an operator makes, not a script.",

		"S3 noncurrent versions and delete markers under this prefix ARE destroyed, version by version — which is the only reason the S3 side of this can ever become final. Note what that implies about the alternative: the bucket has versioning enabled and no NoncurrentVersionExpiration rule, so a plain DeleteObject would have retained every previous version indefinitely, and §9.3's 'wait out the retention window' would have had no window to wait out (G-021).",

		"In-flight S3 multipart uploads under this prefix are not enumerated, so any partially-uploaded object is untouched. The bucket's AbortIncompleteMultipartUpload rule clears them 7 days after initiation (infrastructure/template.yaml); until then the parts persist.",

		"The Telegram sender→tenant link record is partitioned by the sender's id rather than by tenant (keys.TelegramLink), so no tenant-qualified query can reach it and this operation does not delete it. It holds a tenant and user reference and no user content. Removing it is telegram-link.sh's job (Phase 6).",

		"Cognito identities are untouched. The user can still sign in; they would simply have no data. Removing the account is users.sh remove.",

		"CloudWatch log groups are untouched. §9.2 keeps transcript content, audio and PII out of logs, so what remains is identifiers and operational metadata, retained for retention.log_group_days (§7.4).",

		"Any record written to this partition after the inventory above was taken — including this operation's own audit record (I13) and metering record (I12) — is not in the plan and survives. That is why erasure is idempotent: re-run until the plan is empty. It never becomes empty, because each run writes its own attestation; the fixed point is that attestation and nothing else.",

		"Any export archive taken earlier (export-tenant.sh) is a plaintext copy outside this system entirely. Nothing here can find or delete it.",
	)

	if inv.empty() {
		out = append(out, "This tenant's partition and prefix were already empty when the inventory was taken, so this run had nothing to remove. That is the idempotent case, not an error.")
	}
	return out
}

// eraseNotes are the operational caveats that are not survival claims.
func eraseNotes(td *tenantData, inv inventory) []string {
	notes := []string{
		"This is the only code path permitted to delete an L0 transcript (I1, G-038). Every other delete in the system is a soft delete.",
		"Nothing is quiesced first. A capture in flight while this runs may write records after the inventory was taken; they survive, and a second run removes them.",
	}
	if len(inv.Records) > 0 || len(inv.Objects) > 0 {
		notes = append(notes, fmt.Sprintf("Take an export first if the erasure is a data-portability request as well as a deletion request: export-tenant.sh reads the same inventory this plan was built from (%d records, %d object versions).", len(inv.Records), len(inv.Objects)))
	}
	if td.fixtures {
		notes = append(notes, "FIXTURE RUN: no AWS account was touched. Counts describe the fixture set, not a deployment.")
	}
	return notes
}

// ---------------------------------------------------------------------------
// Human-readable report
// ---------------------------------------------------------------------------

// objectSampleMax bounds how many object keys the human report prints in full.
//
// The complete list is always in --json. A terminal report that printed 40,000 keys is one
// nobody reads to the end, and the part an operator must read — what survives — would be
// off the top of the scrollback.
const objectSampleMax = 20

func printErase(res eraseResult) {
	if res.DryRun {
		fmt.Fprintf(reportOut, "DRY RUN — nothing was deleted.\n\n")
	}
	fmt.Fprintf(reportOut, "tenant:       %s\n", res.Tenant)
	fmt.Fprintf(reportOut, "table:        %s\n", res.Plan.Table)
	fmt.Fprintf(reportOut, "object store: %s\n", res.Plan.ObjectStore)
	fmt.Fprintf(reportOut, "prefix:       %s\n\n", res.Plan.ObjectPrefix)

	verb := "WOULD DESTROY"
	if !res.DryRun {
		verb = "DESTROYED"
	}
	fmt.Fprintf(reportOut, "%s\n", verb)
	fmt.Fprintf(reportOut, "  %d record(s) in %d class(es):\n", len(res.Plan.Records), len(res.Plan.RecordClasses))
	for _, c := range res.Plan.RecordClasses {
		fmt.Fprintf(reportOut, "    %-12s %d\n", c.Class, c.Count)
	}
	fmt.Fprintf(reportOut, "  %d object version(s) — %d current, %d noncurrent, %d delete marker(s) — %s\n",
		len(res.Plan.Objects), res.Plan.CurrentObjects, res.Plan.NoncurrentVersions,
		res.Plan.DeleteMarkers, humanBytes(res.Plan.TotalBytes))
	shown := 0
	for _, v := range res.Plan.Objects {
		if shown >= objectSampleMax {
			fmt.Fprintf(reportOut, "    ... and %d more (the complete list is in --json)\n", len(res.Plan.Objects)-shown)
			break
		}
		kind := "version"
		switch {
		case v.DeleteMarker:
			kind = "delete-marker"
		case v.IsLatest:
			kind = "current"
		}
		fmt.Fprintf(reportOut, "    %-13s %s\n", kind, v.Key)
		shown++
	}

	if !res.DryRun {
		fmt.Fprintf(reportOut, "\nremoved: %d record(s), %d object version(s), %d delete marker(s), %s\n",
			res.Removed.Records, res.Removed.ObjectVersions, res.Removed.DeleteMarkers, humanBytes(res.Removed.Bytes))
	}

	fmt.Fprintf(reportOut, "\nWHAT SURVIVES THIS OPERATION\n")
	for _, s := range res.Survives {
		fmt.Fprintf(reportOut, "  - %s\n", s)
	}

	fmt.Fprintf(reportOut, "\nnotes:\n")
	for _, n := range res.Notes {
		fmt.Fprintf(reportOut, "  - %s\n", n)
	}

	if len(res.Failures) > 0 {
		fmt.Fprintf(reportOut, "\nFAILURES (%d) — the erasure is incomplete:\n", len(res.Failures))
		for _, f := range res.Failures {
			fmt.Fprintf(reportOut, "  - %s %s: %s\n", f.Key, f.VersionID, f.Reason)
		}
	}

	if res.DryRun {
		fmt.Fprintf(reportOut, "\nRe-run with --apply --confirm %s to execute. This cannot be undone by this software.\n", res.Tenant)
	}
}
