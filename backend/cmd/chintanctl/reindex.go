package main

import (
	"context"
	"fmt"
	"io"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// reindexResult is what one run reports.
type reindexResult struct {
	Target  target   `json:"target"`
	Apply   bool     `json:"apply"`
	Tenants []string `json:"tenants"`
	Notes   int      `json:"notes_examined"`
	Written int      `json:"notes_reindexed"`
}

func (r *reindexResult) human(w *lineWriter) {
	w.printf("reindex %s (%s)\n", r.Target.Instance, r.Target.Environment)
	w.printf("  tenants:   %d\n", len(r.Tenants))
	w.printf("  notes:     %d\n", r.Notes)
	if r.Apply {
		w.printf("  reindexed: %d\n", r.Written)
	} else {
		w.printf("  would reindex: %d\n", r.Written)
	}
}

// cmdReindex writes the GSI2 key attributes onto notes that lack them.
//
// It exists because adding a global secondary index does not index the rows
// already in the table: DynamoDB backfills a new index only from items that
// carry its key attributes, and every note written before gsi2 existed carries
// neither. Until each one is rewritten they are absent from the index, and the
// notes list — which reads the index, because the base table can only order by
// creation — comes back empty.
//
// So this is not a tidy-up, it is the second half of that deploy. Run it once
// against each instance immediately after the stack update that adds the index.
// It is idempotent, so running it again, or twice at once, costs a write and
// changes nothing.
func cmdReindex(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	fs := newFlagSet("reindex", stderr)
	g.register(fs, true, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runReindex(ctx, e, g.tenants, g.apply)
	if err != nil {
		return err
	}
	if err := report(stdout, g.jsonOut, res); err != nil {
		return err
	}
	return dryRunBanner(stdout, g.apply, fmt.Sprintf("reindex %d note(s)", res.Written))
}

func runReindex(ctx context.Context, e *env, explicitTenants []string, apply bool) (*reindexResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}

	res := &reindexResult{Target: e.Target, Apply: apply, Tenants: tenants}

	for _, tenantID := range tenants {
		// Scanned from the base table on purpose. The index is the thing being
		// repaired, so reading through it would only ever find the notes that
		// do not need repairing.
		err := e.Part.Scan(ctx, "USER#"+tenantID, "NOTE#", func(it Item) error {
			n, err := noteFromItem(it)
			if err != nil {
				return err
			}
			res.Notes++

			pk, sk := repository.NoteIndexKeys(tenantID, n)
			if it.Str("gsi2pk") == pk && it.Str("gsi2sk") == sk {
				// Already indexed, and indexed correctly.
				return nil
			}
			res.Written++
			if !apply {
				return nil
			}

			// The two key attributes only. Rewriting the whole record would
			// bump `version` and hand a client holding the previous one a
			// conflict for a change nobody made.
			next := make(Item, len(it)+2)
			for k, v := range it {
				next[k] = v
			}
			next["gsi2pk"] = AttrValue{S: &pk}
			next["gsi2sk"] = AttrValue{S: &sk}
			return e.Part.Put(ctx, next)
		})
		if err != nil {
			return nil, fmt.Errorf("reindex %s: %w", tenantID, err)
		}
	}

	return res, nil
}
