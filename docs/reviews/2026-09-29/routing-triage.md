# Routing battery triage — 2026-09-29, reconciled

Why the production battery after round 6 (`prod-battery-after-r6.md`, rows
1–32 three times each after #157) moved eight old rows down against the
run after #134 (`prod-battery.md`), row by row, from two independent
triages (A and B) reconciled against the raw data on orb
(`~/r3/live/pb-*.jsonl/log`, `r6b-results.jsonl`, `r6b-judge.txt`, both
state files, both scripts). Nothing was written to AWS. The code and prompt
changes it asked for are in the PR that adds this file (`r6/db6-routing`,
with the double-blind routing findings); the owner's two actions and the
six decisions are below.

## Verdict

For the owner's tenant specifically — one note per title, no aliases or
tags, name-first ring captures — round 6 made routing better, not worse.
The two real misroutes and the span-end damage of `round-6-proposals.md`
§4 are now three of three (rows 23, 24, 27), a same-second duplicate is
deduped and the decision is in the log. Every old-row loss of the after-run
is either a park or a twin-landing on a title the **test tenant** holds two
or three times, or a tag six notes carry (rows 1, 2, 6, 14, 15 — impossible
on the owner's tenant), or a one-of-three flip (7, 8, 9). The only shapes
#157 plausibly added — an opening phrase taken as a name (rows 30, 28) and
a spoken tag ignored for an invented title (17) — are one rename or Move
away, and the first already carries a fixture and needed one prompt
example.

Facts the reconciliation checked: the tenant was identical in both runs
(the same 57 pre-existing ids: "Roof repair" ×2, "Gutter leak" ×10 — not
eleven — "Dentist", "Grocery list", "Portugal trip", "Reading list" once
each), so the duplicates are the substrate of both runs, not the delta.
The before script never undid a landing in a set-up note; the after script
undid everything, and both the landing and the undo bump the note's
`UpdatedAt`, so the candidate order differed between repeats — but in the
two cases the timestamps allow checking, the model did **not** take the
top-listed twin (row 2 run 1, row 1 run 3), so the choice among identical
lines is noise, not order. Of the 14 failed old-row captures, 8 are parks
and 7 of those name the right note; 7 of the 8 sit on a duplicated name, as
did all four parks of the before run. No moved row traces to #151's code,
with one unresolvable exception (row 8 run 3, below).

## The rows that moved

| Row | Utterance | Before | After | Cause | Evidence, in one line |
|---|---|---|---|---|---|
| 1 | Add this to my roof repair note the gutter is leaking again | 2/3 | 3/3 relaxed (raw 2/3) | tenant duplicates; the one-capture move is noise | A worked example in both prompts and no code path touches an over-bar append; only the tie-break among three "Roof repair" lines moved (a park before, a bare-twin landing after), and at 09:34:10Z the set-up note led the list and the model still took the twin. |
| 2 | the gutter is leaking again put that in my roof note | 3/3 | 2/3 relaxed (raw 0/3) | tenant duplicates | "roof" is an alias on the set-up note only, yet runs 1–2 filed into the house-tagged twin and run 3 parked on the bare twin, with the set-up note at the top of the list; the under-bar rescue needs the name at word 0 and the twin's only name is not spoken, so no code change recovers run 3 (B right, A corrected). `ExtendSpans` did its job: the trailing "Note:" is gone three of three. |
| 6 | call this note dentist I need to book a cleaning before December | 3/3 | 2/3 | tenant duplicates | Two "Dentist" lines; run 3 parked with the pre-existing one suggested. "dentist" is seven letters, so `prefixRuleName` refuses it and it is not at word 0; one of three, none before. |
| 7 | remind me to book the dentist on tuesday | 3/3 | 2/3 | prompt text, one of three, within noise | Run 3 `appended` into Dentist at ≥ 0.75 — nothing in code can manufacture that; the transcript is prompt example 5 verbatim (new "Dentist appointment"). Over-bar topic appends went 0 of 66 before to 3 of 96 after (7r3, 8r3, 9r3): the direction across rows is the evidence, not this capture. |
| 8 | I was thinking about the roof today and how the Portugal trip went over budget | 2/3 | 1/3 | prompt text; run 3 possibly #151's `prefix_title` — the log line decides | Three repeats, three answers (park on the bare twin; new "Roof and Portugal trip budget thoughts"; append into the set-up Roof repair). Both runs parked once, so the hedge predates #157. If run 3 answered `new` with a title opening "Roof repair …", `existingNoteNamed`'s `prefix_title` took the first "roof repair" in touch order, which at 09:34:59Z was the set-up note — exactly where it landed. Only the `routing decided` line for `c_18d9c0f844ce7db4_af2e50f2830ff970` (`matched_by` model vs prefix_title) settles it. |
| 9 | Create a new note and add it to Pebble Ring Test | 3/3 | 1/3 | prompt text, weakly; both failures closed by code regardless | The only moved row with no duplicate in play: run 1 parked under 0.75 with that note suggested, run 3's span stopped at "add it to" and appended "Pebble Ring Test" as text. Nearest new text is the Spans example "0:Create … 8:add …" whose worked answer is a new note. `Route` passed the empty append title to `routedContent`, so `ExtendSpans` could not grow over the destination's name (fixed here, R6-RT-6), and the prefix rules needed the name at word 0 (spoken_name, R6-RT-7). |
| 14 | okay so this goes in the roof repair note we need to check the flashing around the chimney | 3/3 | 1/3 relaxed (raw 0/3) | tenant duplicates | The title is spoken exactly; run 1 landed in the bare twin, runs 2–3 parked with the set-up note suggested — the right note both times, top-listed both times — so the model named the twin it saw first and still hedged, which is what "confidence 1 when a listed note was named unambiguously" asks of it with three identical lines. Two parks of three exceeds noise; the substrate is the triplicated title. spoken_name (R6-RT-7) takes the append on this tenant. |
| 15 | file this under house the tiler wants a deposit before he starts | 2/3 | 0/3 | tenant duplicates | Six notes carry `house`; runs 2–3 parked with Kitchen rebuild suggested (on this tenant the correct answer; the before-run's park suggested the wrong bare Roof repair, so the wavering is older than #157); run 1's over-bar append into the set-up Roof repair is the model confusing it with its house-tagged twin one line away. "house" fails `prefixRuleName`; the fixture's precondition (one `house` note) does not hold here. |
| 16 | put this in my books note the new Le Guin collection is out in October | 2/3 | 3/3 | model noise | One `books` candidate in both runs; nothing in #157 concerns tags, #151's rescue cannot fire (an append with no prefix). A single under-bar park before that did not recur. |
| 17 | add this to money we are forty thousand over on the kitchen | 3/3 | 1/3 | prompt text (direction; the span shape predates #157) | In no run of either battery did the model treat "money" as the name (spans {0,2}/{0,3}); after #157 it twice invented a title from the content ("Kitchen", "Kitchen budget"), a shape 0 of 3 before. The Titles sentence and example 8 make new-titled-from-spoken-words the most rehearsed reply, and nothing says a spoken tag IS the name. Code could not rescue: `titleNames` is exact, `prefix_title` runs the other way, `ExtendSpans` grows over a title, never a tag. |
| 21 | add milk to my groceries list | 1/3 | 3/3 | model noise | The pre-existing plain "Grocery list" was present in both runs, so the row's precondition failed both times and neither result measures F5; #151's re-check compares "groceries list" to "grocery list" (not equal, not a prefix) and converts only new→append. Do not bank the up move. |
| 28 (new) | At this job feedback we have made a lot of changes this week | — | 2/3 | prompt-text shape, noise frequency (one of three on an STT garble) | The wrong answer was new "At this job" — the opening phrase taken as a name, what the added Titles sentence and example 8 teach — while the span stayed []; before #157 the owner's live capture of this sentence got new "App feedback" and was rescued by exact title. No code can catch it ("job feedback" is not "app feedback" as whole words). One Move away. |
| 30 (new) | The dog is having his dinner | — | 1/3 | prompt text (reinforced a habit the ring already showed) | Runs 2–3 titled the note with the six-word sentence; run 1 gave "Dog dinner". The Titles sentence plus the five-word "Things to talk with Milos" example say an opening phrase is a name and nothing said what is not one. No code guard reaches it (spans were [], the body intact, `fallbackNoteTitle` would give the same six words). Fixed by prompt here (R6-RT-8), gated by the post-deploy battery. |

Disputed rows decided: row 1 tenant duplicates within noise (A said
duplicates, B noise — the same coin); row 9 prompt text weakly, both
captures closed by code; row 28 prompt-text shape at noise frequency.
Corrections to the triages: A's spoken_name rule does **not** recover row 2
run 3 (the suggested bare twin's only name is not spoken; B is right), and
"Gutter leak" is ×10, not ×11.

## Do now

In the reconciler's order. Items 1 and 2 are the owner's (the agent role
has no write rights on the tenant and no Logs Insights); items 3, 4 and 5
are in this PR, one commit each.

1. **Purge the test tenant's leftovers and re-run the same 32 rows on the
   current build** — *owner*. On the test tenant only
   (claude-test@example.com), archive and permanently delete every
   pre-existing note, or at least the colliders: "Roof repair" ×2
   (`note_18d235d0ba5b53e6` tagged house, `note_18d2368425a6c751` bare),
   "Dentist" (`note_18d235d0e14ffb92`), "Portugal trip", "Reading list",
   "Grocery list" (`note_18d235d1167591e2`), "Gutter leak" ×10, and the
   five house-tagged notes (Boiler service, Kitchen tap, Garden beds,
   Garage shelving plus the tagged Roof repair). The batch purge endpoint
   was retired in #159, so use `DELETE /v1/notes/{id}/permanent` per note.
   Then run `orb:~/r3/live/r6b-routing.py` (set-up, three runs, judge,
   teardown) unchanged, on this PR's build once deployed; keep the
   undo-everything method, it is production behaviour. Expected: rows 1,
   2, 6, 14, 15 return to 3/3 with no other change, old rows from 14/22 to
   about 19/22, rows 21 and 32 measured for the first time. If rows 2, 6,
   14 still park with a single "Roof repair"/"Dentist" listed, the tenant
   story is wrong and the prompt's confidence sentence becomes the
   suspect. Verify: rows 1, 2, 6, 14, 15 at 3/3; zero landings marked
   pre-existing; the raw judge score equals the reported score. This
   re-run also falsifies or confirms the whole attribution above.
2. **Pull the `routing decided` lines for the 21 failed captures** —
   *owner's role*. CloudWatch Logs Insights on
   `/aws/lambda/chintan-worker-dev-prod`, 2026-09-29 09:25–09:38Z. The
   decision line carries no capture id (counts only); the capture's other
   lines in the same trace do, so resolve the correlation ids first and
   read the decision lines by them:

   ```
   fields @timestamp, capture_id, correlation_id
   | filter capture_id in ["c_18d9c076ae20d155_e297a3f4cd551e36", "c_18d9c0801f849b06_1bd30c48ecf94f41",
       "c_18d9c0815f41edc0_731e733e07ceb532", "c_18d9c08a17ef8eff_213bf0c4f873f840",
       "c_18d9c08bf80e6876_88f1f6730d43ff6f", "c_18d9c08f1b9c4d7c_7ed97fd6c62fc336",
       "c_18d9c0a4f40c27a7_c34da080d23b41ac", "c_18d9c0b1d9c7b7ec_48ab5144fd69103d",
       "c_18d9c0c6fe70edde_4490af1e9bc0eca0", "c_18d9c0c8461bc2ea_abea43cc915e9b18",
       "c_18d9c0cb76a1be7a_f5d3de749a689fd2", "c_18d9c0e51ad0d2d9_331cb5a424cf4a2d",
       "c_18d9c0ecfc415074_d5ebbc6f24379127", "c_18d9c0ef55f59554_393da5cd4e7202f3",
       "c_18d9c0f52edc10e6_1646c765a21b1e99", "c_18d9c0f6736b50e1_0074891fd1e22425",
       "c_18d9c0f844ce7db4_af2e50f2830ff970", "c_18d9c0f9a12d996c_963b65f4b484b9e9",
       "c_18d9c102bf95422d_609ea062d032e3d2", "c_18d9c104035c1a8f_6d4cb0373bd48aa2",
       "c_18d9c120fa2666f6_9ea53f81c2eded4b"]
   | stats latest(correlation_id) as corr by capture_id
   ```

   then, with those correlation ids:

   ```
   fields @timestamp, correlation_id, action, confidence, matched_by, spans, removed_words, candidates
   | filter @message like /routing decided/
   | filter correlation_id in [<the 21 corr values>]
   | sort @timestamp asc
   ```

   The captures, by row and run: 2r1 `…e297a3f4cd551e36`, 8r1
   `…1bd30c48ecf94f41`, 9r1 `…731e733e07ceb532`, 14r1
   `…213bf0c4f873f840`, 15r1 `…88f1f6730d43ff6f`, 17r1
   `…7ed97fd6c62fc336`, 28r1 `…c34da080d23b41ac`, 2r2
   `…48ab5144fd69103d`, 14r2 `…4490af1e9bc0eca0`, 15r2
   `…abea43cc915e9b18`, 17r2 `…f5d3de749a689fd2`, 30r2
   `…331cb5a424cf4a2d`, 1r3 `…d5ebbc6f24379127`, 2r3
   `…393da5cd4e7202f3`, 6r3 `…1646c765a21b1e99`, 7r3
   `…0074891fd1e22425`, 8r3 `…af2e50f2830ff970`, 9r3
   `…963b65f4b484b9e9`, 14r3 `…609ea062d032e3d2`, 15r3
   `…6d4cb0373bd48aa2`, 30r3 `…9ea53f81c2eded4b`. Read `action`,
   `confidence`, `matched_by`, `spans`, `removed_words`. Row 8 run 3:
   `matched_by=model` confirms the prompt-text attribution;
   `prefix_title` means #151 filed a `new` titled "Roof repair …" into the
   first Roof repair in touch order. The eight parks' confidence values say
   whether the hedge sits at 0.5 (the prompt's "plausible guess") or just
   under 0.75, which decides whether any threshold talk is worth having.
   The log group keeps 14 days; after 2026-10-13 the lines are gone.
3. **Give `ExtendSpans` the destination note's title on an append, and let
   a span that stops inside the title grow to its end** — *this PR,
   R6-RT-6*. `provider.Route` looks the append's destination up by
   `decision.NoteID` and hands its title to `routedContent`;
   `routing.ExtendSpans` tries an overlap k = 0..n-1 back into the span,
   `NormalizeSpeech` on both sides, smallest k first. Nothing adds a word;
   `RemoveSpans` still refuses a span that does not fit; the empty-content
   guards apply after the growth (a new note keeps the un-grown result when
   the growth would empty it, DB6-4). Unit-tested on row 9's shape (→ empty
   content → `no_content` into Pebble Ring Test) and row 13's ("smoke" no
   longer opens the body); rows 23, 24, 26, 29 unchanged. Post-deploy:
   rows 9 and 13 three times — 9 never appends the text "Pebble Ring
   Test", 13's body does not start with "smoke"; 23, 24, 26, 29 still 3/3
   with the same bodies.
4. **Take an under-bar append when the model's own suggested note's name is
   spoken as a name** — *this PR, R6-RT-7, `matched_by spoken_name`*. In
   `existingNoteNamed`, for an append under 0.75 only: the single candidate
   whose id is `decision.NoteID` (never a re-pick), one of its names
   (title, aliases, tags) passing `prefixRuleName` (two words or eight
   letters), spoken as whole words, and either followed by "note"/"list" or
   beside an instruction cue (`routing.MentionsInstruction`); confidence 1
   as for the prefix rules. Unit table: row 14 and row 9 fire; row 8 run 1
   ("roof" is a topic), row 6 ("dentist", seven letters — owner decision
   1), row 2 run 3 (the twin's name not spoken), row 15 ("house") and a
   bare mention do not. On the owner's tenant it fires only when the model
   was unsure, already picked that note, and the full title was spoken next
   to "note"/"list" or with a filing cue — the silent-file-over-ask trade of
   R6-RT-OD1(a), one Move away if wrong. Post-deploy: rows 9 and 14 three
   times on the unpurged tenant (the duplicates are what make it
   testable): 14 → 3/3 into the set-up Roof repair, 9 → 3/3 `no_content`;
   rows 8, 15, 2, 6 unchanged in shape. Over the following two weeks grep
   the owner tenant's log for `matched_by=spoken_name` and expect a handful
   at most; if it fires on a topic mention, tighten to the "note"/"list"
   branch only.
5. **Prompt: a whole sentence is not a name** — *this PR, R6-RT-8*. One
   Titles sentence after #157's ("A name is a short noun phrase of one to
   five words, never a whole sentence: 'The dog is having his dinner' has
   no name in front, so invent a title of one to five words and keep every
   word as content") and example 9 (→ new "Dog dinner", no span); about 90
   more tokens a route call. **It ships without the live eval**:
   `TestLiveEval` needs `LLM_API_KEY`, which is in SSM and the owner's
   alone. The post-deploy gate is the battery — rows 30, 25, 32, 22, 12,
   27, 7, 8 three times: 30 → 3/3 titled with at most five words and the
   whole sentence as body; 25 (a real five-word name) and 32 (a two-word
   list name followed by items) are the ones the sentence must not weaken;
   22, 12 (keeps the name-only title `ദന്തഡോക്ടർ` #157 gained), 27, 7, 8
   unchanged. If the re-run regresses any of them, that commit is reverted
   alone; the owner can also run `cd backend && LIVE_LLM=1 LLM_API_KEY=… go
   test ./internal/provider -run 'TestLiveEval/route' -v -count=3` first.

Also in the PR, from the double-blind review's routing findings: the
decision line is logged once, from the branch `route()` finally takes, with
an `outcome` attribute, and a dedupe no longer counts as a title match
(DB6-18); a rescued append carries no kind and the exact-title rescue
ignores trailing punctuation (DB6-39, DB6-40); tasks eval case 07 runs with
the title it claims (DB6-19).

## Owner decisions

The reconciler's recommendation first, then the trade.

1. **Widening spoken_name to one-word names of five to seven letters
   ("dentist", "house") — recommend not now.** Revisit with two weeks of
   `matched_by=spoken_name` in the decision line, the review clause
   R6-RT-OD1 already carries. On this tenant it recovers row 6 run 3 (→
   3/3) and row 15 runs 2–3 (→ Kitchen rebuild, 2/3), but it converts an
   ask into a silent file on a short alias or tag the model was only half
   sure of, and six notes carry `house` here. Moot on the owner's tenant
   until it has aliases or tags.
2. **A filing cue followed by a tag or alias that exactly one candidate
   carries files into it and strips the cue and the word — recommend after
   the owner starts using tags, with the uniqueness guard.** Row 17 → 3/3
   with "money" out; row 2 run 3 via the alias "roof" unique to the set-up
   note; row 15 stays silent (six `house`). It is what About/PR-D1
   promised, but it turns an ask into a file and edits the body the model
   left (R6-RT-OD1 chose to keep content as the model left it), needs a
   small stop-word window ("my", "the"), and the owner cannot hit it today
   (every owner note has empty aliases and tags, §4).
3. **Adding "a listed name later in the sentence is a mention, not a
   request" to Destination for rows 7 and 8 — recommend do not chase on
   one-of-three evidence.** Each moved by one capture of three, and the
   sentence trades against row 29 (the owner's real phrasing, 3/3 today,
   whose name sits mid-sentence and IS a request). If ever, only with the
   row-29 escape clause and `TestLiveEval` at -count=3 gating it.
4. **`routeConfidenceThreshold` stays 0.75 — do nothing, stated so it is not
   re-argued after the purge.** A, B and the reconciliation agree: 0.5
   would have silently filed row 8 run 1's wrong suggestion and the
   before-run's row 15 run 3 wrong Roof repair; round-6 §4 found append
   confidence calibrated on the owner's tenant; the value is unobservable
   from here. The same for rewording the confidence sentence to give 1 on
   duplicated titles: it optimises for a tenant state the owner does not
   have. The decision line now carries `confidence` per capture; decide
   from two weeks of the owner's own distribution, not from this tenant.
5. **R6-RT-OD2 (a `new` is never parked) — nothing in either battery argues
   to change it.** The after-run's wrong `new`s (row 17 "Kitchen"/"Kitchen
   budget", row 28 "At this job", row 30 the whole sentence) are wrong
   titles, not junk notes, and a threshold could not have told them from
   the correct `new`s of rows 5, 7, 22, 25, 27.
6. **Battery scoring: whether the same-title relaxation stays in the report
   — say which number is quoted until the purge makes it moot** (below).
   Related: whether `existingNoteNamed`'s tie-break among equal-length
   names (first in touch order wins) should prefer the model's own
   `NoteID` when it is among the matches — it only matters when a tenant
   holds two notes of one title, which the owner's never does.

## The same-title scoring caveat

`prod-battery-after-r6.md` counts a landing in a *pre-existing* twin of the
named note as reaching it. Under that relaxation the after-run is **22 of
32** rows three of three; the raw judge score (`r6b-judge.txt`) is **21 of
32**, and rows 1, 2 and 14 are the ones that differ (row 1: 3/3 relaxed,
2/3 raw; row 2: 2/3 relaxed, 0/3 raw; row 14: 1/3 relaxed, 0/3 raw). Both
numbers describe the test tenant's duplicates, not the router; after the
purge and re-run of do-now 1 the two coincide and the caveat goes. Until
then, any figure quoted from that report should say which it is.
