package main

import (
	"context"
	"fmt"
	"io"
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
//
// The write itself is the store's, not this tool's. Partition.Put overwrites an
// item verbatim, so a Scan-then-Put here would discard any edit made between
// the two — and this is run against a live instance while somebody is using it.
// repository.ReindexNotes sets the two key attributes conditionally and leaves
// `version` alone, so an editor open in another tab is unaffected.
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
		// Counted from the base table, because the index is the thing being
		// repaired: reading through it would only ever find the notes that do
		// not need repairing.
		if err := e.Part.Scan(ctx, "USER#"+tenantID, "NOTE#", func(Item) error {
			res.Notes++
			return nil
		}); err != nil {
			return nil, fmt.Errorf("reindex %s: count notes: %w", tenantID, err)
		}

		if !apply {
			// Without the writes there is nothing to count but the candidates,
			// and every note is a candidate until it has been examined.
			res.Written += res.Notes
			continue
		}

		written, err := e.Notes.ReindexNotes(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("reindex %s: %w", tenantID, err)
		}
		res.Written += written
	}

	return res, nil
}
