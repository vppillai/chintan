package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/vppillai/chintan/backend/internal/obs"
)

type eraseResult struct {
	Target   target `json:"target"`
	TenantID string `json:"tenant_id"`
	Apply    bool   `json:"apply"`

	ItemsPlanned   int   `json:"items_planned"`
	ObjectsPlanned int   `json:"objects_planned"`
	BytesPlanned   int64 `json:"bytes_planned"`

	ItemsDeleted   int   `json:"items_deleted"`
	ObjectsDeleted int   `json:"objects_deleted"`
	BytesDeleted   int64 `json:"bytes_deleted"`

	// SortKeys and ObjectKeys are the exact subjects, so the report is a
	// proof rather than a count. They are identifiers only — no title, no
	// transcript, no note body appears here or anywhere else this command
	// prints.
	SortKeys   []string `json:"sort_keys"`
	ObjectKeys []string `json:"object_keys"`
	// MirrorSortKeys are the tenant's rows in the global WebAuthn credential
	// partition, which live outside the tenant's own partition.
	MirrorSortKeys []string `json:"mirror_sort_keys,omitempty"`
}

func (r *eraseResult) human(w *lineWriter) {
	w.printf("erase tenant %s from %s (%s)\n", r.TenantID, r.Target.Instance, r.Target.Environment)

	groups := map[string]int{}
	for _, sk := range r.SortKeys {
		groups[skGroup(sk)]++
	}
	names := make([]string, 0, len(groups))
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)
	w.printf("  index items (%d):\n", r.ItemsPlanned)
	for _, n := range names {
		w.printf("    %-14s %d\n", n, groups[n])
	}
	if len(r.MirrorSortKeys) > 0 {
		w.printf("    %-14s %d (in the global %s partition)\n",
			"WACRED#mirror", len(r.MirrorSortKeys), credentialListPK)
	}
	w.printf("  objects (%d, %s)\n", r.ObjectsPlanned, humanBytes(r.BytesPlanned))

	if r.Apply {
		w.printf("\n  removed %d index items and %d objects (%s)\n",
			r.ItemsDeleted, r.ObjectsDeleted, humanBytes(r.BytesDeleted))
	}
}

// skGroup is the sort key's kind, for the summary only. It is derived from the
// key, never from a list of kinds this build knows: an sk of a kind added
// tomorrow still appears in the plan under its own name.
func skGroup(sk string) string {
	if i := strings.Index(sk, "#"); i > 0 {
		return sk[:i] + "#"
	}
	return sk
}

func cmdErase(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var tenant, confirm string
	fs := newFlagSet("erase", stderr)
	g.register(fs, false, true)
	fs.StringVar(&tenant, "tenant", "", "tenant id to erase (required)")
	fs.StringVar(&confirm, "confirm", "", "type the tenant id here to skip the interactive prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tenant == "" {
		return fmt.Errorf("--tenant is required")
	}
	if err := checkTenantID(tenant); err != nil {
		return err
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runErase(ctx, e, tenant, g.apply, confirm)
	if err != nil {
		return err
	}
	if err := report(stdout, g.jsonOut, res); err != nil {
		return err
	}
	return dryRunBanner(stdout, g.apply, fmt.Sprintf(
		"irreversibly delete %d index items and %d objects belonging to tenant %s",
		res.ItemsPlanned, res.ObjectsPlanned, tenant))
}

// runErase deletes one tenant everywhere and reports exactly what went.
//
// The plan is built first, in both modes, so the confirmation prompt names a
// real count rather than an abstraction — an operator confirming "delete 0
// objects" has learned something important before pressing return.
func runErase(ctx context.Context, e *env, tenantID string, apply bool, confirm string) (*eraseResult, error) {
	if err := checkTenantID(tenantID); err != nil {
		return nil, err
	}
	ctx = obs.WithTenant(ctx, tenantID)
	res := &eraseResult{Target: e.Target, TenantID: tenantID, Apply: apply}

	// The index is built rather than raw-scanned so the S3 keys the rows
	// reference are known here. Without them, a zero object count is
	// indistinguishable from a tenant who never recorded anything — and this
	// command reported exactly that, "objects deleted: 0", exit 0, while every
	// recording stayed in the live bucket, unreferenced by any index and so
	// invisible to any later erase or export. A deletion request that reports
	// completion and deletes none of the recordings is the worst outcome this
	// tool has.
	credentialSKs := map[string]bool{}
	idx, err := buildIndex(ctx, e.Part, tenantID, func(it Item) error {
		if strings.HasPrefix(it.SK(), credentialSKPrefix) {
			credentialSKs[it.SK()] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.SortKeys = idx.SortKeys
	res.ItemsPlanned = idx.ItemCount

	// repository dual-writes WebAuthn credentials into a global partition, so
	// erasing the tenant partition alone would leave a usable passkey behind
	// pointing at a deleted user. This is the only place chintanctl needs to
	// know about a specific kind, and it is because the storage layout puts
	// this one kind outside the tenant's partition.
	if len(credentialSKs) > 0 {
		err = e.Part.Scan(ctx, credentialListPK, credentialSKPrefix, func(it Item) error {
			if credentialSKs[it.SK()] {
				res.MirrorSortKeys = append(res.MirrorSortKeys, it.SK())
				res.ItemsPlanned++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	err = e.Blobs.List(ctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
		res.ObjectKeys = append(res.ObjectKeys, info.Key)
		res.ObjectsPlanned++
		res.BytesPlanned += info.Size
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Loud, not silent. Index rows naming S3 keys with no objects behind them
	// means this command is pointed at the wrong bucket — which is what
	// happened while it derived the bucket name without the environment.
	if err := requireObjectsForReferencedKeys(e.Target, tenantID, res.ObjectsPlanned, referencedKeys(idx)); err != nil {
		return nil, err
	}

	if !apply {
		return res, nil
	}

	action := fmt.Sprintf("erase tenant %s (%d index items, %d objects) from %s",
		tenantID, res.ItemsPlanned, res.ObjectsPlanned, e.Target.Instance)
	if err := confirmTyped(e.Stdin, e.Stdout, confirm, tenantID, action); err != nil {
		return nil, err
	}

	// Objects first. A run interrupted here leaves index rows pointing at
	// objects that are gone, which reconcile reports loudly as
	// missing_object. The reverse order would leave objects reachable only by
	// prefix walk, which is quieter and easier to miss.
	for _, key := range res.ObjectKeys {
		if err := e.Blobs.Delete(ctx, key); err != nil {
			return res, err
		}
		res.ObjectsDeleted++
	}
	res.BytesDeleted = res.BytesPlanned

	for _, sk := range res.SortKeys {
		if err := e.Part.Delete(ctx, tenantPK(tenantID), sk); err != nil {
			return res, err
		}
		res.ItemsDeleted++
	}
	for _, sk := range res.MirrorSortKeys {
		if err := e.Part.Delete(ctx, credentialListPK, sk); err != nil {
			return res, err
		}
		res.ItemsDeleted++
	}

	obs.Log(ctx).Warn("tenant erased",
		slog.Int("items_deleted", res.ItemsDeleted),
		slog.Int("objects_deleted", res.ObjectsDeleted),
		slog.Int64("bytes_deleted", res.BytesDeleted),
	)
	return res, nil
}
