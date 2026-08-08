package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// Finding kinds. They are stable strings because --json output is something a
// runbook can match on.
const (
	// findingMissingObject is an index row pointing at an object that is not
	// in the bucket. Never repaired automatically: the row is the only
	// remaining record that the object existed.
	findingMissingObject = "missing_object"
	// findingOrphanObject is an object whose owning entity has no index row at
	// all. This is the TTL-expiry cascade's residue, and the case --apply
	// cleans up.
	findingOrphanObject = "orphan_object"
	// findingUnreferencedObject is an object whose owning entity does exist,
	// but which no attribute of that entity names. A new per-capture artifact
	// looks exactly like this before chintanctl learns about it, so it is
	// reported and never deleted.
	findingUnreferencedObject = "unreferenced_object"
	// findingUnknownObject is an object whose key does not fit the layout.
	// Reported, never deleted.
	findingUnknownObject = "unknown_object"
	// findingStuckCapture is a capture left in a non-terminal state.
	findingStuckCapture = "stuck_capture"
)

type finding struct {
	Kind     string `json:"kind"`
	TenantID string `json:"tenant_id"`
	Key      string `json:"key,omitempty"`
	SK       string `json:"sk,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Detail   string `json:"detail"`
	// Repairable is whether --apply would act on this finding.
	Repairable bool `json:"repairable"`
	Repaired   bool `json:"repaired,omitempty"`
}

type reconcileResult struct {
	Target   target    `json:"target"`
	Apply    bool      `json:"apply"`
	Tenants  []string  `json:"tenants"`
	Items    int       `json:"items_checked"`
	Objects  int       `json:"objects_checked"`
	Findings []finding `json:"findings"`
	Repaired int       `json:"repaired"`
}

func (r *reconcileResult) human(w *lineWriter) {
	w.printf("reconcile %s (%s)\n", r.Target.Instance, r.Target.Environment)
	w.printf("  checked %d index items and %d objects across %d tenant(s)\n",
		r.Items, r.Objects, len(r.Tenants))
	if len(r.Findings) == 0 {
		w.printf("  no disagreement between the table and the bucket\n")
		return
	}
	counts := map[string]int{}
	for _, f := range r.Findings {
		counts[f.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		w.printf("  %-22s %d\n", k, counts[k])
	}
	w.blank()
	for _, f := range r.Findings {
		subject := f.Key
		if subject == "" {
			subject = f.SK
		}
		mark := " "
		if f.Repaired {
			mark = "-"
		} else if f.Repairable {
			mark = "*"
		}
		w.printf("  %s %-22s %s  %s\n", mark, f.Kind, subject, f.Detail)
	}
	if r.Apply {
		w.printf("\n  repaired %d finding(s)\n", r.Repaired)
	}
}

func cmdReconcile(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	fs := newFlagSet("reconcile", stderr)
	g.register(fs, true, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runReconcile(ctx, e, g.tenants, g.apply)
	if err != nil {
		return err
	}
	if err := report(stdout, g.jsonOut, res); err != nil {
		return err
	}
	repairable := 0
	for _, f := range res.Findings {
		if f.Repairable {
			repairable++
		}
	}
	return dryRunBanner(stdout, g.apply, fmt.Sprintf("delete %d orphaned object(s)", repairable))
}

// runReconcile is the documented backstop for the dual-write window and for
// the TTL-expiry cascade: S3 objects are written before their index record, so
// a failed index write orphans data rather than losing it, and something has
// to find the orphan afterwards. hardDeleteNote's TODO points here.
//
// Reporting is the default in both directions. Repair, under --apply, is
// deliberately narrow: it deletes only objects whose owning note or capture has
// no index row at all. It never deletes an index row — a row whose object is
// missing is the last evidence the object existed, and throwing it away turns a
// recoverable incident into an unrecoverable one.
func runReconcile(ctx context.Context, e *env, explicitTenants []string, apply bool) (*reconcileResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}
	res := &reconcileResult{Target: e.Target, Apply: apply, Tenants: tenants}

	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)

		idx, err := buildIndex(tctx, e.Part, tenantID, nil)
		if err != nil {
			return nil, err
		}
		res.Items += idx.ItemCount
		refs := referencedKeys(idx)

		// The present set is keys only — never bodies. It is the one
		// unavoidable accumulation in this command, and it is bounded by the
		// object count, not by the corpus size.
		present := make(map[string]bool)
		var orphans []finding
		err = e.Blobs.List(tctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
			res.Objects++
			present[info.Key] = true
			if _, referenced := refs[info.Key]; referenced {
				return nil
			}
			ref := parseObjectKey(info.Key)
			switch ref.Group {
			case "notes":
				if _, ok := idx.Notes[ref.EntityID]; !ok {
					orphans = append(orphans, finding{
						Kind: findingOrphanObject, TenantID: tenantID, Key: info.Key,
						Owner:  "NOTE#" + ref.EntityID,
						Detail: "no index row for the owning note", Repairable: true,
					})
					return nil
				}
				orphans = append(orphans, finding{
					Kind: findingUnreferencedObject, TenantID: tenantID, Key: info.Key,
					Owner:  "NOTE#" + ref.EntityID,
					Detail: "the note exists but names no such key",
				})
			case "captures":
				if _, ok := idx.Captures[ref.EntityID]; !ok {
					orphans = append(orphans, finding{
						Kind: findingOrphanObject, TenantID: tenantID, Key: info.Key,
						Owner:  "CAPTURE#" + ref.EntityID,
						Detail: "no index row for the owning capture", Repairable: true,
					})
					return nil
				}
				orphans = append(orphans, finding{
					Kind: findingUnreferencedObject, TenantID: tenantID, Key: info.Key,
					Owner:  "CAPTURE#" + ref.EntityID,
					Detail: "the capture exists but names no such key",
				})
			default:
				orphans = append(orphans, finding{
					Kind: findingUnknownObject, TenantID: tenantID, Key: info.Key,
					Detail: "key does not fit the tenants/<id>/<group>/<entity>/<file> layout",
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}

		// The other direction: a row whose object is gone.
		missing := make([]string, 0)
		for key := range refs {
			if !present[key] {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		for _, key := range missing {
			res.Findings = append(res.Findings, finding{
				Kind: findingMissingObject, TenantID: tenantID, Key: key,
				Owner:  refs[key],
				Detail: "an index row points at an object that is not in the bucket",
			})
		}

		sort.Slice(orphans, func(i, j int) bool { return orphans[i].Key < orphans[j].Key })
		res.Findings = append(res.Findings, orphans...)

		stuck := make([]string, 0)
		for id, c := range idx.Captures {
			if !terminalStatus(c.Status) {
				stuck = append(stuck, id)
			}
		}
		sort.Strings(stuck)
		for _, id := range stuck {
			res.Findings = append(res.Findings, finding{
				Kind: findingStuckCapture, TenantID: tenantID, SK: "CAPTURE#" + id,
				Detail: fmt.Sprintf("capture is still %q", idx.Captures[id].Status),
			})
		}

		obs.Log(tctx).Info("reconciled tenant",
			slog.Int("items", idx.ItemCount),
			slog.Int("objects", len(present)),
			slog.Int("findings", len(res.Findings)),
		)
	}

	if !apply {
		return res, nil
	}
	for i := range res.Findings {
		f := &res.Findings[i]
		if !f.Repairable {
			continue
		}
		if err := e.Blobs.Delete(ctx, f.Key); err != nil {
			return res, err
		}
		f.Repaired = true
		res.Repaired++
		obs.Log(ctx).Info("deleted orphaned object",
			slog.String("key", f.Key), slog.String("owner", f.Owner))
	}
	return res, nil
}

// terminalStatus reports whether a capture has reached a state the pipeline
// will not move it out of on its own.
func terminalStatus(s model.CaptureStatus) bool {
	switch s {
	case model.StatusAppended, model.StatusFailed, model.StatusNoContent, model.StatusNeedsTarget:
		return true
	case "":
		// A capture row with no status at all is not "in progress"; it is
		// malformed. Reporting it is more useful than assuming.
		return false
	default:
		return false
	}
}
