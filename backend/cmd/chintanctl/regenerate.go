package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
)

// regenerateTask is the worker task this command queues, spelled here rather
// than imported because the pipeline package would pull the provider clients
// into an operator binary; TestRegenerateQueuesTheWorkersTask pins it to
// pipeline.TaskRegenerateNote.
const regenerateTask = "regenerate-note"

// regenerateNote is one note's share of the plan.
type regenerateNote struct {
	NoteID string `json:"note_id"`
	Kind   string `json:"kind"`
	// Captures is how many recordings qualify on the row alone: appended,
	// with a transcript, in a note that is not verbatim. The worker applies
	// the rest of the rule — a plain note's paragraph rewritten by hand is
	// skipped — so this and the cost beside it are upper bounds.
	Captures        int   `json:"captures"`
	TranscriptBytes int64 `json:"transcript_bytes"`
	// CleanedView says the note keeps a whole-note cleaned view, which the
	// worker regenerates once after the last recording lands: one more
	// call, over the whole body, priced into CostMicros from BodyBytes.
	CleanedView bool  `json:"cleaned_view"`
	BodyBytes   int64 `json:"body_bytes,omitempty"`
	CostMicros  int64 `json:"cost_micros"`
	Queued      bool  `json:"queued"`
}

type regenerateTenant struct {
	TenantID   string           `json:"tenant_id"`
	Notes      []regenerateNote `json:"notes"`
	Captures   int              `json:"captures"`
	CostMicros int64            `json:"cost_micros"`
}

type regenerateResult struct {
	Target     target             `json:"target"`
	Model      string             `json:"model"`
	Tenants    []regenerateTenant `json:"tenants"`
	Notes      int                `json:"notes"`
	Captures   int                `json:"captures"`
	CostMicros int64              `json:"cost_micros"`
	Applied    bool               `json:"applied"`
}

func (r *regenerateResult) human(w *lineWriter) {
	w.printf("regenerate %s (%s), priced at openai/%s list price\n", r.Target.Instance, r.Target.Environment, r.Model)
	for _, t := range r.Tenants {
		w.printf("  tenant %s: %s, %s\n", t.TenantID, plural(t.Captures, "recording"), dollars(t.CostMicros))
		for _, n := range t.Notes {
			state := ""
			if n.Queued {
				state = "  queued"
			}
			w.printf("    note %-32s %-10s %3d recording(s) %10s%s\n", n.NoteID, n.Kind, n.Captures, dollars(n.CostMicros), state)
		}
	}
	w.printf("  %d note(s), %s, about %s in cleanup calls\n", r.Notes, plural(r.Captures, "recording"), dollars(r.CostMicros))
	w.printf("  The estimate is the worker's own reservation per call — transcript bytes/4 tokens in and the same out, plus one\n")
	w.printf("  whole-note call over the body of each note that keeps a cleaned view — and an upper bound: the worker skips a\n")
	w.printf("  plain note's paragraph that was rewritten by hand, cleans the view only when a recording landed, and counts what\n")
	w.printf("  it actually ran.\n")
}

type regenerateOptions struct {
	noteID string
	all    bool
	model  string
	apply  bool
	yes    bool
}

func cmdRegenerate(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var o regenerateOptions
	fs := newFlagSet("regenerate", stderr)
	g.register(fs, true, true)
	fs.StringVar(&o.noteID, "note", "", "regenerate this one note of the tenant")
	fs.BoolVar(&o.all, "all", false, "regenerate every active note of the tenant")
	fs.BoolVar(&o.yes, "yes", false, "with --apply: queue without the confirmation prompt")
	fs.StringVar(&o.model, "llm-model", "MiniMax-M3", "the model the instance's worker is configured with (LLM_MODEL), for the estimate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(g.tenants) == 0 {
		return errors.New("--tenant is required")
	}
	if (o.noteID == "") == !o.all {
		return errors.New("pass exactly one of --note <id> or --all")
	}
	if o.noteID != "" && len(g.tenants) != 1 {
		return errors.New("--note takes exactly one --tenant")
	}
	o.apply = g.apply
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	if g.apply {
		if e.Worker, err = resolveWorker(ctx, g); err != nil {
			return err
		}
	}
	res, err := runRegenerate(ctx, e, g.tenants, o)
	if err != nil {
		return err
	}
	if err := report(stdout, g.jsonOut, res); err != nil {
		return err
	}
	return dryRunBanner(stdout, g.apply, fmt.Sprintf("queue %s across %d note(s) for about %s", plural(res.Captures, "recording"), res.Notes, dollars(res.CostMicros)))
}

// runRegenerate plans every named tenant from its rows and its bucket
// listing — no body is read — and, with apply, queues one regenerate-note
// task per note that has something to regenerate, after the operator has
// confirmed the count and the estimate. The worker chooses and resets the
// recordings itself when the task names none (pipeline.RegenerateNote), so
// this command writes nothing to the table.
func runRegenerate(ctx context.Context, e *env, tenants []string, o regenerateOptions) (*regenerateResult, error) {
	res := &regenerateResult{Target: e.Target, Model: o.model, Applied: o.apply}
	for _, tenant := range tenants {
		if err := checkTenantID(tenant); err != nil {
			return nil, err
		}
		idx, err := buildIndex(ctx, e.Part, tenant, nil)
		if err != nil {
			return nil, err
		}
		sizes, err := listObjectSizes(ctx, e.Blobs, tenant)
		if err != nil {
			return nil, err
		}
		plan, err := planRegenerate(idx, sizes, o)
		if err != nil {
			return nil, err
		}
		res.Tenants = append(res.Tenants, plan)
		res.Notes += len(plan.Notes)
		res.Captures += plan.Captures
		res.CostMicros += plan.CostMicros
	}
	if !o.apply {
		return res, nil
	}
	if e.Worker == nil {
		return nil, errors.New("no worker to hand the notes to")
	}
	if res.Captures == 0 {
		return res, nil
	}
	if !o.yes {
		action := fmt.Sprintf("queue %s across %d note(s) for about %s", plural(res.Captures, "recording"), res.Notes, dollars(res.CostMicros))
		if err := confirmTyped(e.Stdin, e.Stdout, "", "yes", action); err != nil {
			return nil, err
		}
	}
	// ponytail: one Event invocation per note, all at once, so a tenant
	// with many notes runs them concurrently and a provider 429 burst leaves
	// `failed` rows for the strip's Retry; the worker's own retry handles a
	// fault, not a provider verdict. A pause between notes, or a cap on the
	// notes per run, when a tenant that large exists.
	for ti := range res.Tenants {
		t := &res.Tenants[ti]
		for ni := range t.Notes {
			n := &t.Notes[ni]
			if n.Captures == 0 {
				continue
			}
			payload, err := json.Marshal(map[string]string{"task": regenerateTask, "tenant_id": t.TenantID, "note_id": n.NoteID})
			if err != nil {
				return nil, err
			}
			if err := e.Worker.Invoke(ctx, payload); err != nil {
				return nil, fmt.Errorf("tenant %s note %s: %w", t.TenantID, n.NoteID, err)
			}
			n.Queued = true
		}
	}
	return res, nil
}

// objectSizes is the size of every recording's raw transcript and of every
// note's body in the bucket, by capture id and by note id, from one listing
// of the tenant's prefix: the estimate needs the bytes and nothing else
// about the text.
type objectSizes struct {
	transcripts map[string]int64
	bodies      map[string]int64
}

func listObjectSizes(ctx context.Context, blobs Blobs, tenantID string) (objectSizes, error) {
	sizes := objectSizes{transcripts: map[string]int64{}, bodies: map[string]int64{}}
	err := blobs.List(ctx, tenantPrefix(tenantID), func(info ObjectInfo) error {
		ref := parseObjectKey(info.Key)
		switch {
		case ref.Group == "captures" && ref.File == "raw.txt":
			sizes.transcripts[ref.EntityID] = info.Size
		case ref.Group == "notes" && ref.File == "note.md":
			sizes.bodies[ref.EntityID] = info.Size
		}
		return nil
	})
	if err != nil {
		return sizes, fmt.Errorf("list objects for tenant %s: %w", tenantID, err)
	}
	return sizes, nil
}

// planRegenerate applies the row half of service.RegenerableCaptures over the
// index: the selected active notes that are not verbatim, and in each the
// appended recordings with a transcript. Each recording is priced as the
// worker reserves for its cleanup call — bytes/4 tokens in, the same out
// (pipeline.estimateTokens), on the LLM model — from meter.DefaultPrices; a
// note that keeps a cleaned view adds the one whole-note call the worker
// makes after its last recording lands, priced the same way over the body.
func planRegenerate(idx *tenantIndex, sizes objectSizes, o regenerateOptions) (regenerateTenant, error) {
	price := func(bytes int64) int64 {
		tokens := float64(bytes)/4 + 1
		return meter.DefaultPrices.Cost("openai", o.model, meter.Quantities{
			meter.UnitInputTokens:  tokens,
			meter.UnitOutputTokens: tokens,
		})
	}
	plan := regenerateTenant{TenantID: idx.TenantID}
	var noteIDs []string
	if o.noteID != "" {
		n, ok := idx.Notes[o.noteID]
		if !ok {
			return plan, fmt.Errorf("tenant %s has no note %s", idx.TenantID, o.noteID)
		}
		if n.DeletedAt != "" {
			return plan, fmt.Errorf("note %s is archived; restore it first", o.noteID)
		}
		noteIDs = []string{o.noteID}
	} else {
		for id, n := range idx.Notes {
			if n.DeletedAt == "" {
				noteIDs = append(noteIDs, id)
			}
		}
		sort.Strings(noteIDs)
	}
	for _, id := range noteIDs {
		n := idx.Notes[id]
		entry := regenerateNote{NoteID: id, Kind: "note"}
		if n.Kind == model.NoteKindChecklist {
			entry.Kind = n.Kind
		}
		if !n.Verbatim {
			for _, cid := range idx.NoteCaptures[id] {
				c := idx.Captures[cid]
				if c.Status != model.StatusAppended || c.RawKey == "" {
					continue
				}
				entry.Captures++
				entry.TranscriptBytes += sizes.transcripts[cid]
				entry.CostMicros += price(sizes.transcripts[cid])
			}
		}
		if entry.Captures == 0 && o.all {
			// Nothing to say about a note nothing would touch.
			continue
		}
		if entry.Captures > 0 && n.CleanedBody != "" {
			entry.CleanedView = true
			entry.BodyBytes = sizes.bodies[id]
			entry.CostMicros += price(entry.BodyBytes)
		}
		plan.Notes = append(plan.Notes, entry)
		plan.Captures += entry.Captures
		plan.CostMicros += entry.CostMicros
	}
	return plan, nil
}
