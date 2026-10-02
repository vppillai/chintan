# History

Dated reports, reviews, QA passes and the owner's queue, kept for reference.
Each describes the system as it was on the day it was written; the code, the
infrastructure and the other documents have moved on since, and links inside
them may point at files that no longer exist. Read `README.md` at the
repository root for the current description of the system, `docs/design/` for
how each part works, `docs/ops/` for the runbooks and `docs/backlog-open.md`
for what is open. Nothing here is maintained.

| Path | What it is |
|---|---|
| `morning-queue.md` | The owner's dated diary: one section per round, newest first, saying what shipped, what waited on a decision and what was decided. Appendable; never rewritten. |
| `ops-log.md` | Dated operator actions against the live account: rehearsals, re-applied bootstrap scripts, deleted secrets, live-eval runs. Appendable. |
| `reviews/` | The review reports, rulings and live QA passes of rounds 3 to 9, one folder per date, with `reviews/README.md` as their index. |
| `reviews/2026-09-26/r5/` | The round-5 measurement notes. Its 2.7 MB of decision renders (PNG) were removed from the tree; `git log --diff-filter=D -- docs/reviews/2026-09-26/r5/` finds them. |
| `2026-09-03-review/` | The 2026-09-03 code, infrastructure and live-account review (`review.md`), with its per-area reports. |
| `prompt-evals.md` | Every run of the live prompt evaluation and of the production routing battery, one line per run, with the per-case outcomes of the 2026-10-01 baseline and the production token measurement of 2026-09-20..26. |
| `2026-09-03-review/ux-proposal-2026-09-03.html` | The 3 September UX proposal, an HTML mock-up of the screens as they were then imagined. |
| `2026-09-04-qa/report.md` | The 4 September exploratory QA pass; its screenshots were removed from the tree (the report says where). |
| `2026-09-04-log-review.md` | The 4 September production log review: volumes, stage durations and the upload stalls it found. |
