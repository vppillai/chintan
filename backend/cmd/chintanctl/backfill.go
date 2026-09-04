package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// searchTextAttr is the promoted attribute internal/repository writes
// NoteIndex.SearchText to. Spelled here as well because this command works on
// raw items and has no access to the store's constant; the two are pinned
// together by TestBackfillWritesTheAttributeTheStoreReads.
const searchTextAttr = "search_text"

// Backfill outcomes. Stable strings, because --json output is what a runbook
// matches on.
const (
	backfillCurrent   = "current"   // the row already holds the body's search text
	backfillFilled    = "filled"    // written (or would be, without --apply)
	backfillNoBody    = "no_body"   // the note's markdown object is not in the bucket
	backfillChanged   = "changed"   // the row moved between read and write; not touched
	backfillOversized = "oversized" // the body would put the item over the DynamoDB limit
)

type backfillNote struct {
	TenantID string `json:"tenant_id"`
	NoteID   string `json:"note_id"`
	Outcome  string `json:"outcome"`
	Bytes    int    `json:"search_text_bytes,omitempty"`
}

type backfillResult struct {
	Target  target         `json:"target"`
	Apply   bool           `json:"apply"`
	Tenants []string       `json:"tenants"`
	Notes   []backfillNote `json:"notes"`
	Counts  map[string]int `json:"counts"`
}

func (r *backfillResult) human(w *lineWriter) {
	w.printf("backfill-search-text %s (%s)\n", r.Target.Instance, r.Target.Environment)
	w.printf("  examined %d note(s) across %d tenant(s)\n", len(r.Notes), len(r.Tenants))
	for _, k := range []string{backfillFilled, backfillCurrent, backfillNoBody, backfillChanged, backfillOversized} {
		if n := r.Counts[k]; n > 0 {
			w.printf("  %-10s %d\n", k, n)
		}
	}
	if r.Counts[backfillNoBody] > 0 || r.Counts[backfillChanged] > 0 || r.Counts[backfillOversized] > 0 {
		w.blank()
		for _, n := range r.Notes {
			if n.Outcome == backfillCurrent || n.Outcome == backfillFilled {
				continue
			}
			w.printf("  %-10s NOTE#%s (%s)\n", n.Outcome, n.NoteID, n.TenantID)
		}
	}
}

func cmdBackfillSearchText(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	fs := newFlagSet("backfill-search-text", stderr)
	g.register(fs, true, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runBackfillSearchText(ctx, e, g.tenants, g.apply)
	if err != nil {
		return err
	}
	if err := report(stdout, g.jsonOut, res); err != nil {
		return err
	}
	return dryRunBanner(stdout, g.apply, fmt.Sprintf("write search_text on %d note row(s)", res.Counts[backfillFilled]))
}

// runBackfillSearchText derives NoteIndex.SearchText for every note row from
// the note body in the bucket and writes it where it differs.
//
// It exists for the notes that predate the attribute: the API and the worker
// write search_text on every body write from 2026-09 on, and nothing else ever
// rewrites an untouched note. The write is a conditional SET of the one
// attribute (Partition.Update): it neither rewrites the record blob nor bumps
// the version the API's optimistic concurrency compares, so it can run behind
// a live instance. A row that moves between the read and the write is skipped
// and reported, never overwritten.
//
// The item stays under DynamoDB's 400 KB limit by construction:
// service.SearchText caps its output at model.MaxSearchTextBytes (32 KB) and
// the rest of a note row is a few KB. The oversized outcome exists for the
// case that cannot happen today — a row whose other attributes have somehow
// grown past the budget — so that it is reported rather than failing the run
// with a ValidationException half way through a tenant.
func runBackfillSearchText(ctx context.Context, e *env, explicitTenants []string, apply bool) (*backfillResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}
	res := &backfillResult{Target: e.Target, Apply: apply, Tenants: tenants, Counts: map[string]int{}}

	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)
		var rows []Item
		err := e.Part.Scan(tctx, tenantPK(tenantID), "NOTE#", func(it Item) error {
			rows = append(rows, it)
			return nil
		})
		if err != nil {
			return nil, err
		}

		for _, it := range rows {
			note, err := noteFromItem(it)
			if err != nil {
				return nil, err
			}
			if note.ID == "" {
				note.ID = strings.TrimPrefix(it.SK(), "NOTE#")
			}
			outcome, size, err := backfillOne(tctx, e, tenantID, it, note, apply)
			if err != nil {
				return nil, err
			}
			res.Notes = append(res.Notes, backfillNote{TenantID: tenantID, NoteID: note.ID, Outcome: outcome, Bytes: size})
			res.Counts[outcome]++
		}
		obs.Log(tctx).Info("backfilled search text",
			slog.Int("notes", len(rows)),
			slog.Bool("apply", apply))
	}
	return res, nil
}

// maxBackfillItemBytes is the budget the whole item has to fit in after the
// attribute is added. DynamoDB's limit is 400 KB; the margin covers attribute
// names and encoding overhead the serialised JSON does not show.
const maxBackfillItemBytes = 380 << 10

func backfillOne(ctx context.Context, e *env, tenantID string, it Item, note model.NoteIndex, apply bool) (string, int, error) {
	key := note.S3MarkdownKey
	if key == "" {
		key = "tenants/" + tenantID + "/notes/" + note.ID + "/note.md"
	}
	body, err := e.Blobs.Open(ctx, key)
	if errors.Is(err, errObjectMissing) {
		return backfillNoBody, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	// Bounded: a note body is capped at 1 MiB by the API, and the search text
	// is derived from the first 32 KB of the lowered text anyway, but a body
	// object is read whole because the fold can move rune boundaries.
	raw, err := io.ReadAll(io.LimitReader(body, 2<<20))
	_ = body.Close()
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", key, err)
	}

	want := service.SearchText(string(raw))
	if it.Str(searchTextAttr) == want {
		return backfillCurrent, len(want), nil
	}
	if itemBytes(it)+len(want) > maxBackfillItemBytes {
		return backfillOversized, len(want), nil
	}
	if !apply {
		return backfillFilled, len(want), nil
	}

	set := Item{searchTextAttr: StringAttr(want)}
	if want == "" {
		// An emptied body clears the attribute rather than storing "": the
		// store omits the attribute for an empty search text, and the reader
		// treats absence and emptiness alike.
		set = Item{searchTextAttr: AttrValue{NULL: boolPtr(true)}}
	}
	err = e.Part.Update(ctx, it.PK(), it.SK(), set, it.Num("version"))
	if errors.Is(err, ErrItemChanged) {
		return backfillChanged, len(want), nil
	}
	if err != nil {
		return "", 0, err
	}
	return backfillFilled, len(want), nil
}

// itemBytes approximates an item's stored size from its JSON rendering, which
// is within a few percent of DynamoDB's own accounting for string-heavy items.
func itemBytes(it Item) int {
	n := 0
	for name, v := range it {
		n += len(name)
		switch {
		case v.S != nil:
			n += len(*v.S)
		case v.N != nil:
			n += len(*v.N)
		case v.B != nil:
			n += len(v.B)
		default:
			n += 64
		}
	}
	return n
}

func boolPtr(b bool) *bool { return &b }
