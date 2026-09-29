# Reviews

Dated review reports, and the owner's queue. A report describes the system as
it was on the day its lenses ran, and the code has moved since; read
`README.md` at the repository root for the system as it is, `docs/design/` for
how each part is meant to work and `docs/backlog.md` for what is planned. One
file here is maintained: `2026-09-21/morning-queue.md`, the owner-facing
queue, kept current with what shipped, what waits on a decision and what was
decided. Nothing else is.

| File | What it is |
|---|---|
| `2026-09-21/round-3.md` | Round 3: nine lenses on v0.5.16 and two verifiers, reconciled. The findings the 21 September batch worked from. |
| `2026-09-21/morning-queue.md` | The owner's queue: what shipped, the decisions waiting, the round-4 section. Maintained. |
| `2026-09-24/round-4.md` | Round 4: eight lenses on the ten merges of 24 September, verified live. Ten findings and the seven streams that fix them. |
| `2026-09-26/round-5-proposals.md` | Round 5: seven lenses on the owner's 26 September feedback. The ten asks answered (§1), the 24 decisions (§2), the eleven streams that shipped as #111–#125 (§3), the async-updates position and the prompt audit. `r5/` holds the decision renders — logo marks, header variants, disc glyphs; 3.7 MB — to be pruned once the decisions are taken. |
| `2026-09-29/round-6-proposals.md` | Round 6: four lenses (routing, checklist intelligence, one-handed UX, simplify) on the owner's 29 September feedback. The asks answered (§1), the seven decisions (§2), the nine streams that shipped as #142–#162 (§3), the routing numbers (§4), the checklist strategy (§5) and the cleanup plan (§6). |
| `2026-09-29/prod-battery.md` | The production routing battery three times and the items battery once, on the test tenant after #134 deployed: 17 of 22 routing rows three of three, the five that were not and why, the token cost per call. |
| `2026-09-29/qa-r6-live.md` | Round 6 wave 1, verified live on 29 September: swipe between segments, the caret above the keyboard, the Details sheet as a nested scroller, checklist nesting and Split up as the editor, no note multi-select, the Note id in Details, delete-asks-first, the console, and a name-first routing probe through the inbox. All pass; five notes, no bug. |
| `2026-09-29/prod-battery-after-r6.md` | The production batteries after round 6, on the test tenant after #157 and #155 deployed: routing rows 1–32 three times each (22 of 32 three of three, 4 at two of three; rows 1, 16, 21 up, rows 2, 6–9, 14, 15, 17 down, the new name-first rows 8 of 10), the items battery with the tree, dedupe and tick cases (16 of 16), one audio probe, and the six failure shapes with the prompt rule each one misses. |
| `2026-09-29/double-blind-r6.md` | Round 6 double-blind: two blind reviewers per area over five areas of `7f40f09..303dc7d` (#142–#162), reconciled and re-verified on mirrors. 39 findings (6 Medium, 21 Low, 12 nits), 4 refuted, 33 % overlap; the twenty-nine obvious fixes by stream and the ten owner decisions. |
| `2026-09-29/routing-triage.md` | The routing battery triage, reconciled from two independent triages against the raw data: why eight old rows moved down after #157 (the test tenant's duplicated titles and `house` tags for rows 1, 2, 6, 14, 15; one-of-three flips for 7, 8, 9; the prompt for 17, 28, 30), row by row with the evidence; the five do-now items (the owner's purge-and-re-run and the Logs Insights pull of the 21 failed captures; the two code rules and the prompt sentence the `r6/db6-routing` PR ships); the six owner decisions with the recommendation first; and the same-title scoring caveat (raw 21/32 against the reported 22/32). |

The queue names two hands-on passes from 21 September, `qa-final.md` and
`smoke-checklist-capture.md`; neither was committed, and their outcome is in
the queue's own QA section. The round-5 section names `qa-w1-live.md`, the 26
September live pass on prod; it was not committed either, and its outcome is
in that section.
