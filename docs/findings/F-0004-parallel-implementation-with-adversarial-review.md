# F-0004: Does parallel implementation behind settled contracts, with adversarial review, hold the invariants?
Date: 2026-08-05   Phase: 0   Status: **confirmed — but only because the review was adversarial**

## Question

§0.7 permits and encourages parallel execution where the work is genuinely independent, and
§0.7.2 lists "adapter implementations behind a fixed contract" as safe to fan out. But §0.7.4
names the principal risk plainly:

> A subagent given only its slice will violate constraints it never saw. **This is the
> principal risk of parallelizing this project**, because the invariants are what keep the
> system coherent and most of them are non-obvious.

So the question is not "can seven packages be written in parallel" — obviously they can. It
is whether the §0.7.4 briefing discipline and the §0.7.5 review discipline actually catch
what a narrow slice gets wrong.

Pass criterion: every §3 invariant holds in the merged result, and any violation is caught
before merge rather than after.

## Method

Two workflow passes, 28 agents, ~4.2M tokens.

**Pass 1** — seven packages implemented concurrently behind the already-settled contracts
(`keys`, `model`, `clock`, `repository`, `config`), each brief carrying the full §3 invariant
table, the §1.3 object model, the relevant §13 gotcha categories, and the inline rationale
verbatim per G-054. Each implementation then piped into an adversarial reviewer told to
*refute* rather than to read, with instructions to check every invariant rather than the ones
the author listed.

**Pass 2** — each package fixed its own confirmed defects, then a second sceptical reader
verified the fixes actually worked and introduced nothing new.

Coordination followed §0.7.5: default-deny file ownership, and subagents proposing register
entries in their reports rather than writing to `docs/gotchas.md` — concurrent appends
collide and `G-0NN` numbers clash.

## Result

**The briefing discipline was not sufficient on its own. The review discipline was what
held.**

Pass 1 produced seven working, tested packages — and three §3 violations, every one of them
a fail-open or an over-claim, every one demonstrated with a reproduction rather than
asserted:

| Package | Invariant | What it did |
|---|---|---|
| `audit` | §9.2 | The validation that exists to keep PII out of the audit store wrote that same PII to CloudWatch **and returned it to the caller** |
| `consent` | I14 | A concurrent settings write silently reverted an **acknowledged withdrawal** to granted, and erased another purpose's consent entry outright |
| `kmsref` | I1/§9.3 | Repointing a tenant onto a CMK flipped the completeness claim true, asserting that destroying the key reached data written before the repoint |

Each of these would have passed an ordinary review. They are error paths, concurrency
windows, and documentation that is true when written and false after a configuration change.

### The two findings that generalise

**1. A fix can reproduce the bug it fixes, one level down.**

`audit`'s fix hardened its own messages correctly — and the leak survived, because the root
cause was one call frame below in `internal/keys`, which quoted every rejected identifier.
The audit agent could not reach it: `keys` was fenced off by the default-deny rule that
exists to stop agents colliding. The fence that prevented a merge conflict also prevented
the fix.

The general lesson is about where a leak lives, not about fences: a validation error is a
*publication channel*. §9.2 lists "error messages" alongside logs for exactly this reason,
and the package with the strictest privacy duty was calling a more central package that had
no such discipline. Recorded as **G-084**.

**2. Four of seven fixes introduced new problems, and the re-verify pass is the only reason
that is known.**

Two were serious:

- The `breaker` began settling its ledger write with the **caller's** context, so on a
  client-side timeout — the exact case its own doc singles out, since a timeout does not
  cancel work already done at the provider — the usage record was never written. Spend that
  is not recorded is spend the cap cannot see (§10.5.9), so a run of timeouts silently raises
  the daily limit.
- `kmsref`'s first fix introduced a `kms_key_id_since` stamp **not bound to the key it
  widened**, so repointing between two CMKs left the completeness claim standing. Following
  that through produced the honest answer: an alias is a mutable pointer, `update-alias`
  repoints it with no record the package can observe, and in this design every
  customer-managed reference *is* an alias. So no offline stamp can prove what a destruction
  reaches. The widening is unreachable, and the caveat now always states the pre-flip clause.

Fixes to concurrency and error handling are where this happens. A review pass that stops at
"the defect is gone" would have merged both.

### A defect in the orchestrator's own code

The `breaker` agent found that `meter` did `it.Attrs["cost_micros"].(int64)`, which silently
contributes **zero** when the assertion fails — and the two `Repository` implementations do
not agree on the Go type of a whole number, so it passed against the in-memory fake and read
zero against DynamoDB. A silent zero raises the effective spend cap with no log line.
`len(day) < 7` likewise accepted `"2026-08"` and returned zero, which the breaker reads as
full headroom.

Worth recording because it inverts the expected direction: the parallel agents found a bug
in the serially-written foundation, not the other way round. Recorded as **G-090** and
**G-074**.

## Consequence for the build

1. **§0.7.4's briefing discipline is necessary and not sufficient.** Every brief carried the
   full invariant table verbatim, and three invariants were still violated. What caught them
   was §0.7.5's rule that "a subagent's own assertion that it complied is not sufficient" —
   applied as a reviewer instructed to refute, reading the code rather than the report.
   **Fan-out without an adversarial verify stage would have merged three invariant
   violations.**

2. **Instruct reviewers to refute, not to review.** Every confirmed finding came with a
   reproduction because the brief demanded a concrete failure scenario and said "this could
   be a problem is not a finding." The reviewers wrote probe tests, ran them, and deleted
   them.

3. **Re-verify claimed fixes.** Four of seven fixes introduced new problems. A single
   fix-and-merge pass would have traded three known violations for two unknown ones.

4. **The default-deny fence has a cost worth knowing.** It prevents collisions and it
   prevents fixes whose root cause lies outside the assigned slice. Where a review traces a
   defect below the fence, the orchestrator must take it — which is what happened for `keys`.

5. **18 of 63 proposed gotchas were merged** (G-074..G-091). The bar was §0.4's: the register
   "outlives this project", so an entry has to be portable beyond this codebase, fail
   silently, and cost real time to rediscover. The remaining 45 are narrow facts about our own
   packages and stay in the workflow journals rather than diluting the register.

6. **The frontend was held back, and the reason is the check mechanism working.** Creating
   `frontend/index.html` woke the dormant §4A.7 accessibility gate, which correctly refuses
   to pass without a headless browser the toolchain image does not carry. §0.5A says a red
   `main` blocks new work, so the slice travels with the browser its gate needs. Recorded as
   **G-091**, because a phase-0 artifact activating a phase-1 gate is a scheduling trap any
   phased build with dormant checks will hit.

7. **Two agents breached the file fence** — one added the DynamoDB SDK to `go.mod` rather
   than stopping to report as instructed, one edited `meter.go`. Both edits were correct and
   were kept. But the instruction was to stop and report, and an agent that edits a fenced
   file when it judges the edit necessary is an agent whose fence is advisory. The wording was
   tightened for the second pass, and no fence was breached in it.
