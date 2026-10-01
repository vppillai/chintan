# Round 9 review — docs-process lens (DOC-n)

Main reviewed: `53bf8a23`; the read-only checkout's HEAD is `c636993b` (#207), one commit later — noted where it changes a verdict. Read-only; nothing edited, no PRs, no comments. Date: 1 Oct 2026.

Scope: does the written record still tell the truth after rounds 6–8, and does it still serve the owner. Method: every claim was checked against code, `git log`, `gh`, or measured with a 60-line script over `docs/backlog.md` (scratchpad only). Three sub-reviewers spot-checked the design docs (frontend, backend), README and openapi.yaml; ~130 symbols and 120+ file paths were resolved, and every number the docs state was compared with the constant in code.

| Severity | Ids |
|---|---|
| High | DOC-1, DOC-2, DOC-3 |
| Medium | DOC-4, DOC-5, DOC-6, DOC-7, DOC-8, DOC-9 |
| Low | DOC-10, DOC-11, DOC-12, DOC-13 |

The headline: the code-level record is honest — the design docs and openapi are far closer to the code than three fast rounds would suggest. What has decayed is the *queue*: the backlog cannot answer "what is open", the gate the owner believes in is opened by a script, and the know-how that runs a round is on one laptop outside git.

## Findings

### DOC-1 (High) — `docs/backlog.md` has no current state; at least 21 of its 62 non-done rows are finished and still read open

Evidence (measured at 53bf8a23: 727 lines, 554 data rows in 32 tables under 20 `##` sections; 492 `done`, 62 other):

| Row (line) | Status shown | Truth |
|---|---|---|
| T22 (L278) | `open — PR #70, merges after the S6 front` | #70 merged 2026-09-21 09:32Z (`gh pr view 70`). |
| PR-2, PR-4 (L435) | `**gated** (owner)` | Shipped in #134 (`b21313fd` "routing rewrite … PR-2, PR-4"). The row points at `docs/design/prompts/proposed/`, which #134 deleted. |
| PR-D1–PR-D5 (L436) | `**open** (owner decisions)` "Nothing built." | Decided 27 Sept (L492–L496), built in #134. |
| CL-D1, CL-D2, R5-RC-D1, R5-RC-D2, PR-D1…D5, WH-A/B/C/E, CH-O2, CH-O3 (L409–L470; 15 rows) | `**queued** (owner)` | Each has a `done`/`declined` row under "Round 5 decisions" (L478–L496); CL-D1 re-decided again by R8-F1a (L705). |
| CL-P2 (L411) | `next` | The nesting editor shipped (#154, #194). |
| CL-P3 (L412) | `next` | Backspace-at-start merge exists (`frontend/src/features/notes/ChecklistRow.tsx:97` → `onBackspaceEmpty`); row never moved. |
| OF-M (L349) | `**open** → follow-up PR` | Second OF-M row (L369) is `done`. |
| R6-RT-8 (L636), R7-10a (L652) | `**done** (prompt; WITHOUT the live eval)` | Both reverted (L639, L693); the `done` rows carry no pointer forward. |

The legend (L3) says status is one of eight words; rows use eighteen (`queued (owner)` ×25, `open` ×10, `gated`, `reverted` in three spellings, `reversed`, `deferred`, `asked`, `not reproduced`, `closed — not a fault`, …). Rows per round: R3 99 · R4 43 · R5 77+20 · R6 67 · R7 44 · R8 26; commits touching the file per working day: 15, 8, 27, 11, 21, 25. The round-9 brief itself had to say "grep Round 7 and Round 8 rows marked open/deferred/asked" because there is no other way in.

Why it is debt: the owner and every next agent read the same 62 rows and a third of them are wrong; the cost is paid in every brief and every "do not re-report" clause. Fix: see "Proposed backlog structure" (S). Owner decision only on step 4 (edit the stale status cells in place vs append corrections).

### DOC-2 (High) — The production "human gate" is approved by a script; the record says the owner approves

Evidence (traced): `.github/workflows/deploy-backend.yaml:6-7` "the production job is behind an environment that requires a human"; `README.md:47-48` "production waits for your approval on the `production` environment"; `scripts/setup.sh:25,193` "GitHub user who must approve a production deploy" (default reviewer = repo owner, L71). In practice `.agent-tools/gate.sh` polls `pending_deployments` and POSTs `state=approved` with comment "approved by the agent under the owner's standing instruction", using the owner's `gh` token and hard-coded `ENV_ID=19433873960`; `merge-seq.sh` runs it after every merge (optional for `fe`). Every "merged behind CI, the staging smoke and the prod gate" in the queue (29 Sept, 30 Sept ×2) is a gate the agent opened within ~20 s. `grep -ri "standing instruction"` over README, docs and workflows: zero hits; it exists only in the script's default comment and the agent's memory file.

Why it is risk: the control the owner believes exists and the control that exists differ, and the environment's approval log shows the owner's login on every approval. Fix (S, owner decision): (a) record the rule in the repo (`docs/contributing.md` "Deploy gate": who may approve, under what condition — green CI + staging smoke — and that agents do so during a round; make `gate.sh` cite it), or (b) stop auto-approving and have the queue list the deploys waiting.

### DOC-3 (High) — Nothing in the repo lets the next agent or human run a round

Evidence: no `CLAUDE.md`, `.claude/`, `AGENTS.md` or `docs/contributing*`. The round mechanics live in `/Users/vpillai/Downloads/chintan/.agent-tools/`: `merge-seq.sh` (rebase, wait for CI, squash, pull, gate; encodes the 27 Sept lesson that an `fe` PR touching `backend/**` still triggers a backend deploy), `stack-merge.sh` (retarget uppers to `main` before the bottom merges — the #152 lesson), `gate.sh`, `resolve-known.py` (which files are append-only), `mk-sync.sh`, the per-round `brief.md` (toolchain exports, gates, wire-change procedure, security rules), the r8 specs (100 KB). Gotchas only in agent memory: orb `~/.local/bin/node` is a shim that `exec`s bun; Playwright on orb needs `CI=1` (`frontend/playwright.config.ts:93` `reuseExistingServer: !CI` attaches to another stream's preview otherwise); `setup.sh` needs the Mac's `vppillai` token piped over stdin because orb's `gh` is `vpillaiTT`; `docs/backlog.md` and `docs/reviews/README.md` are append-only and resolved keep-both; the main checkout is never edited; at most 3 agents after two Mac restarts; `CHINTAN_UPDATE_FIXTURES=1` after any merge from main; Mac `grep` is ugrep, no `timeout`. README "Develop" (L267–L300) documents CI and gates well, but not any of this. Memory records the scratchpad being wiped twice; `.agent-tools` is one directory away from the same.

Fix (M): move the five scripts to `scripts/dev/` (CI already shellchecks `scripts/**`; the hard-coded `/Users/vpillai/...` and `ENV_ID` become arguments); write a `CLAUDE.md` ≤ 80 lines with the items above plus the gate rule from DOC-2; the per-round brief becomes "read CLAUDE.md plus this round's streams". No owner decision beyond DOC-2.

### DOC-4 (Medium) — `resolve-known.py`'s keep-both resolution manufactures duplicate rows and headers

Evidence (measured): two `### S8` headings (L558 "Wave 2, frontend pure moves", L564 "Frontend pure moves"); a second `| # | Item | Status | Notes |` header inside Round 8 (L703–L704) with nothing under it; 60 ids on more than one row (113 rows). 37 of those are legitimate halves/decision pairs; 12 are id reuse across rounds (`F2`, `F4`, `F5`, `H5`, `Q1`–`Q12` reused by Round 3 for items unrelated to the QA-pass `Q` rows); 11 are same-section near-duplicates with no `Correction:`/`Review of` marker on either: R7-11 (L665/L666, diverge at col 433), R8-F2 (L717/L718, col 746: "Not done: the Archived chip still replaces" vs "the Archived chip pushes"), R8-S1 (L725/L726, col 841: "the pull-to-refresh guard still holds because the body is the nested scroller" vs "now excludes the whole `.note-drawer`"), C7, R5-BR-H1, R7-6a, R7-6b, R7-13 ×3, DB6-1, R7-21. A reader cannot tell which of a pair is current.

Fix (S): in `resolve-known.py`, after keep-both drop a line byte-identical to one already in the file and a header row directly after a header row; convention that a second row for an id starts with `Correction:`/`Review of #N:`/`Superseded by`; the generated view (DOC-1) takes the last row per id. No owner decision.

### DOC-5 (Medium) — `done` rows that still owe a gate, recorded for R6 only

| Row | Owed | Owner? |
|---|---|---|
| R6-CL-1 (L548) → R6-CLOSE-1 (L645) | `TestLiveEval -count=3` route eval (key in SSM) | owner |
| R6-NAV-2 (L522), DB6-5 (L605) | Pixel + iPhone caret/keyboard check | owner |
| R6-NAV-1 (L523) | pen-device check | owner |
| R7-9 (L655) ◦ | record the replay set and commit it | owner |
| R8-F5 (L708), R8-F3 (L719) ◦ | iPhone loupe / banner-mic checks | owner |
| Tidy prompt (queue 30 Sept evening, item 3) | `TestLiveEval/(items\|tasks) -count=3` | owner |
| R6-RT-8 / R7-10a | re-proposal gated on `-count=10` | agent, after the owner records |

(◦ = known per the brief.) Two prompt changes shipped "WITHOUT the live eval" and both regressed on the post-deploy battery; the eval is the only check that catches prompt drift and the owner is the only one who can run it. Fix (S): a `gate:` token in the Status cell and a "Done, gate owed" section in the generated view; collapse the three eval runs into one command the queue repeats until run.

### DOC-6 (Medium) — `prompts.md` and the design index describe a replay/eval gate that does not exist

Evidence (sub-reviewer, verified): `docs/design/prompts.md:482-484` "the command above fills `backend/internal/provider/testdata/eval/recordings/`, which is committed" — the directory has never existed (`git log -- …/recordings` empty; `testdata/eval` holds `fixtures.json`, `prod-battery.md`). `:473-474` "Record and replay test the two together in CI without the key" — `pipeline.TestRoutingEvalReplay` hits its `t.Skip` on every run. `:495-498` "after any change to `routing.SystemPrompt` … the replay misses and fails" — it skips. `docs/design/README.md:23` "the live evaluation that gates a change" — no workflow sets `LIVE_LLM`, `LLM_API_KEY` or `LLM_REPLAY` (`ci.yaml:62-64`, `deploy-backend.yaml:114-119`); the always-on checks are prompt-wording unit tests and `TestEvalFixturesParse`. `prompts.md:502-503` and `:523-525` admit the procedure is manual and that the last rewrite shipped without a baseline.

Why it is debt: the one safety story for prompt changes is told as if automated; R6-RT-8 and R7-10a are what happens when it is not. Fix (S): reword `prompts.md:473-498` to "manual, until the owner records"; the honest fix is R7-9 (owner) plus the queue's item "AI key in GitHub secrets" (owner decision, already asked 30 Sept).

### DOC-7 (Medium) — Design docs: five false claims across fifteen files, and four features with no doc

False (each verified against code):
1. `docs/design/README.md:16` — checklists row: "the whole-note **Split up** (`service/note_clean.go`)". Replaced by "Tidy up list" in #195 (`NoteActions.tsx:144`, `useTidyList.ts:18`); `checklists.md:606-637` itself documents Tidy up. File right, name wrong.
2. `docs/design/README.md:22` — push row: "keeps it dormant until `scripts/vapid-keys.sh` has run". True at 53bf8a23, false at HEAD: #207 installs the pair by default (`setup.sh:82`, `bootstrap.sh:165-173`); `push.md:3-5` was rewritten, the index was not.
3. `note-screen.md:64-66` — "'Record into this' lands on `?tab=recordings`". `captureReturnPath` returns the note route with no tab (`CaptureScreen.tsx:316-326`); the control is gone (the doc's own L31); contradicted by `capture-ux.md:70-73`.
4. `regenerate.md:187-192` — "one level since #130 … the block is written at the top level". Three levels since #191 (`cleanup.MaxDepth = 2`, `items.go:311`; `MAX_DEPTH = 2`, `checklist.ts:72`) and the block keeps the recording's indent (`append.go:515-516, 629-646`).
5. `regenerate.md:31-35, 78-82` — "the whole-note cleaned view is regenerated afterwards (`cleanNoteAfter`)". For a checklist `cleanNoteAfter` returns at once (`clean_note.go:386-389`, #195); the code comment at `regenerate.go:27,38-40` has the same rot.
6. `async-updates.md:128` names `scripts/push-keys.sh`; the script is `scripts/vapid-keys.sh` (inside the labelled proposal section, so low).

Verified current (contrary to the brief's expectation): `capture-ux.md` (PTT hold/lock/cancel, `/talk` gone, Space/R/Esc, every `holdTiming.ts` number, the Filing tray and `noticeKind`), `checklists.md` (three levels, Tidy up, Undo rules, 409/400 texts), `home.md` (multi-select and batch purge recorded as removed at L108, the Back model, `useBackGuard`), `pins.md`, `inbox.md`, `ask.md`, `pipeline-deadlines.md`, `append-vs-autosave.md`, `usage-accounting.md`, `push.md` at HEAD. All 120+ file paths the docs cite exist at the stated location.

No design doc for: audio/body **retention and the purge sweep** (`backend/internal/purge`, `model.RetentionTiers {7,30,90,365}`, the five tagged S3 lifecycle rules `template.yaml:1006-1111`, `ExpirySweepRule` — one sentence in `home.md:144`); **idempotent replay** (`handler` `idempotent()` wrapper; a passing mention in `inbox.md:151-155`); the **Recordings tab** (`SelectionBar`, `MoveSheet.tsx`, `zipRecordings.ts`, `useRetranscribeCapture.ts`, `TranscriptPanel.tsx`); the **#201 adopt fix** (`useNoteEditor.ts:71-76,547-555`, `useTidyList.ts:173-179`) that removed the 409 before every Tidy. The round-8 specs (`spec-gestures.md` 38 KB, `spec-checklists.md` 32 KB, `spec-visual.md` 33 KB, mocks) exist only in `.agent-tools/r8/`; their content did reach `docs/design`, but the decision record (options weighed, taste calls) is uncommitted, and `qa-r8-live.md:139`'s requested spec sentence was added there, outside the repo.

Fix (S for the six lines; M for the four docs): one docs PR; commit the r8 specs under `docs/design/specs/2026-09-30/` with a one-line "superseded by the design docs where they differ" head. No owner decision.

### DOC-8 (Medium) — `docs/reviews/README.md` index: four committed reports missing, two phantom ones named

`git ls-files docs/reviews` = 17 files; the table has 13. Missing: `2026-09-24/qa-r4-live.md`, `2026-09-27/qa-decisions-live.md`, `qa-feedback-live.md`, `qa-regenerate-live.md` (0 mentions each). The closing paragraph says `2026-09-21/qa-final.md` and `smoke-checklist-capture.md` "were not committed" — both sit untracked in the checkout (`git status` `??`), and `qa-w1-live.md` and `qa-delete-live.md` (26 Sept, on prod) exist only in `.agent-tools/`. `r5/` is described as 3.7 MB; `du` says 2.7 MB; its prune waits on the three branding decisions (open since 26 Sept) — say so. Fix (S): four rows; commit or delete the four stray reports; fix the size.

### DOC-9 (Medium) — README: every command runs; eight claims are stale

(Sub-reviewer, verified.) L93 `enable_alarms` says five alarms; the template has seven (`template.yaml:2194-2415`, the 5xx alarm from #176) and L204 already says six-or-seven. L296 "the Playwright e2e on Chromium" — CI installs and runs WebKit too (`ci.yaml:353-369`). L292 webkit spec list omits `pins`, `back-nav` (`playwright.config.ts:77`). L150 `auto_clean` "after every recording" — a checklist's `auto_clean` is ignored (`note_clean.go:246`, #195; About says so). L7/L93/L204 worker task lists miss `regenerate-note` and (L7) `storage-snapshot` (`cmd/worker/main.go:14-19`). L146 "the switch went on 2026-09-27" — it was removed (`model/types.go:229-232`). L194 protected-resource list omits `UserPoolDomain` (`deploy.sh:55`; L115 has it). L41/L60 never mention `setup.sh --reviewer`, so a fork maintained by someone other than the owner gets the wrong approver. Omitted scripts: `build-lambda.sh`, `lib/common.sh`. Features README does not mention: hold-to-talk on the disc, the Back stack, the Filing tray, Tidy up list, tab swipe, pins. Verified true: all 17 scripts and every flag, every `aws`/`gh`/`go`/`bun`/`curl` line, the `chintanctl` subcommand list, every constant in "Connect a device", all 8 links, the SQS DLQ/staging/boundary architecture (the memory note "v3 dropped SQS/GSI2/staging/boundary" was a plan; README matches the code). Fix (S), no decision.

### DOC-10 (Low) — Fifty open owner decisions in nine places; the oldest ten days

The queue's "older decisions stay parked until you say those too" (29 Sept item 7) is honest, but there is no single list, and five 29 Sept items were settled silently by R8 (DB6-7, DB6-10 moot, DB6-11, PR-D4's reversal, VAPID by #207). Table below. Fix (S): the "Needs the owner" section of the generated view, keyed by id with the date first asked.

### DOC-11 (Low) — Promised follow-ups that did not happen

- Queue 29 Sept item 6: the `NoteCleanMode` comment "frontend-scoped one-liner" — `frontend/src/api/schema.ts:157-162` still says "`tasks` is the one mode a checklist is cleaned in"; DB6-22 keyed live region and DB6-35b knip flag — rows still `open`/`queued`, no PR.
- `qa-r7-live.md:204` F5 (receipt excerpt is the transcript, not the filed text) — no backlog row.
- Round-5 queue L173: prune `r5/` after the decisions — 20 of 24 taken; blocked on the three branding ones; not said anywhere.

### DOC-12 (Low) — "Order of work" (backlog L94–L100) is the 4 September plan

"B7 passkey registration", "D1 cleanup modes (needs your call)" — all shipped weeks ago, and it is the first prose after the legend. One line: "historical".

### DOC-13 (Low) — The r8 brief is the r7 brief

`.agent-tools/r8/brief.md` begins "# Round 7 implementer brief"; identical to r7 apart from paths. Its Playwright line lacks `CI=1`, the gotcha that cost a stream on 30 Sept. Folds into DOC-3.

## Checked, no finding

- **`docs/api/openapi.yaml`**: no past-tense wording (no Split up, /talk, multi-select, purge, SQS, staging, GSI2, "planned"); 133 schema properties, 130 with a `json:"…"` tag in Go, the 3 others are multipart parts handled by `case` labels (`handler/inbox.go:314-336`) — zero orphan fields; exactly one `deprecated: true` (`Settings.cleanup_mode`, L1591–1601), accepted-and-ignored on PUT (`handler/wire.go:457-465`), never returned, unused by the frontend; 42 operations ↔ 42 routes in `handler/routes.go:20-104`, 1:1. Four conformance tests (`openapi_conformance_test.go:195,257,359,419`) plus the contract fixtures run in CI. They do not read `components/schemas`, so field drift would not be caught — but there is none today.
- Backend design-doc numbers (deadlines, quotas, ladders, alarms, TTLs — ~60 constants) all match code.
- `docs/history/`, `docs/ops/`, `docs/qa/` indexes consistent; CH-O3's screenshot deletion done (`docs/qa/2026-09-04` is 32 KB).
- Every PR number in the queue's 29–30 Sept "What shipped" exists and is merged.

## Backlog measurements

- 727 lines · 554 data rows · 32 table headers (one spurious) · 20 `##` sections · 13 `###` (two `S8`).
- Rows per round: R3 99 · R4 43 (+15 feedback) · R5 77 (+20 decisions, +11 feedback) · R6 67 · R7 44 · R8 26; pre-round 133.
- Statuses: 492 `done` · 25 `queued (owner)` · 10 `open` · 7 `next` · 3 `design` · 3 `answer` · 3 `declined` · 1 each `dropped`, `deferred`, `asked`, `gated`, `reversed`, `not reproduced`, `closed — not a fault`, `queued (frontend)` · `reverted` ×3.
- Duplicate ids: 60 (113 rows); 11 same-section near-duplicates; 12 cross-round id reuse.
- `done` rows contradicted later: R6-RT-8, R7-10a (reverted); DB6-1, DB6-3, R7-21 (corrections); R7-1 (R7-1a "was not"); R7-7a (superseded by R8-F9); R6-CL-D1 (by R8-F8); CL-D1 (by R8-F1a); PR-D4 (reversed).
- Open rows nobody owns (no owner, PR or stream named): R6-RT-9, R6-RT-10, DB6-8, DB6-22 follow-up, DB6-35b, R8-F8b, C-fe4, T55-r, H7, D12, B14 — 11.
- Commits touching the file: 21 Sept 15 · 24 Sept 8 · 26 Sept 27 · 27 Sept 11 · 29 Sept 21 · 30 Sept 25.

## Proposed backlog structure (append-only kept)

1. Keep `docs/backlog.md` as the ledger, shape unchanged; append-only plus keep-both is right for ten parallel streams.
2. Two legend conventions: a later row that changes an earlier row's truth starts its Item with `Correction:` / `Review of #N:` / `Superseded by <id>:` / `Reverted:` and names the id; the Status cell may carry `gate: <what>` and `owner: <date asked>`. Most rows already do this informally.
3. Generate `docs/backlog-open.md` with `scripts/backlog-view.py` (≈60 lines; run in CI like the contract fixtures, fail when stale). Last row per id; four sections: *Needs the owner* (with age), *Open, unowned*, *Done, gate owed*, *Reverted / superseded (30 days)*. One line each: id · section · line · item. That file is what the queue links to and what a round brief says to grep.
4. One-time cleanup of the ~21 rows in DOC-1 — edit the status cell in place (smaller) or append corrections; owner's call.
5. The morning queue keeps its narrative; its "Needs you" lists become links into the generated owner section.

## Open owner decisions — one table (age at 1 Oct 2026; ◦ = known per the brief)

| # | Decision | Asked | Where | Age (d) |
|---|---|---|---|---|
| 1 | Two legacy August notes: restore or purge (Decision A) | 21 Sep | queue L30 | 10 |
| 2 | New notes default to checklists (Decision B / C6) | 21 Sep | queue L32; backlog L201 | 10 |
| 3 | PWA shortcut records at once vs confirmation (Decision C / CS4) | 21 Sep | queue L34; backlog L190 | 10 |
| 4 | Re-apply the read-only agent policy (Action D) — still denied per the r9 brief | 21 Sep | queue L38 | 10 |
| 5 | Transcription language default (§4 item 1) | 21 Sep | queue L44 | 10 |
| 6 | Branch protection on `main` (item 3) | 21 Sep | queue L46 | 10 |
| 7 | Termination protection (item 4) | 21 Sep | queue L47 | 10 |
| 8 | Cleanup latency tail: two-phase append or accept (item 7) | 21 Sep | queue L50 | 10 |
| 9 | CSP meta after a Pixel test (item 8) | 21 Sep | queue L51 | 10 |
| 10 | Shared spend cap before inviting anyone (item 10) | 21 Sep | queue L53 | 10 |
| 11 | Self-service account deletion (item 11) | 21 Sep | queue L54 | 10 |
| 12 | Review the three restructures on the phone (item 15) | 21 Sep | queue L58 | 10 |
| 13 | Native wrapper / hardware button for the ring (R4 decision 5) | 24 Sep | queue L73 | 7 |
| 14 | Pin-only PATCH skips the version check (R4 decision 6/8) | 24 Sep | queue L74, L109 | 7 |
| 15 | gsi1 projection / gsi2 zero-downtime plan (R4 decision 7/9, R4-39) | 24 Sep | queue L103, L111 | 7 |
| 16 | Logo mark (R5-BR-L1) | 26 Sep | backlog L449 | 5 |
| 17 | Brand row placement (R5-BR-H2) | 26 Sep | backlog L450 | 5 |
| 18 | PTT disc glyph (R5-BR-P3) — half moot, `/talk` is gone | 26 Sep | backlog L451 | 5 |
| 19 | Receipts: rows or "N new" badges (R5-RC-D3) — folded receipts shipped in R7 without closing it | 26 Sep | backlog L454 | 5 |
| 20 | Pebble webhook "Recording only" experiment (WH-D) | 26 Sep | backlog L463 | 5 |
| 21 | Custom-domain DNS: registrar or Route 53 (OD-1) | 26 Sep | backlog L465 | 5 |
| 22 | Cognito sign-in domain (OD-2) | 26 Sep | backlog L466 | 5 |
| 23 | URL shape on the custom domain (OD-3) | 26 Sep | backlog L467 | 5 |
| 24 | Split the `pipeline` package or only its file (CH-O1) — #111 did the file | 26 Sep | backlog L468 | 5 |
| 25 | Purge the test tenant's 57 leftover notes (R6-CLOSE-2) | 29 Sep | queue L226; backlog L646 | 2 |
| 26 | Keep "App feedback" active or archive per round (R6-RT-OD3) | 29 Sep | queue L229 | 2 |
| 27 ◦ | DB6-9 orphan sub-item block rule | 29 Sep | queue L230, L306 | 2 |
| 28 | DB6-14 "note that / note to" after a filing span | 29 Sep | queue L230 | 2 |
| 29 | DB6-15 equal-length prefix match on two notes sharing a tag | 29 Sep | queue L230 | 2 |
| 30 | DB6-21 stale "note changed" flag after Undo: wire change or documented wart | 29 Sep | queue L230 | 2 |
| 31 | DB6-25 / R6-CLOSE-1: run the owed live eval and device checks | 29 Sep | queue L230; backlog L645 | 2 |
| 32 | R6-RT-8 re-proposal of the title sentence | 29 Sep | queue L230; backlog L639 | 2 |
| 33 | Undo toast dies with its panel on a tab switch? (shipped: lives) | 29 Sep | queue L231 | 2 |
| 34 | The stricter Undo variant | 29 Sep | queue L231 | 2 |
| 35 | DB6-22 follow-up PR (keyed live region) | 29 Sep | queue L231; backlog L615 | 2 |
| 36 | DB6-35b knip flag PR | 29 Sep | queue L231; backlog L598 | 2 |
| 37 | DB6-8 residual | 29 Sep | queue L231; backlog L621 | 2 |
| 38 ◦ | Phone checks after R6 (row hold, disc hold, caret) | 29 Sep | queue L227 | 2 |
| 39 ◦ | Record the routing replay set (R7-9) | 30 Sep | queue L260; backlog L655 | 1 |
| 40 | "Always a new note" target in the pill (R7-14a) | 30 Sep | queue L262; backlog L687 | 1 |
| 41 | Store the ring's own transcript as a fallback | 30 Sep | queue L266 | 1 |
| 42 | A stronger model for routing only | 30 Sep | queue L266 | 1 |
| 43 | The AI key in GitHub secrets so the eval runs in CI | 30 Sep | queue L266 | 1 |
| 44 ◦ | Phone checks after R7 (Show focus, row hold, caret) | 30 Sep | queue L263 | 1 |
| 45 ◦ | Phone checks after R8 (loupe, banner mic, swipe with keyboard, Pixel Back) | 30 Sep | queue L305 | 1 |
| 46 | Run `TestLiveEval/(items\|tasks) -count=3` for the Tidy prompt | 30 Sep | queue L307 | 1 |
| 47 ◦ | Checklist depth 3 vs 4 | 30 Sep | queue L296 | 1 |
| 48 | Taste calls 1–8 of round 8 ("say if you want any reversed") | 30 Sep | queue L289–L302 | 1 |
| 49 | DOC-2: record or stop the agent's gate approval | 1 Oct | this report | 0 |
| 50 | DOC-1 step 4: edit the stale rows in place or append corrections | 1 Oct | this report | 0 |

Settled since asked but never closed in the record: R5-RC-D1/D2 (built), VAPID (#207), DB6-7, DB6-10 (moot), DB6-11 (R8-F1a), PR-D1–D5, CL-D1/D2, WH-A/B/C/E, CH-O2/O3, round-3 §4 items 2, 5, 6, 9, 12 (→ R5-BR-L1), 13.

## Process — what should move into the repo

| Artefact (today) | Belongs | Why |
|---|---|---|
| `merge-seq.sh`, `stack-merge.sh`, `gate.sh`, `resolve-known.py` | `scripts/dev/` | Encode three recorded lessons (#152 retarget, fe-kind deploys, keep-both files); CI already lints `scripts/**`. |
| `mk-sync.sh` | `scripts/dev/` | The only route from a worktree to the machine with the toolchains. |
| r7/r8 `brief.md` stable half | `CLAUDE.md` | Unchanged between rounds; the per-round half goes to `docs/reviews/<date>/brief.md`. |
| r8 `spec-*.md`, mocks | `docs/design/specs/2026-09-30/` | The decision record for PTT, depth, tray, Back. |
| Memory gotchas: bun shim, `CI=1`, setup.sh token over stdin, append-only files, 3-agent limit, read-only main checkout, `CHINTAN_UPDATE_FIXTURES=1` after merge-from-main, ugrep / no `timeout` | `CLAUDE.md` | Each cost a probe or a stream once. |
| The deploy-gate standing instruction | `docs/contributing.md` or README "Operate" | DOC-2. |
| QA harness libs on orb (`~/r3/live/*.js`, `r6b-*.py`), `qa-w1-live.md`, `qa-delete-live.md` | `scripts/qa/`, `docs/reviews/` | Every live QA report cites them; none is in git. |

## What is good (keep)

1. Ledger discipline: every row names the PR, the file and the test, and a corrected claim gets a correction row rather than a silent edit. The record does not lie on purpose; it is simply never re-read.
2. The design docs are current where it counts: the round-8 behaviour (PTT, three levels, Tidy, tray, Back) is already in `capture-ux.md`, `checklists.md`, `home.md`; all 120+ cited paths exist; every backend constant matches.
3. openapi.yaml ↔ router is 1:1 and four conformance tests plus the fixture check keep it so.
4. `docs/reviews/README.md` and `docs/history/README.md` say plainly what is maintained and what is not; the morning queue is written for a human and names its own stale paragraphs.
5. The pre-agreed revert rule for prompt changes (R7-10a) was written down before shipping and honoured — the process caught its own regression.