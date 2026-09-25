# Morning queue — 2026-09-21

Everything below was collected during the overnight run (2026-09-21, 06:20–12:00 UTC). What did not need you was implemented, merged behind CI and the staging→production gate, and deployed; what needs you is in the numbered sections.

## What shipped tonight

Prod is at the latest `v0.5.x` release (backend) with the matching frontend on https://vppillai.github.io/chintan/dev/. Zero WARN or ERROR lines from either Lambda after the deploys. The full review is `round-3.md`; the two hands-on passes are `smoke-checklist-capture.md` and `qa-final.md`.

- **Swipe on tiles works again** (#67). Root cause: the pen-wedge fix from 5 Sept treated the pressed button handing pointer capture to the row as a cancel. Verified live on the Pixel profile after deploy: a sideways drag opens the tray (−144 px), a vertical drag scrolls.
- **Multi-language** (#74): a recording made from Home used to be transcribed in your default language before routing, so a note's language never applied to the common flow; now the worker transcribes again in the destination note's language when it differs. Each recording can also be **transcribed again** in another language from its menu, both languages are logged, and the cleanup and routing prompts no longer anglicise. See §1 below for the one setting only you can change (your default is Auto-detect, which mis-scripted real Malayalam as Tamil in the review's test).
- **Capture screen** (#62, #73): round icon controls with a **Send** that stops and sends without the review step; a real failure card with Try again; a pulsing live indicator; receipts say *which* note a recording was filed into.
- **Checklist notes** (#64, #65, #77): Details → Checklist turns a note into a list; recordings filed into it become items; the Items tab ticks, adds, deletes; Done items grey out at the bottom; "Split up" breaks long items into tasks and "Use this list" applies it; a **Checklists** chip on Home. Verified end to end on prod, including a recording filed into a checklist landing as exactly one item.
- **Note screen** (#76): the tab-bar mic reads "Into this note" and records into the open note; Details · Share · Archive/Delete moved into a header menu; one-line meta; recording rows show "Heard as ⟨language⟩" when Whisper disagreed; one typed-word delete ritual everywhere. Chrome above the body fell from 55 % to 30 % of the phone screen.
- **Home, Ask, You, About** (#75): leaner Home (first note at 24 % of the screen, was 45 %), the Ask toggle inside the search field, one Ask input with source chips, Usage behind one row with its own route, retention as the real tiers, **Download my notes**, plainer About wording.
- **Platform** (#72): deep links open from the cached shell, the first page reads offline, sign-out revokes the refresh token, Malayalam chillu folding in search.
- **Backend hardening** (#71): a Clean tap survives a concurrent save; a spend-capped recording can be resent the next day; exports are tagged for lifecycle expiry.
- **Security and infrastructure** (#69, #70, #68, #59, #60, #63, #78): **self sign-up on the production user pool is closed** (verified: `AllowAdminCreateUserOnly = true`); the unused Cognito admin scope is gone from tokens; a user-offboarding path; the `golang.org/x/net` advisory fixed; frontend toolchain bumped to Vite 8 / Vitest 5 / TypeScript 6.
- **Prod data**: the reconcile repairs that needed no decision were applied — 25 recordings without a stored size fixed, 2 legacy recordings re-indexed, 4 orphaned capture rows of a dead tenant removed. Only the two legacy notes (§Decisions 1) remain.

## The final QA pass and what it changed

A hands-on pass over the new UI on the phone and desktop profiles (`qa-final.md`, 15 findings) found one High and five Mediums; all were fixed and deployed before morning (#79, #80, #81):

- **High:** a Details save (language, checklist, verbatim, auto-clean) sent the unchanged body with every PATCH, which the server read as an edit and moved every recording's marker to the end, so "Transcribe again" duplicated a paragraph and "Delete recording" left the text behind. The editor now sends only the fields that changed, and the server keeps a marker beside its paragraph when that paragraph is unchanged. One honest limit remains: if you type new text directly *below the last recording* in a note, the recordings whose paragraphs run into that text lose their marker again (nothing is lost; Delete/Move/Transcribe-again then act on the marker alone). A storage-shape change (an end fence after each paragraph) would close it; it is in the backlog as a follow-up.
- **Mediums:** "Transcribe again in ⟨language⟩" now records the requested language on the wire; Ask answers name notes by title instead of raw ids; the offline body prefetch runs to completion (five cold loads had stored 20, 1, 1, 20 and 1 bodies); the archive view has its own "Archived · N" heading; the Checklists chip count no longer shows a phantom "+".
- **Lows:** Details sheet capped and scrollable, destructive menu items never wear the accent, checklist items in the Into chooser shown as words, the recording screen no longer reflows when the chooser opens, "+1 more" shows the chip, About copy matches the new mic label, "Archive · 0" hidden at zero, single-note bulk delete names the note, offline caption spacing.

One thing the pass could not make appear: the "Heard as ⟨language⟩" chip on a recording only shows under **Auto-detect**, because Whisper echoes a requested language as the detected one. Under a chosen language the chip has nothing to disagree with; that is by design of the provider, not a bug in the app.

## Decisions

A. **Two legacy August notes** (`note_1786145611625087752`, `note_1786154337360555961`) are archived without a purge deadline, so neither the notes list nor the archive shows them and no sweep collects them. Options: (a) restore them into the Archive so you can look and delete; (b) purge them with their two recordings. Recommendation: (a) — `chintanctl reconcile -instance dev -tenant <your sub> -apply -only unlisted_note` re-promotes them; they then appear under Archived.
B. **"After designating the first note as a checklist, all subsequently added notes should also be created as checklists."** Implemented as: recordings added to a checklist note become items. If you meant "new notes default to checklists", say so and it becomes a switch on You.
C. **PWA shortcut starts recording immediately** ("When a note is opened from the screen widget, it directly starts recording"). Today the home-screen shortcut is "Record a thought" → `/capture`, which opens the microphone at once by design. If you want a confirmation step, or a second shortcut that opens the library, say which.

## Actions

D. **Re-apply the read-only agent policy** so future reviews can read alarm state, metrics and DLQ depth: on orb, with your admin profile, `scripts/bootstrap-agent.sh --apply`. (Denied again this run.)

## From the round-3 review (`round-3.md` §4)

Numbered as in the report, so its "section 4, item N" references resolve here; the lists above are lettered so "item 4" is unambiguous. Decisions and one-off admin commands only; everything marked DO-NOW in §2 is implemented in the stream PRs.

1. **Transcription language.** Your live default is Auto-detect, which re-scripted real Malayalam as Tamil and dropped a Malayalam sentence from a mixed clip; forcing Malayalam kept both languages and transcribed pure English verbatim, though one human mixed clip under `ml` still came back poor. Options: (a) set You → Transcription language to Malayalam; (b) also trial `whisper-large-v3` by setting `GROQ_STT_MODEL=whisper-large-v3` on the worker (about three times the per-hour price, cents a month at your volume) and compare one real Malayalam and one mixed clip before and after. Recommendation: both, (a) now and (b) for a week. (T3, T59)
2. **Per-recording language control.** Tonight's fix re-transcribes a routed recording once in the destination note's language when it differs. If wrong-language transcripts persist after that, the remaining option is a second pill beside "Into" on the capture screen ("In · മലയാളം"), remembered per device, which needs `language` on `CaptureCreate`. Recommendation: wait a few days on the fix before adding a control. (T2)
3. **Branch protection on `main`.** Options: (a) a ruleset requiring a pull request and the eleven CI status checks, no required reviewers, plus block force-push and deletion, which also makes the environments' "protected branches only" policy bite; (b) leave as is. Recommendation: (a); the overnight agent still merges green PRs, and a red PR can no longer reach production. `setup.sh` should create it for a fresh clone. (T24)
4. **Termination protection.** Two commands with your admin profile: `scripts/setup.sh --region us-west-2 --apply` redeploys `chintan-bootstrap` with the `cloudformation:UpdateTerminationProtection` grant for the GitHub role and protects the bootstrap stack itself; `scripts/bootstrap-agent.sh --apply` (the same command as D) gives the agent the grant too, since `cleanup-aws.sh` and `teardown.sh` now switch protection off before they delete and would otherwise stop at AccessDenied. `chintan-dev-prod` and `chintan-dev-staging` are then protected by their next deploy (`deploy.sh` re-asserts it on every deploy); to protect them sooner, `aws cloudformation update-termination-protection --enable-termination-protection --stack-name <stack>`, once each. Recommendation: yes, all of it. (T25)
5. **Legacy data.** DONE tonight except the two notes in Decisions 1: the dangling and unindexed capture rows were repaired (`chintanctl reconcile … --apply --only dangling_capture --only unindexed_capture`, to remove four dangling capture rows and re-index three. Tenant `d8a16350…` has no settings and no notes; the pool also holds a `you@example.com` from the README example. Recommendation: erase the dead tenant and delete the stray user while you are there. (T33, T29)
6. **Dead secrets.** `gh secret delete AWS_ROLE_ARN` and `gh secret delete AWS_REGION`; the code that set or documented them is gone tonight. Recommendation: delete both. (T65)
7. **Cleanup latency tail.** About one capture in ten waits 10–14 s for MiniMax before any text shows. Options: (a) two-phase append, raw paragraph at `transcribed` replaced when cleaned, text visible in about a second at no extra cost; (b) an 8 s cleanup budget with verbatim fallback and a counter. Recommendation: (a). (T27)
8. **Content-Security-Policy meta.** Would make an injected script inert while tokens live in `localStorage`, but must be tested on your Pixel (blob: audio, presigned S3 PUT/GET, Cognito redirects). Recommendation: yes, next round, with you holding the phone. (T53)
9. **About wording.** Implemented tonight as: "Stored in the AWS account of whoever runs this instance (yours, if you deployed it); the operator can export or read everything with the admin tools. Your speech goes to the transcription provider once; the text goes to the language model to route it, clean it, optionally rewrite the whole note, and to answer Ask questions over the notes it selects. Neither provider is asked to retain anything." Recommendation: read it once; it is your promise to anyone you invite. (T28)
10. **Shared spend cap before inviting anyone.** A second user shares your $5/day provider cap and the 50 rps gateway throttle. Options: (a) accept for the alpha; (b) a per-tenant daily cap (L, declined in H9). Recommendation: (a) until a second user actually exists. (T29)
11. **Self-service account deletion.** "Download my notes" ships tonight; "Delete my account" needs `AdminDeleteUser` under the API role behind a typed confirmation. Recommendation: not for a one-user alpha; document `chintanctl erase` as the path instead. (T30)
12. **App icon.** The placeholder microphone glyph is still on the install sheet and the sign-in page; manifest `id`, categories and screenshots ship tonight. Recommendation: pick from an icon sheet next round, as you asked in U9. (T57)
13. **CloudTrail cost.** The trail's ~600 small objects a day cost more than DynamoDB and API Gateway together, on a $0.20 bill. Recommendation: leave it; the security value exceeds nine cents. (T61)
14. **Read-only agent policy.** Same as D.
15. **Review three restructures on your phone**: the note screen (T6), Home (T17) and You (T20). Each is reversible in one PR; say which, if any, you want back.

# Update — 2026-09-24

Your five points from the 24th (plus the desktop-selection remark) were implemented and deployed the same day. What each became:

- **Recording into a note returns to that note.** Send (or Stop → Send) goes back to the note on the tab you were on, and a slim filing banner under the header shows the stages until the text lands; the Recordings tab still lists the row.
- **Push-to-talk.** Hold the tab-bar mic for a third of a second: it records in place with a small overlay ("Release to send · slide away to cancel"); release sends. There is also a full-screen **Hold to talk** page (`/talk`, and a home-screen shortcut of the same name) for one-handed quick captures, one after another. A real Android home-screen *widget* is not something a web app can provide; see Decisions.
- **Injecting recordings from other devices.** You → **Devices & shortcuts** creates a device key (shown once). Any device or app that can send one HTTPS request with one header can post audio (`POST /v1/inbox/audio`, one shot, ≤4 MiB) or text (`POST /v1/inbox/text`), and it files like a normal recording; the card carries copyable curl, iOS Shortcut and Android HTTP-Shortcuts recipes. Keys are hashed at rest, revocable, and capped at 200 requests a day each. Verified live with a real upload through a device key.
- **Pin and reorder.** Pin from a row's tray, ⋮ menu or the note's header menu; pinned notes sit in a Pinned group at the top of Home and can be dragged into any order (grip handle on desktop, long-press-and-drag on the phone). The "newest at the bottom" report could not be reproduced: the list is served and shown newest-touched first, and every write bumps a note's timestamp; a new recording's *text* does land at the end of its note by design. If you still see it, a screenshot of Home with the note's name will pin it down.
- **Desktop selection.** The hover checkbox is gone; press-and-hold with the mouse enters selection like a finger does, and every row has a ⋮ menu (Pin · Archive · Delete · Select).
- **Checklists.** Ticking in Split up now works (the first tick replaces your list with the split version and says so); the boxes and ticks are drawn in the app's own stroke, with an animated tick and faint struck-through done items.

## Decisions (new)

5. **A real home-screen widget or a hardware button** (for the ring/watch use) needs a thin native wrapper (Android TWA with a widget, or an iOS Shortcut bound to the Action button posting to the inbox). The inbox is ready for either. Say if you want the Android wrapper built.
6. **Pinning from a stale list** can answer 409 because a pin PATCH carries the note's version. Recommendation: let a pin-only PATCH skip the version check (small backend change) — approve and it ships.

## Status at the end of the afternoon (24 Sept)

Everything above is merged and live: frontend 8da7821 (#88 pins and reorder, #85 return-to-note and hold-to-talk, #84 checklists, #83 Devices & shortcuts), backend v0.5.29 / ea7ab37 (#87 inbox and pins; #89 fixes two one-in-sixteen flakes in the device-key test that had failed #84's CI). Prod logs: 0 WARN/ERROR since the deploy window.

**Live QA on prod, test tenant** (`orb:~/r3/live/qa0924-*`, 60 screenshots):

| Flow | Result |
|---|---|
| A Home on a phone: long-press select, swipe tray, ⋮ Pin, Pinned group, reload, drag-reorder | PASS 18/18 |
| B Home on a desktop: no hover checkbox, mouse hold, Shift/Escape, ⋮ by mouse and Tab/Enter, grip drag and arrows | PASS 17/17 |
| C Ordering: a note recorded into rises to the top, live on an open Home (6.5 s) and after a reload | PASS |
| D Return-to-note: Send → the note's Text tab, banner between header and tabs, cleared 4.8 s after Send, paragraph appended | PASS |
| E Hold-to-talk: tab-bar mic and `/talk`; appended 3.7 s / 2.1 s after release; a short hold shows the hint | PASS 12/12 |
| F Regressions: no console errors on any screen, no "Filed" receipts with zero notes | PASS |
| G Checklists: live Split up (one PATCH, caption, then the body), drawn boxes, grey + strike | 8/9 — the ticked item did not hold before moving |
| H Devices: create, key shown once, list, three recipes, inbox text → appended, no player, Remove → 204 and the key answers 401 | 10/11 — the row did not say "From ⟨device⟩" |

Both misses are fixed the same afternoon:

- **G4 (#91).** `--motion-duration-base` is `220ms` in the source sheet and `.22s` in the built one; the hold parsed it with `parseFloat`, so production held for 0.22 ms and every test held for 220. The parser now honours the unit.
- **H5 (#90).** `GET /v1/captures/{id}` said `device:…` while the same capture inside `GET /v1/notes/{id}` said `app`: the by-note page reads gsi1, whose projection does not carry `source`. The row now carries `source` at the top level and the page overlays it with one `BatchGetItem`, the way the notes list already fetches its large fields. See Decisions 7.
- **Found while cleaning up (#92).** Two test-tenant captures whose upload never landed (the HOME-4 rows) have shown "Still not done" since 04:22Z and `DELETE` answered 409 for ever: delete refused every pending status, while retry, move and re-transcribe already allow a capture nobody can still be working on (15 minutes). Delete now uses the same rule.

**Also seen, no action:** Chromium logs `ERR_ABORTED` for every successful presigned S3 PUT and the device `DELETE` 204 (the requests complete; unconsumed empty bodies). The orb VM froze for about an hour mid-QA because the Mac's swap filled (12 of 13 GB); nothing on the VM was touched and it recovered on its own, but it will happen again until some apps are closed.

## Decisions (new, continued)

7. **`source` in gsi1's projection.** CloudFormation cannot change a live index's projection, so #90 pays one extra read per by-note page instead. The clean end state is `source` in `NonKeyAttributes`, which means deleting and re-creating gsi1 by hand (two stack updates; the note screens and device-key lookups are degraded while it backfills, minutes on this table). Recommendation: do it in a quiet hour when you next touch the template, then delete `hydrateCaptureSources`. Not urgent.

## Round 4 (evening of the 24th)

A second double-blind review ran over the day's ten merges: eight lenses, adversarial verification of every Medium-or-higher claim, one reconciled report at `docs/reviews/2026-09-24/round-4.md`. Nothing Critical or High; seven Medium defects at the seams between the parallel streams and a tail of polish, thirty-seven items in all, are being implemented in seven streams (S1–S7 in the report's §5) and merged as they go green. Three things need you:

8. **Decision 6 with new evidence: a pin-only PATCH still carries the note's version, and the app's own pin makes its list stale, so Pin → reopen ⋮ → Unpin at natural speed is refused 409 and the note silently stays pinned** (R4-3). (a) Approve Decision 6 as recommended: a pin-only PATCH skips the version check (small backend change in UpdateNote); the frontend fix in S2 ships regardless. (b) Frontend fix only, keep the check: the window shrinks to the PATCH's own RTT but does not close. Recommendation: (a). Evidence: live API PATCH {version:2,pinned:true} → 200 v3, PATCH {version:2,pinned:false} → 409 current_version 3; live browser on prod, Pixel profile, no throttling, menu reopened 127 ms after Pin → 409, row remained in the Pinned group with no error text.

9. **Decision 7 amended: the queued delete/re-create of gsi1 takes the inbox fully down for the gap (device-key lookups live on gsi1), and a zero-downtime alternative uses the same two stack updates** (R4-39). (a) As queued: remove gsi1 in one update, re-add it with `source` in NonKeyAttributes in the next; every by-note capture page, the export walk and every device-key lookup fail during the gap (inbox answers 401/500), seconds to minutes on this table (252 items, gsi1 59 items). (b) Add gsi2 with the wider projection in one deploy (one index per update is allowed; CFN does not wait for backfill), switch the reader's index-name constant in dynamo.go and delete hydrateCaptureSources once describe-table shows ACTIVE, drop gsi1 at the next template touch; IAM already wildcards the index ARN, so no policy change and no window where the inbox is down. (c) Keep hydrateCaptureSources (one BatchGetItem per by-note page, <$0.01/month at today's volume) as the permanent answer. Recommendation: (b), when the template is next touched. Note: editing the live gsi1's projection in place is refused by DynamoDB and rolls the stack back with the table untouched, so there is no accidental-rebuild risk.

10. **The Pinned group on the phone: six pins fill the first screen with no collapse; and review of R4-4's new 'Move up'/'Move down' menu items** (R4-15). R4-4 ships Move up / Move down in a pinned row's ⋮ as the gesture-free reorder path (WCAG 2.5.7); look at it once and say if you would rather have a faint 'Hold a note to move it' caption under the Pinned heading instead. On the group's height (live: six pins ran 252–970 px on a 915 px screen, Today at 990): (a) show the first three pinned rows and a 'Show all N pinned' toggle (a <details> around rows 4..N, open state in localStorage); (b) make the Pinned heading a collapse toggle; (c) leave it — the group's point is to be first, and your own pin count decides. Recommendation: (c) until your pins pass about five, then (a).

Also from your message on the night of the 24th: moving a recording to a *new* note from the Move sheet (type a name, the note is created, the recording and its text move into it) is being built in streams S1 (the move endpoint accepts `new_note_title`, like the Into chooser's target already does) and S4 (a "New note…" row in the sheet). Moving a recording already carries its text: the paragraph is cut from the old note and inserted into the new one in time order among its recordings, and both notes are re-indexed (`capture_move.go`).
