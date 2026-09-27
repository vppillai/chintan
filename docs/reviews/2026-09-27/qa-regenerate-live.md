# QA — "Regenerate from recordings" live on production (2026-09-27)

Target: Chintan dev instance / prod stack, backend `81ef9b6` (release v0.5.51, PR #138), app `https://vppillai.github.io/chintan/dev/`, test tenant `6871b3c0-1001-708e-de05-a2acbd0ceec3` (claude-test@example.com). Tooling on orb: `~/r3/live/qar-api.py` (API battery), `~/r3/live/qar-ui.js` (phone context, Playwright via `qaw1-lib.js`), `~/r3/live/qar-ui-old.js` (aged-note check), `~/r3/live/qar-lpa.py` (wire probe), `~/r3/live/qar-old.py` (aged fixture); raw data `~/r3/live/qar-api.json`, `~/r3/live/qaw1-qar-mobile-ink-{reqs,results}.json`; screenshots `~/r3/live/qar-*.png`. Only "QA R …" notes/devices were created; all purged. No AWS writes; no code changed.

| Step | Result |
|---|---|
| 1. API: create/post/tick → regenerate 202 → 409 in flight → poll → bodies → 404 | **PASS** |
| 2. UI (phone): ⋮ → Regenerate… → confirm copy → banner → item disabled → list back, tick kept | **PASS** on the fresh fixture as specified; **FAIL** on a note whose recordings are > 10 min old (F1, live-verified): strip says "Still not done — something may have gone wrong" through a healthy 5 s run and for ~55 s after it |
| 3. chintanctl regenerate dry run | **PASS** (23 notes / 30 recordings / ~$0.0011; `--dry-run` flag does not exist, dry run is the default) |
| 4. Cleanup | **PASS** (device 204; notes 204/204; purge both `purged`; the two probe fixtures purged too; no "QA R" leftovers) |

## 1. API

Fixture: `QA R plain` (`note_18d945f57f166adf_61b9859ebaffd962`, kind note) and `QA R list` (`note_18d945f58ba55a1e_37535556245b39d9`, kind checklist); device `QA R device` (`dev_0144cc037b1d`). Three texts each through `POST /v1/inbox/text` with `X-Chintan-Note-Id`, every one 202 and `appended` (targeted, no routing).

**Bodies before** (wire bodies; `GET /v1/notes/{id}` strips the capture markers — `service/notes.go:339 StripCaptureMarkers` — so markers are not visible on the wire, see "Markers" below):

```
QA R plain  v7
The plumber is coming on Tuesday morning to look at the kitchen tap.

And, like, we should um probably ask him about the, the water heater too, you know.

So yeah, um, remember to, like, move the car out of the driveway before he, uh, arrives.

QA R list  v8
- [ ] Milk
- [ ] Eggs

- [ ] Two loaves of bread

- [ ] Dish soap
```

Inputs were: plain — "Um, so, the plumber is, you know, coming on Tuesday morning to look at the kitchen tap." / "And, like, we should um probably ask him about the, the water heater too, you know." / "So yeah, um, remember to, like, move the car out of the driveway before he, uh, arrives."; list — "add um milk and eggs" / "we also need, like, two loaves of bread" / "and dish soap". Note already here: only the first plain paragraph lost its fillers (observation F2).

Tick: `PATCH /v1/notes/{list}` `{version: 8, body: …"- [x] Milk"…}` → 200, version 9.

**Regenerate:**

| Call | Result |
|---|---|
| `POST /v1/notes/{plain}/regenerate` | **202** `{"status":"queued","captures":3}` |
| same, immediately again | **409** Problem `detail: "this note is being regenerated, or a recording is being filed into it; wait for it to finish"` (captures at that moment: `transcribed, transcribed, cleaning`) |
| `POST /v1/notes/{list}/regenerate` | **202** `{"status":"queued","captures":3}` |
| same, immediately again | **409** (captures `transcribed ×3`) |
| plain: all three `appended` again after | ~4 s (poll on the note's `captures[].status`; no note-level `regenerate_state` exists, by design) → v13 |
| list: all three `appended` again after | < 2 s → v15 |
| `POST /v1/notes/{plain}/regenerate` once done | **202** `{"status":"queued","captures":3}` — **not a no-op**: the three recordings are re-cleaned again (done in ~4 s, v19). "Asking again later runs it" per the design; a completed run leaves nothing to refuse. |
| `POST /v1/notes/nt_doesnotexist000000/regenerate` | **404** `detail: "no such resource"` |

**Bodies after the first regenerate:**

```
QA R plain  v13  (3 paragraphs, same order, same meaning)
So, the plumber is, you know, coming on Tuesday morning to look at the kitchen tap.

And, like, we should um probably ask him about the, the water heater too, you know.

So yeah, um, remember to, like, move the car out of the driveway before he, uh, arrives.

QA R list  v15  (identical set, tick kept on Milk, no duplicates)
- [x] Milk
- [ ] Eggs

- [ ] Two loaves of bread

- [ ] Dish soap
```

After the second run on the plain note (v19):

```
Um, so, the plumber is, you know, coming on Tuesday morning to look at the kitchen tap.

And, like, we should probably ask him about the water heater too, you know.

So yeah, um, remember to, like, move the car out of the driveway before he, uh, arrives.
```

Mechanics verdict: paragraph count unchanged (3) across both runs, each paragraph replaced in place under its own recording (no duplicates, no orphans), the checklist's items re-extracted to the same four with the tick following "Milk" and nothing doubled. Cleanup calls landed under the `cleanup` op in `GET /v1/usage` (no `regenerate` op, as designed).

**Markers.** The wire body has no `<!-- chintan:capture:… -->` markers (stripped by `GetNoteDetail`), so "marker still present" cannot be read off the response. The evidence is indirect but strong: `RegenerableCaptures` counts a plain-note recording only when `CutCaptureParagraph` finds its marker with text under it, and the *second* regenerate of the plain note answered `captures: 3` — all three markers were still in the stored body after the first run replaced the paragraphs.

## 2. UI (phone context: Pixel 8 Pro profile, 412×915, ink theme)

Flow on `QA R list` (screenshots `~/r3/live/qar-01…06-*.png`):

| # | Check | Result |
|---|---|---|
| U1 | Header ⋮ menu: Details · Share · Pin · **Regenerate from recordings…** · Delete; item enabled | PASS (`qar-02-menu.png`) |
| U2/U3 | Confirm: title "Regenerate from recordings?", body "Re-does the AI cleanup of **3 recordings** with the current settings; edits you made inside those paragraphs are replaced. **Ticks are kept.**"; Cancel / Regenerate | PASS (`qar-03-confirm.png`) |
| — | Tap Regenerate → `POST …/regenerate` 202 `{"status":"queued","captures":3}` on the wire | PASS |
| U4 | Filing strip appears +382 ms after confirm: "Filing your recording — … Filing" (4-segment stage bar, 3rd segment active) | PASS (`qar-04-banner-inflight.png`) |
| U5 | ⋮ opened during the run: item reads **"Regenerating…"** and is disabled (Details/Share/Pin/Delete stay enabled) | PASS (`qar-05-menu-inflight.png`) |
| U6 | Server: all three captures `appended` again at +6.5 s | PASS |
| U7/U8 | List returns: Eggs · Two loaves of bread · Dish soap open, **Milk ticked in Done (1)**; no duplicates; meta line still "3 rec · 1 of 4 done", Recordings (3) | PASS (`qar-06-list-after.png`) |
| U9 | ⋮ afterwards: "Regenerate from recordings…" enabled again | PASS |

Console: one non-API 404 (`GET …/dev/notes/<id>` — GitHub Pages' 404 fallback for a deep link into the browser router; expected). No API request ≥ 400 in the whole UI run.

Timing observation that led to finding F1: the UI made only two note GETs after the confirm — at +0.1 s (the mutation's invalidation) and at **+15.2 s**. The server finished at +6.5 s, so the strip and the disabled item lingered ~9 s past the end. See F1.

## 3. chintanctl (orb, built from the synced main checkout with go1.23.4 → `/tmp/chintanctl`)

`--dry-run` is rejected: `flag provided but not defined: -dry-run` (dry run is the house default; `--apply` is the only way to execute). Run without `--apply`:

```
$ AWS_PROFILE=chintan AWS_REGION=us-west-2 /tmp/chintanctl regenerate --instance dev --tenant 6871b3c0-1001-708e-de05-a2acbd0ceec3 --all
regenerate dev (prod), priced at openai/MiniMax-M3 list price
  tenant 6871b3c0-1001-708e-de05-a2acbd0ceec3: 30 recordings, $0.0011
    note note_18d235d0ba5b53e6_6aaf917c4f5de611 note         3 recording(s)    $0.0001
    note note_18d2368425a6c751_78f1332c2b046264 note         2 recording(s)    $0.0001
    … 19 more plain notes with 1 recording each …
    note note_18d945f57f166adf_61b9859ebaffd962 note         3 recording(s)    $0.0001   ← QA R plain
    note note_18d945f58ba55a1e_37535556245b39d9 checklist    3 recording(s)    $0.0000   ← QA R list
  23 note(s), 30 recordings, about $0.0011 in cleanup calls
  The estimate is the worker's own reservation per call — transcript bytes/4 tokens in and the same out, plus one
  whole-note call over the body of each note that keeps a cleaned view — and an upper bound: …

DRY RUN — nothing was changed.
  Would: queue 30 recordings across 23 note(s) for about $0.0011
  Re-run with --apply to execute.
```

`--json` agrees: target `{instance: dev, environment: prod, region: us-west-2, table: chintan-dev-prod, bucket: chintan-content-dev-prod-338186951935}`, model `MiniMax-M3`, per-note `captures`, `transcript_bytes`, `cleaned_view: false` for all, `cost_micros` (e.g. 115 µ$ for 3 recordings / 286 transcript bytes), `queued: false` throughout, `applied: false`. Exit 0. Reads only (DynamoDB + one S3 listing); the agent principal lacks `lambda:InvokeFunction`, so `--apply` could not have queued anything anyway. Not run with `--apply`.

## 4. Cleanup

`DELETE /v1/devices/dev_0144cc037b1d` → 204; `DELETE /v1/notes/{plain}`, `{list}` → 204, 204; `POST /v1/notes/purge` → both `purged`. Sweep of `GET /v1/notes?limit=50` and `GET /v1/devices` for "QA R" → none left. The two extra probes below ("QA R lpa", "QA R old") each revoked/deleted/purged their own fixture — see their sections.

## Findings

### F1 — Note-detail captures carry `last_progress_at: null`, so the UI's poll cadence, stuck rule and the ⋮ guard run off `created_at`: a regeneration of a note older than 10 min — the feature's actual target — is shown as stuck from its first second and the screen catches up only on a 60 s poll (live-verified)

Live-verified (probe `qar-lpa.py`, fixture "QA R lpa", purged): after one recording landed,
- `GET /v1/captures/{id}` → `last_progress_at: "2026-09-27T20:06:53Z"`
- `GET /v1/notes/{id}` → `captures[0].last_progress_at: null` (same for `GET /v1/captures?note_id=`).

Cause: both listings come from `store.ListCapturesByNote` (GSI1 query, `repository/dynamo.go:1312`) whose projection has no `last_progress_at` — the design doc itself notes this for the server side ("no `last_progress_at` to judge a stuck capture by") and reads each row whole, but the handler's `capturesOf(captures.Items)` for `GET /v1/notes/{id}` (`handler/notes.go:209-216`) ships the projection as is.

Frontend consequences (all three read `note.captures`):
- `capturePollInterval` (`api/queries/captures.ts:70`): `last_progress_at ?? created_at` → age < 30 s: 1.5 s; quiet < 2 min: 4 s; **2–10 min: 15 s; > 10 min: 60 s**. Observed in step 2: recordings 2.6 min old → polls at +0.1 s and +15.2 s while the server finished at +6.5 s.
- `isStuck` (`features/capture/filing/model.ts:52`): `Date.now() - (last_progress_at ?? created_at) > 10 min`. For any recording older than 10 minutes, the moment `RequestRegenerate` puts it back to `transcribed` the app judges it stuck.
- `FilingItem` (`filing/FilingItem.tsx`): `data-stuck="true"` → "Still not done — something may have gone wrong" with a Dismiss control, for a run that is healthy and seconds old (seen live, below).
- `NoteMenu` (`NoteActions.tsx`): `regenerating = captures.some(!terminal && !isStuck)` → **false** for such a note, so the label never becomes "Regenerating…". The item still went dark in the live run, but only because `regenerableCount` counts `appended` captures and all three were `transcribed` at that instant (`regenerable === 0`); on a note long enough for the poll to land mid-run, the recordings already re-appended make `regenerable > 0` and the item comes back on while the server is still working — a tap then gets the 409 as the error line. Not reproducible on a 3-recording note (5.5 s run, 60 s poll).

Server side is unaffected (`ResetForRegenerate` stamps `LastProgressAt = now` on the full row and `RegenerableCaptures` reads rows whole), so no double run and no data damage — a UI-only misreport, but on exactly the notes regenerate exists for (made before a prompt change, so days old). Live-verified on an 11-minute-old fixture: see "Aged-note check" below. The same misreport applies to "Transcribe again" on an old recording (same reset, same wire), which predates #138.

Not fixed (read-only pass). Smallest fix candidates for the owner to weigh: add `last_progress_at` to GSI1's projection (a table change), or have `getNote` read the non-terminal rows whole the way `RegenerableCaptures` already does, or have the regenerate mutation's `onSuccess` seed `last_progress_at` client-side — one of the first two also fixes the same misreport after "Transcribe again" on an old recording.

### F2 — Cleanup output is weak and non-deterministic on filler words (prompt/model quality, not a regenerate defect)

Same three transcripts, three cleanups each (initial append, regenerate, regenerate again):

| Transcript | append | regen 1 | regen 2 |
|---|---|---|---|
| "Um, so, the plumber is, you know, coming on Tuesday…" | cleaned ("The plumber is coming…") | "So, the plumber is, you know, coming…" | returned verbatim |
| "And, like, we should um probably ask him about the, the water heater too, you know." | verbatim | verbatim | cleaned ("…we should probably ask him about the water heater too, you know.") |
| "So yeah, um, remember to, like, move the car…" | verbatim | verbatim | verbatim |

`Pipeline.clean` has no raw-text fallback (a failed call → `failed`), so these are the model's answers under the current cleanup prompt (provider openai per `/v1/usage`, `LLM_MODEL` on the instance). Regeneration does exactly what it promises — re-runs the prompt — but with this prompt/model a run can make a paragraph *less* clean than before, and "edits inside those paragraphs are replaced" then costs the person a good paragraph for a worse one. Worth a look at the cleanup prompt/model before the owner runs `chintanctl regenerate --apply` across a tenant; the checklist items prompt was stable across all runs (Milk / Eggs / Two loaves of bread / Dish soap every time).

### F3 — Minor / by design

- The progress strip during a regeneration is the ordinary filing strip: "Filing your recording", with Upload and Transcribing shown complete. Accurate enough (nothing is uploaded or transcribed), matches the design's "the strip a fresh recording gets".
- A second regenerate after completion is a full re-run (202, N captures, N more cleanup calls), not a no-op — consistent with the design; the confirm dialog is the only brake.
- Deep link into the app on GitHub Pages logs one 404 (the 404.html fallback) — pre-existing, not from #138.

## Aged-note check (fixture "QA R old", 3 recordings, regenerated 11.1 min after they landed)

Fixture `note_18d94656e6a89f6d_0f73e15bb4ea970d` + device `dev_151678427657` (`qar-old.py setup`, three texts appended 20:07:46–51Z); script `qar-ui-old.js` run at 20:18:57Z; screenshots `~/r3/live/qar-old-01-after-confirm.png`, `qar-old-02-menu-during-run.png`, `qar-old-03-after-70s.png`.

| # | Observation | |
|---|---|---|
| O0b | `GET /v1/notes/{id}` captures: `last_progress_at: null` ×3, `created_at` 20:07:46–51Z, all `appended` | wire |
| O1 | ⋮ before: "Regenerate from recordings…" enabled | PASS |
| — | Confirm → `POST …/regenerate` 202 | PASS |
| O2 | +0.76 s: server captures `transcribed ×3` (healthy, in flight). Strip: **"Still not done — something may have gone wrong"**, `data-status="transcribed" data-stuck="true"`, a **Dismiss** button | **misreport** |
| O3 | +0.97 s, ⋮ open: item **disabled** but still labelled **"Regenerate from recordings…"** (never "Regenerating…"). Off only because `regenerableCount` found no `appended` capture at that instant, not because the app thinks it is running | guard held by accident |
| O5 | Server finished at **+5.5 s**. UI note GETs after the confirm: **+0.1 s and +60.3 s** only (`CAPTURE_POLL_STUCK_MS`). The "Still not done" strip stayed up ~55 s after the run had completed, then vanished on the 60 s poll (`qar-old-03-after-70s.png`: strip gone, body regenerated) | |
| O6 | UI catches up within 10 s of the server finishing | **FAIL** |

Compare the fresh fixture in step 2 (recordings 2.6 min old): strip healthy ("Filing your recording — Filing"), item "Regenerating…", poll at +15.2 s. Same feature, same server behaviour; the only difference is the recordings' age as the app measures it from `created_at`.

Cleanup: `DELETE /v1/devices/dev_151678427657` 204, `DELETE /v1/notes/{old}` 204, purge `purged`; sweep for "QA R" notes/devices → none.
