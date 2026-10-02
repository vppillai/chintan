# R5 prompts lens — measurements (2026-09-26)

Sources: prod worker log group `/aws/lambda/chintan-worker-dev-prod`, "provider usage" lines 2026-09-20..26
(7-day retention; 439 rows, 2 tenants: owner 6871b3c0… 289 rows, test tenant 78e1c340… 150 rows);
DynamoDB `chintan-dev-prod` note rows (aggregates only); gpt-tokenizer cl100k as a stand-in for MiniMax's tokenizer.

## Real usage per op (MiniMax-M3 list price $0.30/M in, $1.20/M out)

| op | calls/7d | input tok p50 / p90 / max | output tok p50 / p90 / max | cost p50 µ$ | 7-day sum µ$ |
|---|---|---|---|---|---|
| route | 86 | 3223 / 3313 / 10781 | 31 / 50 / 55 | 995 | 74,291 |
| cleanup | 180 | 377 / 855 / 2580 | 22 / 71 / 2219 | 143 | 36,824 |
| clean_note (30 of 34 = tasks) | 34 | 505 / 677 / 3279 | 76 / 254 / 1634 | 246 | 17,067 |
| ask | 13 | 1470 / 2008 / 2112 | 337 / 768 / 849 | 836 | 12,569 |
| transcribe (Groq) | 126 | 4,141 audio s total | | 100 | 46,069 |

Routing is 52 % of LLM spend and 40 % of all provider spend. A Home recording costs ≈ 1,240 µ$ of which routing is 80 %.

## Owner tenant shape
57 active notes (7 above the 50-candidate window), title avg 14.5 chars (max 32), 4 aliases and 14 tags in total, 0 checklists, 0 verbatim, all notes inherit the default language. Test tenant: 8 active (2 checklists).

## Prompt sizes (tokens)

| prompt | current system | current user (sample) | proposed system | proposed user |
|---|---|---|---|---|
| route | 1433 | 1807 (50 cands, 60 words; ids 21 tok each → 1587 for the block) | 987 | ~520 (ordinals: 418 for all 57 notes + 60 numbered words ≈ 130) |
| route, targeted strip (1 cand) | 1433 | 333 | 987 | ~150 |
| cleanup faithful / polished | 170 / 166 | 105 (fence + rule + language line, 60 words) | 191 (one template, all rules inline) | ~25 + transcript |
| items | 643 | 70 | 480 | 70 |
| note structured / polished | 163 / 176 | 286 (4 para) | 181 (one template) | same |
| note tasks | 306 | 55 | — (owner decision: drop) | — |
| ask | 350 | 1400–2100 measured | unchanged | unchanged |

Word numbering costs ~3 tokens/word (30 words: 33 → 122 tokens; 10 Malayalam words: 27 → 62). One real note id: 21 tokens; "- id: … | title: Roof repair" 30 tokens vs "12 | Roof repair" 7.

## Prompt-outcome signals in the same window
- `router chose a new note whose title names an existing note; appending to it instead`: 11 of 86 routes (13 %) — the prompt tells the model the opposite of what the code then does.
- `routing failed … router returned unknown action "none"`: 1 (JSON shape).
- `router returned no instruction_spans` (legacy `content` path): 0. `checklist item extraction returned no list`: 0. `dropped tasks whose words are not in the note`: 0.
- Ask: 13 calls, 62 notes considered, 12 packed each time, 1.6–2.2 KB packed, all grounded.
- clean_note tasks: 30 calls on bodies of 10–780 bytes — the Split up view regenerated after every appended item.
