# Prompt evaluations

Every run of the live prompt evaluation (`TestLiveEval`,
`backend/internal/provider/live_eval_test.go`) and of the production routing
battery (the procedure: `backend/internal/provider/testdata/eval/prod-battery.md`), one line
per run, newest last. How a prompt change is measured is
`docs/design/prompts.md`, "Changing a prompt"; the rules the battery
exercises beyond the reply are `docs/design/routing.md`. The latest live-eval
run is the baseline the next prompt change is compared against: a case below
3/3 there is a known weakness of the prompt, not a regression.

## Runs

| Date | Run | Scope | Result | Notes |
|---|---|---|---|---|
| 2026-09-28 | Production battery after the routing rewrite (#134: sections, numbered candidates, tags as names, all active notes as candidates), by hand through the inbox on the owner's tenant | 22 route rows × 3 | routing 17 of 22 rows three of three; items 9 of 10 | `docs/history/reviews/2026-09-29/prod-battery.md`; the pre-merge live eval was not run because the agent could not read the key |
| 2026-09-29 | Production battery after round 6 (#157 name-first convention and the ring phrasings as fixtures; #155 items as a one-level tree), on the test tenant | 32 route rows × 3 (96 captures), 16 items rows, 2 audio probes | routing 22 of 32 rows 3/3 (rows 2, 6, 7, 28 at 2/3; 8, 9, 14, 17, 30 at 1/3; 15 at 0/3); items 16 of 16; audio 2 of 2 | `docs/history/reviews/2026-09-29/prod-battery-after-r6.md`; 8 of 96 routing captures parked at `needs_target`, 4 `no_content` as expected; rows 1, 16, 21 moved up, 2, 6, 7, 8, 9, 14, 15, 17 moved down against the 28 Sept run |
| 2026-09-29 | Re-check after #170 (the "a name is one to five words, never a whole sentence" Titles sentence and a ninth example, R6-RT-8) | gate rows | row 30 ("The dog is having his dinner") 1/3 → 3/3; row 32 ("Groceries list milk eggs and protein powder") 3/3 → 1/3 | the prompt commit was reverted under the pre-agreed rule (any regression of a gate row reverts the prompt alone); `docs/backlog.md` R6-RT-8 |
| 2026-10-01 | `TestLiveEval/route -count=3` with the instance key, the worker's default model (PR10-4, D14) | 32 cases, 96 calls, 185 s | 24 of 32 cases 3/3 | per-case table below; two failures are a dropped payload, not prompt shape (route/19, route/31 return empty `content` on an append to a listed list) |
| 2026-10-01 | `TestLiveEval/(items\|tasks) -count=3`, same key and model | 17 + 9 cases, 78 calls, 106 s | items 14 of 17 3/3; tasks 5 of 9 3/3 | per-case table below; tasks/06 is refused every run by `SplitOutput`'s coverage guard, not by the model's shape. The tenth tasks case (four levels, PR10-11) was added after this run and has no result yet |

## Per-case outcomes, 2026-10-01

Every case not listed passed 3/3.

| Case | Transcript or body | Runs | What the model did |
|---|---|---|---|
| route/08 | "I was thinking about the roof today and how the Portugal trip went over budget" | 2/3 | once `append` to Roof repair at `conf=0.50` (the 0.75 bar parks it at `needs_target`), not `new` |
| route/12 | പുതിയ കുറിപ്പ് പേര് ദന്തഡോക്ടർ നാളെ വിളിക്കണം | 2/3 | once `append` to an existing note instead of a new note titled ദന്തഡോക്ടർ |
| route/18 | "create a shopping list and add chickpeas and green gram into it" | 1/3 | `new` checklist "Shopping list" every time, but the content came back `""` and `"and"` in two runs |
| route/19 | "Add umbrella to shopping list" | 0/3 | `append` to Shopping list every time, content `""` every time — the item is dropped from the reply |
| route/24 | "Business ideas by Priyanka seated pool for dogs" | 2/3 | once `new "Business ideas by Priyanka"` instead of naming the listed "Business ideas" |
| route/29 | "these are things that we need to include in our daily report the memory controller…" | 1/3 | once content trimmed to the clause after the cue, once `new "Things to include in our daily report"` |
| route/30 | "The dog is having his dinner" | 1/3 | twice titled with the whole sentence (the shape the reverted R6-RT-8 sentence addressed) |
| route/31 | "Add milk, eggs and protein powder to shopping list" | 0/3 | `append` to Shopping list every time; content `"to shopping list"`, `""`, `""` |
| items/04 | "for the party plates, cups and napkins" | 1/3 | the group named "For the party" instead of "Party" |
| items/13 | the wall of speech | 1/3 | Milk, Bread, Eggs left top-level (5 top-level, want 3) with the Hardware store grouped |
| items/15 | "salt and pepper and fish and chips" | 0/3 | "Salt and pepper · Fish and chips" every time (2 items, want 3–4) |
| tasks/02 | eggs from Walmart and meat from Costco | 2/3 | once the whole line kept as a parent over the two stores (6 lines, want 5) |
| tasks/05 | "buy milk and call the plumber" | 2/3 | once "Call the plumber" nested under Milk |
| tasks/06 | `- [ ] Milk` / `- [x] milk` / `- [ ] Eggs` | 0/3 | the model merged the duplicate as done; `SplitOutput`'s coverage guard (R9-BE1) refused the answer with `an open item was lost` — open did not win |
| tasks/08 | "book flights and hotel for Lisbon and tell Anu the dates" | 2/3 | once kept as one item |

route/19 and route/31 return an empty `content` on an append to a listed
list, so what the worker files there is whatever `decide()` and the items
prompt make of the raw transcript; the routing replay, once recorded, would
show the outcome.

## Measured sizes, production, 2026-09-20 to 2026-09-26

From the prod worker log, 439 provider-usage rows across two tenants
(`docs/history/reviews/2026-09-26/r5/prompt-measurements.md`; MiniMax-M3 list price
$0.30/M in, $1.20/M out; tokens counted with cl100k as a stand-in). The
system-prompt column is the prompt as it was before and after the 2026-09-27
rewrite.

| Prompt | Calls / 7 d | Input tokens p50 / p90 / max | Output p50 | Cost p50 | System prompt (tokens) |
|---|---|---|---|---|---|
| Routing | 86 | 3,223 / 3,313 / 10,781 | 31 | 995 µ$ | 1,433 → 987 |
| Cleanup | 180 | 377 / 855 / 2,580 | 22 | 143 µ$ | 170 / 166 → 191 (one) |
| Whole-note (30 of 34 tasks) | 34 | 505 / 677 / 3,279 | 76 | 246 µ$ | 163 / 176; tasks 306 → ~540 (rules 330 + wrapper 210) |
| Items | — (new) | — | — | — | 643 → 480 → ~500 (rules 330 + wrapper 170) |
| Ask | 13 | 1,470 / 2,008 / 2,112 | 337 | 836 µ$ | 350 |

Routing was 52 % of LLM spend and 40 % of all provider spend; a Home
recording cost about 1,240 µ$, of which routing was 80 %. Of routing's 3,223
input tokens, about 1,050 were note ids (21 tokens each, 50 of them), which
the numbered candidate lines of the 2026-09-27 rewrite removed: the owner's
57 notes render in about 418 tokens against 1,587 for the old 50, the whole
prompt in about 1,500 against 3,200 (−53 %). Word numbering costs about 3
tokens a word by this count. `RouterTitleMatchedExistingNote` fired on 11 of
the 86 routes under the old prompt, which told the model the opposite of the
title rule the code enforces.
