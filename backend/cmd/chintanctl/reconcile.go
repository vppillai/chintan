package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

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
	// findingDanglingCapture is a capture row filed into a note that has no
	// row. Before note deletion cascaded through GSI1 (August 2026), deleting a
	// note left its captures behind; the app lists captures through their
	// note, so these are unreachable and cost storage for nothing. It is the
	// one finding whose repair deletes an index row — see runReconcile.
	findingDanglingCapture = "dangling_capture"
)

type finding struct {
	Kind     string `json:"kind"`
	TenantID string `json:"tenant_id"`
	Key      string `json:"key,omitempty"`
	SK       string `json:"sk,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Detail   string `json:"detail"`
	// Objects are the keys --apply deletes along with the row, for findings
	// whose subject is a row rather than an object.
	Objects []string `json:"objects,omitempty"`
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
	return dryRunBanner(stdout, g.apply, res.repairPlan())
}

// repairPlan says what --apply would do, in the words the banner prints.
func (r *reconcileResult) repairPlan() string {
	orphans, dangling, danglingObjects := 0, 0, 0
	for _, f := range r.Findings {
		if !f.Repairable {
			continue
		}
		switch f.Kind {
		case findingDanglingCapture:
			dangling++
			danglingObjects += len(f.Objects)
		default:
			orphans++
		}
	}
	plan := fmt.Sprintf("delete %d orphaned object(s)", orphans)
	if dangling > 0 {
		plan += fmt.Sprintf(" and %d dangling capture row(s) with %d object(s)", dangling, danglingObjects)
	}
	return plan
}

// runReconcile is the documented backstop for the dual-write window and for
// the TTL-expiry cascade: S3 objects are written before their index record, so
// a failed index write orphans data rather than losing it, and something has
// to find the orphan afterwards. hardDeleteNote's TODO points here.
//
// Reporting is the default in both directions. Repair, under --apply, is
// deliberately narrow: it deletes objects whose owning note or capture has no
// index row at all, and capture rows (with their objects) whose note has no
// row. It never deletes a row whose object is missing — that row is the last
// evidence the object existed, and throwing it away turns a recoverable
// incident into an unrecoverable one. A dangling capture is the opposite
// shape: the row is complete and its objects are all there, but the note it
// was filed into is gone, and the app reaches captures only through their
// note, so nothing can ever show it. Keeping it preserves bytes nobody can
// read.
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

		// Captures filed into a note that has no row. Every object under such
		// a capture goes with the row under --apply, whether or not an
		// attribute names it: once the row is gone they would be orphans on
		// the next run anyway.
		dangling := make(map[string][]string)
		for id, c := range idx.Captures {
			if c.NoteID == "" {
				continue
			}
			if _, ok := idx.Notes[c.NoteID]; !ok {
				dangling[id] = nil
			}
		}

		// The present set is keys only — never bodies. It is the one
		// unavoidable accumulation in this command, and it is bounded by the
		// object count, not by the corpus size.
		present := make(map[string]bool)
		var orphans []finding
		err = e.Blobs.List(tctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
			res.Objects++
			present[info.Key] = true
			ref := parseObjectKey(info.Key)
			if ref.Group == "captures" {
				if _, isDangling := dangling[ref.EntityID]; isDangling {
					dangling[ref.EntityID] = append(dangling[ref.EntityID], info.Key)
					return nil
				}
			}
			if _, referenced := refs[info.Key]; referenced {
				return nil
			}
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

		// The other direction: a row whose object is gone. A dangling
		// capture's objects are its own finding, not this one's.
		missing := make([]string, 0)
		for key, owner := range refs {
			if id, ok := strings.CutPrefix(owner, "CAPTURE#"); ok {
				if _, isDangling := dangling[id]; isDangling {
					continue
				}
			}
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

		danglingIDs := make([]string, 0, len(dangling))
		for id := range dangling {
			danglingIDs = append(danglingIDs, id)
		}
		sort.Strings(danglingIDs)
		for _, id := range danglingIDs {
			keys := dangling[id]
			sort.Strings(keys)
			res.Findings = append(res.Findings, finding{
				Kind: findingDanglingCapture, TenantID: tenantID, SK: "CAPTURE#" + id,
				Owner:   "NOTE#" + idx.Captures[id].NoteID,
				Objects: keys,
				Detail: fmt.Sprintf("filed into a note that has no row; %d object(s) unreachable through the app",
					len(keys)),
				Repairable: true,
			})
		}

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
		switch f.Kind {
		case findingDanglingCapture:
			// Objects first, then the row, the order erase uses: a run
			// interrupted between the two leaves a row that the next run
			// finds dangling again and finishes, rather than objects reachable
			// only by a prefix walk.
			for _, key := range f.Objects {
				if err := e.Blobs.Delete(ctx, key); err != nil {
					return res, err
				}
			}
			if err := e.Part.Delete(ctx, tenantPK(f.TenantID), f.SK); err != nil {
				return res, err
			}
			obs.Log(ctx).Info("deleted dangling capture",
				slog.String("sk", f.SK), slog.String("owner", f.Owner), slog.Int("objects", len(f.Objects)))
		default:
			if err := e.Blobs.Delete(ctx, f.Key); err != nil {
				return res, err
			}
			obs.Log(ctx).Info("deleted orphaned object",
				slog.String("key", f.Key), slog.String("owner", f.Owner))
		}
		f.Repaired = true
		res.Repaired++
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
