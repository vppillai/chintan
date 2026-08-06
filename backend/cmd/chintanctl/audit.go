package main

// `chintanctl audit` — query the audit log (§11.4, Phase 0).
//
// §11.4: "Query the audit log by actor, action, resource, or time range." All four
// dimensions live in audit.Query, which is the only read path over audit records; this
// subcommand is argument handling and presentation over it, and `audit.sh` is argument
// handling over this. I16 is why there is no fifth path: an `aws dynamodb query` typed at
// 2am is untested, unaudited, unrepeatable, and has no --dry-run.
//
// # The recursion, handled on purpose
//
// §11.3 requires every data script invocation to write an audit record (I13). For this
// subcommand that means appending to the log it is about to read. Two decisions:
//
//   - **The record is written first**, per audit.Record's contract — a crash between
//     reading the log and recording that read leaves the gap §2A.1 calls unrepairable.
//     openObs does that before this file reads anything.
//   - **That one record is then excluded from the results, and the exclusion is stated in
//     the output.** Excluded because otherwise `audit --limit 1` answers with nothing but
//     itself and every invocation inflates the next one's answer; stated because a query
//     that silently filters records is the wrong tool for a compliance question, and the
//     excluded record's id is printed so it can be looked up directly. Only ever exactly
//     one record, identified by key rather than by a "recent + matching action" heuristic
//     that could hide a real access — see obsIDs for how the id is known.
//
// A previous invocation's record is NOT excluded: reads of the audit log are themselves
// accesses worth seeing, and `--action audit.query` lists them.

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/vppillai/chintan/backend/internal/audit"
	"github.com/vppillai/chintan/backend/internal/model"
)

const auditCmdUsage = `chintanctl audit --tenant <id> [filters]

Query the audit log by actor, action, resource, or time range (§11.4). Append-only
records, never mutated (I13) — this is a read.

Read-only. No --apply, and none needed (§11.3). One audit record is written for the
invocation itself (I13) BEFORE the read, and then excluded from the results and reported
by id, because a query whose answer is dominated by itself answers nothing.

Filters (all optional, all exact-match except the time bounds):
  --actor <id>        e.g. user:u-123, script:usage.sh, system:worker
  --action <verb>     e.g. capture.read, item.update, tenant.erase
  --resource <id>     what was accessed — a sort key or an entity id
  --result <r>        allowed | denied
  --since <bound>     inclusive; yyyy-mm-dd or an RFC3339 UTC timestamp (Z suffix)
  --until <bound>     EXCLUSIVE, same forms, so consecutive windows tile
  --limit <n>         maximum records, 0 for unlimited (default 100)
  --oldest            oldest first; the default is most-recent first

Exit codes: 0 report produced (including zero matches), 1 failure, 2 invocation error.

Examples:
  chintanctl audit --tenant u-123 --config ../config/instances/prod.yaml --limit 20
  chintanctl audit --tenant u-123 --fixtures ../scripts/test/fixtures/observability/records.json \
      --action capture.read --since 2026-08-01 --until 2026-09-01 --json
`

// auditQueryReport is the --json contract.
type auditQueryReport struct {
	Tenant  string            `json:"tenant"`
	Source  string            `json:"source"`
	Filters map[string]string `json:"filters,omitempty"`

	Records []model.Audit `json:"records"`
	Count   int           `json:"count"`

	// Truncated distinguishes "exactly N matches" from "N of more". audit.Page carries it
	// for the same reason: those are different compliance answers and a bare list cannot
	// tell them apart.
	Truncated bool `json:"truncated"`

	// OwnAuditRecord is this invocation's own record (I13), and Excluded says whether it
	// was filtered out of Records above — it is, whenever it fell inside the query.
	OwnAuditRecord string `json:"own_audit_record"`
	OwnExcluded    bool   `json:"own_record_excluded"`
}

func runAudit(args []string) int {
	fs := newFlagSet("audit", auditCmdUsage)
	var f obsFlags
	f.register(fs, "script:audit.sh")
	actor := fs.String("actor", "", "filter by actor, exact match")
	action := fs.String("action", "", "filter by action, exact match")
	resource := fs.String("resource", "", "filter by resource, exact match")
	result := fs.String("result", "", "filter by result: allowed or denied")
	since := fs.String("since", "", "inclusive lower time bound: yyyy-mm-dd or RFC3339 UTC")
	until := fs.String("until", "", "EXCLUSIVE upper time bound, same forms")
	limit := fs.Int("limit", 100, "maximum records to return, 0 for unlimited")
	oldest := fs.Bool("oldest", false, "oldest first (default is most-recent first)")
	if err := fs.Parse(args); err != nil {
		return obsExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "chintanctl audit: unexpected argument %q\n\n%s", fs.Arg(0), auditCmdUsage)
		return obsExitUsage
	}
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "chintanctl audit: --limit cannot be negative; 0 means unlimited")
		return obsExitUsage
	}

	ctx := context.Background()
	// Resource is the collection, not the filters. audit.Access is explicit that a handler
	// acting on a collection names the collection — and the filter values are
	// caller-supplied strings, which have no business entering the longest-retained store
	// in the system (§9.2). The invocation's arguments live in the operator's shell
	// history; the record says the log was read, by whom, and when.
	env, err := openObs(ctx, &f, obsActionAudit, "audit-log")
	if err != nil {
		return obsFail("audit", err)
	}

	// Bounds and the result filter are validated by audit.Query, which is the single
	// authority on their shape — a second opinion here could disagree with it, and a
	// disagreement about a date bound is the kind of wrong-but-plausible answer §11.6
	// refuses elsewhere. That the record is already written when a malformed bound is
	// refused is correct rather than unfortunate: an attempt to read the log is worth
	// recording, which is the same reason §9.1 records a denial as readily as a success.
	rep, err := runAuditQuery(ctx, env, audit.Query{
		Tenant:   env.tenant,
		From:     *since,
		To:       *until,
		Actor:    *actor,
		Action:   *action,
		Resource: *resource,
		Result:   *result,
		Newest:   !*oldest,
	}, *limit)
	if err != nil {
		return obsFail("audit", err)
	}
	rep.Filters = auditFilters(*actor, *action, *resource, *result, *since, *until, *limit, *oldest)

	if f.asJSON {
		if err := obsEmitJSON(os.Stdout, rep); err != nil {
			return obsFail("audit", err)
		}
	} else {
		writeAuditHuman(os.Stdout, rep)
	}
	return obsExitOK
}

// runAuditQuery runs the query, removes this invocation's own record, and re-applies the
// limit.
//
// The limit is raised by one before the query and re-applied after the removal, so a
// `--limit 20` still answers with twenty records rather than nineteen. Without that, the
// self-exclusion would quietly consume one row of every page — most visibly at
// `--limit 1`, where the answer would be empty.
func runAuditQuery(ctx context.Context, env *obsEnv, q audit.Query, limit int) (*auditQueryReport, error) {
	auditor := audit.New(env.repo, env.clk, &obsIDs{}, obsLogger(), 0)
	if limit > 0 {
		q.Limit = limit + 1
	}
	page, err := auditor.Query(ctx, q)
	if err != nil {
		// Wrapped as an invocation error: every failure audit.Query returns is a rejected
		// argument — an impossible date bound, a result filter that is neither value — and
		// those are exit 2, not 1.
		return nil, fmt.Errorf("%w: %v", errObsUsage, err)
	}

	records := make([]model.Audit, 0, len(page.Records))
	excluded := false
	for _, r := range page.Records {
		if r.ID == env.ownAuditID {
			excluded = true
			continue
		}
		records = append(records, r)
	}
	truncated := page.Truncated
	if limit > 0 && len(records) > limit {
		records, truncated = records[:limit], true
	}

	return &auditQueryReport{
		Tenant:         string(env.tenant),
		Source:         env.source,
		Records:        records,
		Count:          len(records),
		Truncated:      truncated,
		OwnAuditRecord: env.ownAuditID,
		OwnExcluded:    excluded,
	}, nil
}

// auditFilters echoes the query back in the output.
//
// Not decoration: "no records matched" and "no records matched THIS query" are different
// statements, and the second is the one an operator can act on. A report that did not
// restate its filters makes a mistyped actor indistinguishable from a clean log.
func auditFilters(actor, action, resource, result, since, until string, limit int, oldest bool) map[string]string {
	f := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	set("actor", actor)
	set("action", action)
	set("resource", resource)
	set("result", result)
	set("since", since)
	set("until", until)
	f["limit"] = fmt.Sprintf("%d", limit)
	if oldest {
		f["order"] = "oldest-first"
	} else {
		f["order"] = "newest-first"
	}
	return f
}

func writeAuditHuman(w io.Writer, rep *auditQueryReport) {
	fmt.Fprintf(w, "audit log — tenant %s\n", rep.Tenant)
	fmt.Fprintf(w, "source: %s\n", rep.Source)
	if len(rep.Filters) > 0 {
		fmt.Fprintf(w, "filters: %s\n", auditFilterLine(rep.Filters))
	}
	fmt.Fprintln(w)

	if rep.Count == 0 {
		// Said plainly, because the empty answer is the one most easily misread. The
		// filters are on the line above so a typo is visible next to the result.
		fmt.Fprintln(w, "no records matched this query.")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ts\tresult\tactor\taction\tresource")
		for _, r := range rep.Records {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.TS, r.Result, r.Actor, r.Action, r.Resource)
		}
		_ = tw.Flush()
		fmt.Fprintf(w, "\n%d record(s)", rep.Count)
		if rep.Truncated {
			// Never silent: a cut list that looked complete would answer a compliance
			// question with a subset.
			fmt.Fprint(w, " — TRUNCATED at --limit; raise it or narrow the window before drawing a conclusion")
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "\naudit record for this invocation: %s", rep.OwnAuditRecord)
	if rep.OwnExcluded {
		fmt.Fprint(w, " (written before this read per I13, and excluded from the rows above)")
	}
	fmt.Fprintln(w)
}

// auditFilterLine renders the filter map in a fixed order, so two runs of the same query
// produce byte-identical output and a diff of two reports shows only what changed.
func auditFilterLine(f map[string]string) string {
	order := []string{"actor", "action", "resource", "result", "since", "until", "limit", "order"}
	out := ""
	for _, k := range order {
		v, ok := f[k]
		if !ok {
			continue
		}
		if out != "" {
			out += " "
		}
		out += k + "=" + v
	}
	return out
}
