# Backend lens — platform review, 1 Oct 2026

Reviewed main `53bf8a23` (Details sheet: fixed head, R8-S1, #203), read-only. Baseline for "what rounds 7–8 did" is `69d1f2b` (30 commits, 69 backend files, +5,007/−460). Scope `backend/internal/*`, `backend/cmd/*`. Measurements ran on an orb mirror (`~/temp/chintan-r9-backend`), Go 1.23.4 toolchain auto: `go vet`, `staticcheck`, `deadcode` (with and without `-test`), `go test -cover`, `go test -race -count=3 -p 2 ./...` (run twice; the first script lost the race step to `sh`'s missing `time`, the second ran visibly), and an AST function-length scan of head and of `69d1f2b`.

Not re-reported (recorded open elsewhere): checklist depth 3 vs 4, DB6-9, the recorded owner phone checks, the un-recorded routing replay set, the 320 px capture flake, R7-14a, R8-F8b's frontend half, DB6-8/DB6-10/DB6-30, the merged-child-inside-a-paragraph limit (`checklists.md:262-268`).

## Counts

High 2 · Medium 4 · Low 7. Owner decisions needed on BE-1, BE-3, BE-4 (chillu part), BE-5 (whether new rescue rules wait for recordings).

## Findings, ranked

### BE-1 · Tidy accepts a model answer that drops open items and prose lines; the design doc says the guards refuse it — High, S/M, owner decision yes

- `backend/internal/cleanup/prompt.go:230-369` (`SplitOutput`); claim at `docs/design/checklists.md:640-643` ("`SplitOutput`'s guards (below) refuse an answer that drops, invents or re-ticks an item"); prompt-only rule at `prompt.go:90` ("Every line's meaning is kept: nothing dropped").
- Evidence: reproduced (one-off test on the mirror, removed) and read. Body `Milk / Eggs / Bread / Rice` all open, reply `{"items":[{"text":"Milk"}]}` → stored `- [ ] Milk`, dropped=0, err=nil. Body `Milk / "Remember the coupon" (prose) / Eggs`, reply Milk+Eggs → prose gone, err=nil. The refusal set at `prompt.go:332-344` is exactly: a done item lost, a done item reopened, an open item closed, a done item invented; plus invented items dropped and the 500 cap. No arm checks that an open body line survives in some answer item. `TestSplitOutputRefusesALostReopenedOrInventedTick` pins ticks only.
- Why it is risk: until #195 the tasks view was a proposal in a second tab. Since R8-F8 **Tidy up list writes the view over the body** with a 6 s Undo, and a prose→checklist conversion auto-tidies on landing — the case where a model most wants to summarise. A dropped open item is unrecoverable from the UI (S3 noncurrent versions are 90 days but not user-reachable). The R8-F8 backlog row says the guards "remain the only check before the write" without recording that open lines are unguarded, and the design doc states the opposite of the code.
- Fix: one more guard in `SplitOutput`: every open body line (folded words) must be a sub-sequence of, or contain as a sub-sequence, some kept answer item (`llm.VerifySubsequence` both ways, so "Milk" + "Milk 2 litres" merging to one item still passes); otherwise refuse whole with `ErrNotATaskList: an open item was lost`. One table test (the two cases above). Correct the doc sentence. Owner decides whether a legitimate merge of two open lines that share no words (rare) may be refused — the safe default is yes.

### BE-2 · A move does not stamp the target note, so an editor save during the move loses the paragraph in both notes — High, S, owner decision no

- `backend/internal/service/capture_move.go:181-234` (steps 2–4 of `moveInto`); `backend/internal/service/notes.go:494-560` (`UpdateNote`), `notes.go:445-535` (`ErrAppendInProgress`, the S1 stamp).
- Evidence: traced. S1 (backlog:166) closed the editor-save race for the worker's append by stamping the row (`StampNoteAppend`, version+1) before the body write, so `UpdateNote` refuses with `ErrAppendInProgress`. `moveInto` writes the target body (step 2), cuts the source (step 3) and only then refreshes the target row (step 4); `grep StampNoteAppend capture_move.go` is empty. Interleaving on the target: an editor PATCH arrives after step 2 with a body loaded before the move → `UpdateNote` reads the fresh ETag (post-insert), the row version is unchanged (no stamp, no refresh yet), `CarryCaptureMarkers` carries the marker but not the paragraph → `PutIfMatch` succeeds with the client's text → step 3 cuts the paragraph from the source. Text in neither note; an empty marker survives. Window = two S3 rewrites + one refresh. Needs the target open on another device (or a Tidy/regenerate apply PATCHing it). `capture_edit_test.go` has no save-during-move test.
- Why it is risk: R7-1/R7-1a's "moves never lose text" holds for crashes and in-process faults; the one concurrent writer the system already guards against elsewhere is unguarded here. Same class as S1, which was rated High.
- Fix: stamp the target with `StampNoteAppend` before step 2 and clear it in the step-4 refresh (`ClearAppendStampFor`) — the mechanism `append.go:142-172` already uses; no new code path. Optionally stamp the source around step 3 (a stale save there resurrects the paragraph without its marker: duplication, not loss). One test: `UpdateNote` between the insert and the refresh gets `ErrAppendInProgress`.

### BE-3 · 65 metrics emitted, 3 alarmed, 0 on any dashboard; every round adds counters nothing reads — Medium, M, owner decision yes

- `backend/internal/obs/metrics.go` emitters across `internal/` (65 distinct names, non-test grep); `infrastructure/template.yaml:2292,2369,2412` alarm only `InboxKeyRefused`, `ProviderKeyRejected`, `SpendCapRejections`; no `AWS::CloudWatch::Dashboard`; `docs/ops` holds one file. `internal/obs/rollup_test.go` `TestEveryAlarmedMetricIsRolledUp` checks alarm → emitter only.
- Evidence: measured. r6–r8 added `CaptureCleanupTidied`, `CaptureNoSpeech`, `CaptureHintEcho`, `RouterCreateDeduped`, `ChecklistItemsWithdrawn`, `TargetedInstructionCheck`, `TasksItemsDropped`; none alarmed, dashboarded or read by chintanctl. Failure counters an owner would want paged on are emit-only: `CaptureStageFailures`, `PushSendFailures`, `ExpiredNotesFailed`, `WorkerMessagesDiscarded`, `NoteCleanInvokeFailures`, `UsageRecordFailures`, `ProviderTimedOut`. `CaptureMoveUnrecovered` is gone everywhere (R7-1, confirmed).
- Why it is debt: write-only EMF costs a dimension each and gives nothing back; agents cannot read metrics (denied), so they are not even a review tool; the habit "a rescue ships with a counter" is accreting with no consumer.
- Fix: owner picks the failure counters that page (the five above) → `CountWithRollup` + alarms; a 20-line reverse test (every emitted name is alarmed or listed in one `docs/ops/metrics.md`) in the shape of `TestEveryAlarmedMetricIsRolledUp`; delete what neither list wants.

### BE-4 · Seven word-splitting rules are still in force; R7-2's `IsWordRune` unified two, and a new one was added after it without it — Medium, S (chillu part: owner decision)

| Function | Rule | Divergence from `llm.IsWordRune` |
|---|---|---|
| `llm.FoldWords`/`comparableWords` (`llm/llm.go:78-97`) | lower; Letter, Digit, Mn, Mc minus Variation_Selector | baseline; no chillu fold |
| `routing.NormalizeSpeech` (`routing/spans.go:157`) | IsWordRune + `'` | intended |
| `ask.Tokenize` (`ask/ask.go:147-151`) | FoldScript, inline Letter/Digit/Mn/Mc | **selectors kept (pre-R7-2a rule)**; chillu folded |
| `service.filenameSlug` (`service/capture_recordings.go:165`) | Letter/Digit/`IsMark` | Me and selectors kept |
| `provider.normalizePhrase` (`provider/stt.go:163`) | Letter/`IsNumber` | **no marks; added in R7-10d (#186) six PRs after R7-2 (#180)**, in a package that already imports `routing` |
| `match.tokens` (`match/match.go:24,122`) | `[^a-z]+` | ASCII only; any non-Latin title has zero token overlap in `MatchNotes` |
| `service/search.go:238-290` | FoldScript, lower, whitespace | no word-rune rule |

- Evidence: read; `llm.go:95-97`'s own comment records that `ask.Tokenize` and the slug "keep marks for the same reason" — the duplication was noticed and left. Same stage, two sentence splitters too: `echoesHints` splits on `,.;\n` (`transcribe.go:321`), `silenceHallucination` on `.!?\n` (`stt.go:147`).
- Why it is debt: R7-2 → R7-2a was exactly "fixed in one place, missed in another"; the next Indic/emoji fix repeats it. `ask.FoldScript` (chillu/ZWJ) is applied in Ask and search but not in `FoldWords`, so a Malayalam item dictated with the ZWJ chillu form and the same item typed atomically are two items to the merge and one to Ask.
- Fix: `ask.Tokenize`, `filenameSlug`, `normalizePhrase` → `llm.IsWordRune`/`routing.NormalizeSpeech` (three one-line edits). `match.tokens` → the same fold, or delete `match` if `MatchNotes` has no live caller. Chillu: move `FoldScript` into `llm` and apply in `comparableWords` — owner decision, it changes which stored lines compare equal.

### BE-5 · The routing rescue set is accreting, not converging: 14 deterministic rules, 8 numeric bounds in 3 packages, none retired, 4 of them under the pure/replayed half — Medium, M, owner decision partial

Rules and bounds in force at head (all traced):

| # | Rule | Bound | Where | Pinned by |
|---|---|---|---|---|
| 1 | exact title/alias/tag match on a "new" title | — | `route.go:599` `titleNames` | `TestANewNoteTitledLikeAnExistingNoteIsAppendedToItInstead` |
| 2 | prefix_title / prefix_transcript, longest name | name ≥ 2 words or ≥ 8 letters (`prefixRuleName`) | `route.go:516-531,588` | `TestARecordingThatOpensWithANoteNameIsFiledIntoIt`, `TestThePrefixRuleKeepsIndicVowelSigns` |
| 3 | spoken_name on the model's own unsure append | name + "note"/"list", or `NamedAfterCue` (cue + ≤1 of my/the/our) | `route.go:565`, `spans.go:200` | `TestAnUnsureAppendIsTakenWhenTheSuggestedNoteIsSpokenAsAName`, `TestNamedAfterCue` |
| 4 | append vs ask | `routeConfidenceThreshold` 0.75 | `pipeline.go:52`, `route.go:375` | `TestCompleteCaptureAsksWhenRoutingIsUncertain` |
| 5 | span growth over the following title / trailing "note" | k < title words | `spans.go:118` `ExtendSpans` | `TestExtendSpansClosesTheGap…`, two provider tests |
| 6 | spans bound | `MaxInstructionWords` 24 | `spans.go:28` | `TestRemoveSpansRefusesToRemoveDictation` |
| 7 | DB6-4 un-grown fallback | title > `maxNameWords` 5 | `openai_router.go:35,143` | `TestRouteKeepsTheDictationWhenGrowingOverTheTitleWouldEmptyIt` |
| 8 | empty-content guard | `maxInstructionOnlyWords` 20, `maxSpokenTitleWords` 8 | `openai_router.go:23,26,177` | `TestRouteKeepsTranscriptWhenTitleSwallowedTheDictation` |
| 9 | derived content is a sub-sequence | — | `openai_router.go:186` | `TestRouteKeepsTranscriptWhenSpansWouldLoseDictation` |
| 10 | same-second re-check before create | `maxRouteCandidates` 200 drained again | `route.go:208-223` | `TestASiblingCaptureCreatingTheSameNoteIsAppendedToNotDuplicated` |
| 11 | targeted-capture instruction gate | 37 cues (`instructionCues`) | `spans.go:168` | `TestMentionsInstruction` |
| 12 | fallback title | 6 words / 40 runes | `route.go:765` | `TestFallbackNoteTitleIsTheFirstWordsOfTheTranscript` |
| 13 | no-speech: letter test, 9 silence phrases, score pair 0.6/−1 | — | `stt.go:99-135` | `TestNoSpeechUsesWhispersSegmentMeasures`, R7-10d/e tests |
| 14 | hint echo; short-dictation tidy | `minHintAudioMS` 1500, `maxHintNotes` 50; `shortDictationWords` 12 | `transcribe.go:249-338`, `clean.go:143-247` | `spelling_hints_test.go`, `clean_short_test.go` |

- Evidence: measured. Since `69d1f2b` rules 13–14 and the hint path were added (R7-10b/c/d/e, R7-15); zero removed; R7-10a was added and reverted (#175 → #185); R6-RT-8's Titles sentence was reverted but its code bound stayed (`maxNameWords`'s comment at `openai_router.go:27-31` cites the reverted sentence as its provenance). Three title bounds coexist: provider `maxTitleLen` 120, service `maxNoteTitleLen` 200, handler `MaxTitleRunes` 200 — `route.go:189` sanitises a title the provider already sanitised, with a different bound. 23 code comments in `route.go`/`spans.go`/`openai_router.go` cite a battery row or a DB6/R6-RT id as the reason for a constant; the rules are described in prose in `prompts.md` §Routing (lines ~130–210) but the numbers are tabulated nowhere.
- `decide()` (`route.go:366`) **is** pure (no I/O, no logging; `existingNoteNamed` → `routing.NormalizeSpeech` only) and covers rules 1–4. `TestRoutingEvalReplay` reaches rules 5–9 through `provider.Route`. Rules 10–14 and the three title bounds are outside any replay. The harness `t.Skip`s when `provider/testdata/eval/recordings/` is empty (it is; known), so CI shows green for the prompt→outcome path today; once recorded, a prompt change does fail (`RecordingKey` = sha256 of model + system + user prompt — the design is right).
- Why it is debt: each rescue was a one-line fix to a battery row and each came with a test, which is the good part; but the bounds disagree with each other (a "name" is ≤5 words to rule 7, ≤8 to rule 8, ≥2 words/8 letters to rules 2–3, 6 words to rule 12), the two prompt experiments left orphaned code bounds, and the next battery row will add rule 15 in whichever file is open. Reasoning about "why did this file where it did" now needs four files.
- Fix: one `routing.Bounds` const block (or one table in `prompts.md` beside the matched_by list) that every number above lives in or is cross-referenced from, with the one test that pins it; drop the stale R6-RT-8 reference; fold the three title sanitisers into `service.SanitizeTitle` with one bound. Owner decision: whether new rescue rules wait for the replay recordings (the R7-10a revert rule already says prompt text does).

### BE-6 · The stage functions grew in rounds 7–8 and still mix decision with I/O; no size gate exists — Medium, M, owner decision no

| Function | 30 Sept (`69d1f2b`) | Head | Mixes |
|---|---|---|---|
| `cmd/chintanctl/reconcile.go runReconcile` | 321 | 321 | scan, six classifications, repair switch |
| `pipeline/route.go Pipeline.route` | 175 | **191** | 5 I/O calls (Get, Put, getNote, DrainNotes, CreateNoteOnce), 5 outcome branches, decision log |
| `pipeline/transcribe.go Pipeline.transcribe` | 112 | **174** (+62) | presign, hints read, breaker call, 2 S3 puts in a goroutine pair, 3 gates (echo, NoSpeech, language outcome), status choice, defer/persist choice |
| `cmd/worker/main.go setup` | 169 | 169 | wiring |
| `pipeline/clean_note.go Pipeline.CleanNote` | 161 | 167 | row read, body read, breaker, 6 verdicts, ETag re-read, versioned write |
| `cmd/api/main.go init` | 156 | 156 | wiring |
| `cleanup/prompt.go SplitOutput` | 93 | **138** (+45) | pure, but 5 maps and 5 nested recursive closures; order-dependent (`gained` before the lost-tick loop) |
| `pipeline/append.go Pipeline.append` | 127 | 132 | half comments; I/O already split out |
| `service/capture_move.go moveInto` | 84 | **122** (+38) | the 4-step protocol plus two compensations |
| `pipeline/append.go replaceChecklistItems` | 85 | **105** | pure, pinned |

- Evidence: measured (AST scan of both trees). `backend/` has no `.golangci.yml` (ci.yaml:85 says so; v2 defaults = errcheck/govet/ineffassign/staticcheck/unused), so nothing in CI notices a function crossing 200 lines.
- Why it is debt: `transcribe` is the clearest case — three classification rules landed in it in one round, each a few lines, each inside the I/O; the next gate (a fourth silence shape) goes in the same place. `route` is the second: R7-21 added the create-once and archived-own-note branches inside the function instead of in a pure "decision + note state → outcome" step beside `decide()`.
- Fix: extract `transcriptOutcome(result, hints) (status, metric)` and a pure `routeOutcome(decision, noteState)` (the same split R7-9 gave `decide()`); split `SplitOutput` into `dropInvented` + `tickSafety` (+ the BE-1 guard) with a table test per guard sentence; add a source-reading ceiling test in the shape of `TestHandlerDispatchesEveryTaskConstant` ("no non-test function over N lines except this allow-list") or `funlen` for new code. `runReconcile`: lift each classification into `func(idx, present, sizes) []finding`, I/O at the edges (effort M, low urgency: unchanged since 30 Sept and covered by 16 fake-backed tests).

### BE-7 · `SetCaptureTarget` with `new_note_title` still has the pre-R7-21 shape: fresh-id `CreateNote` before the versioned row write — Low, S, owner decision no

- `backend/internal/service/capture.go:638-664`. Evidence: traced. A 5xx or crash between `CreateNote` and `PutCapture`, or two `/target` POSTs with different idempotency keys, leaves an empty orphan note; `idempotent()` does not replay 5xx (`idempotency.go:187`), so the client repeat makes a second. `MoveCaptureToNewNote` compensates with `discardUnusedNote`; this path does not. No crash/retry test.
- Fix: `CreateNoteOnce(ctx, userID, NoteIndex{ID: routedNoteID(captureID), Title…})` here too; the archived-note case already answers `ErrNoteArchived`.

### BE-8 · `chintanctl restore` drops the `capture-audio` object tag that R7-3 made load-bearing; restore overwrites live rows on `--apply` alone — Low, S, owner decision no

- `backend/cmd/chintanctl/awsports.go:335` (`s3Blobs.Put`, no `Tagging`), `backup.go` (no tag read), `internal/upload/upload.go:41,112` (signs the tag), `infrastructure/template.yaml:1103-1107` (7-day `ExpireCaptureAudioVersions` keyed on it); `restore.go:184` `Part.Put` unconditional. Evidence: traced; `TestBackupRestoreRoundTripIsExact` passes because the fake blob store has no tags. The 3 Sept history review's H12 (typed confirmation for restore) was never carried into `docs/backlog.md` (that file's H12 is a passkey row).
- Fix: backup records `GetObjectTagging` into the manifest, restore re-applies via `PutTagged`; refuse a restore into a non-empty partition unless `--overwrite`.

### BE-9 · Two body→tree readers in `cleanup/items.go` with different rules; `ItemsFromLines` turns a capture marker into an item — Low, S, owner decision no

- `items.go:370-385` (`ParseLines`) vs `items.go:396-431` (`ItemsFromLines`). Evidence: reproduced. `ItemsFromLines("- [ ] A\n<!-- chintan:capture:c_1 -->\n  - [ ] B")` → items `A`, `<!-- chintan:capture:c_1 -->` › `B`; `ParseLines` gives the marker ok=false and B under A. No live caller passes markers today and nothing pins that; `checklists.md:81-84` says the two differ "on one thing only" (indented prose), which is no longer true.
- Fix: one `continue` on a trimmed line starting `<!--`, plus the marker case in `TestItemsFromLinesMatchesTheSharedFixture`.

### BE-10 · The two Lambda mains duplicate their bootstrap and neither is tested — Low, S, owner decision no

- `cmd/api/main.go:45-200`, `cmd/worker/main.go:82-250`: two copies of the AWS-config load (`AWS_REGION` branch), `logLevel`, `mustEnv`, `envInt64`, the VAPID SSM read, `NewDynamoStore`/`NewS3Objects`/`NewDynamoCounter`/`usage.NewDynamo`. Coverage: `cmd/api` none, `cmd/worker` 14.4 %. Evidence: traced + measured. Already drifting: worker has `envOr` and fatals on an unpriced model; api has neither.
- Fix: `internal/boot` with `LoadAWS`, env helpers, `NewStores(cfg)`; the mains stay as composition roots.

### BE-11 · Two silent `_ = s.store.DeleteCapture(...)` compensations — Low, S, owner decision no

- `backend/internal/service/capture.go:398, :434`. Evidence: traced. If the object PUT fails and the compensating row delete also fails, nothing is logged; the row sits at `uploaded` until the sweep, then R7-11 refuses its retry. The R7-13 standard (Warn with `capture_id`, as `releaseAppendClaim`) was applied to one site in r7 and not to these.
- Fix: log at Warn.

### BE-12 · Fixed sentences and sanitisers duplicated — Low, S, owner decision no

- "daily provider spend cap reached" at `pipeline/status.go:249` and `clean_note.go:37` (two literals; every other verdict is a named const). Title sanitiser three times with bounds 120/200/200 (`provider/openai_router.go:195`, `service/notes.go:44`, `handler/body.go:35,108`); `route.go:189` runs two of them on one title. The frontend holds none of the Go sentences (checked `src/`; only `api/__fixtures__`), so the user-facing text is single-sourced — good.
- Fix: one const; one sanitiser with one bound.

### BE-13 · ARCH-10 status: chintanctl decoders are still parallel, now bounded and not drifting — Low, S, owner decision no

- `cmd/chintanctl/enumerate.go:244-312` `noteFromItem`/`captureFromItem` decode the `data` blob first, then a 21-key legacy fallback (12 note + 9 capture) against the repository's 70 `read*` keys; encoders are shared (`promote.go:41,48` → `repository.NoteItemAttributes`/`CaptureItemAttributes`). r7/r8 attributes (`excerpt`, `created_note`) ride in the blob (`model/types.go:557,564`; `dynamo_captures.go:19,55`) and the backup is raw-item (`backup.go:171`), so `git diff 69d1f2b..HEAD -- backend/cmd/chintanctl` is empty and nothing was lost. Backup/restore are tested only against fakes (`backup_restore_test.go`: round-trip, hash mismatch, edited manifest, no header, dry-run); no CI job runs chintanctl; no restore rehearsal is documented (`docs/ops` has one file).
- Fix: delete the legacy fallback once a reconcile shows no un-promoted rows; document one restore rehearsal.

## Checked and clean

- **ARCH-11 (mislabelled 404s): not present at head.** Every arm of `handler/errors.go fail` uses `errors.Is` on a sentinel; `ErrNotFound` producers are GetItem misses, `attribute_exists` deletes, S3 `NoSuchKey` only (`s3.go:104,133`; AccessDenied is 500), stamp re-reads finding no row, foreign-tenant scoping (documented `errors.go:24-27`). The only arguable sites are `service/notes.go:500` (an invalid note id in the path → 404, arguably 400) and `export.go:182,338` (invalid export id → 404). No internal fault maps to 404.
- **Concurrency**: `PutNote` conditioned on version AND stamp (`dynamo_notes.go:489`); `StampNoteAppend`/`StampCleanRequest` on `attribute_exists` AND version; `ClearNoteAppend` on `appending_capture = :capture`; `ClaimCaptureAppend` versioned with the claimable condition (own token / empty / past `AppendClaimLease` 20 min); `CompleteCaptureAppend` on `append_token = :token AND version` (#163 holds; two workers cannot both stamp; a dead holder's claim is taken over after the lease with `paragraphInNote` as the witness). `CreateNoteOnce`: consistent `GetNote` → version-0 put → re-read on conflict; `routedNoteID` is the capture id, so stable across retries. R7-16a memo: caches only the three capture keys, remembers after the S3 put succeeds, falls through on a miss; `seenNote` is used only by `destination`, never by the stamp or the refresh; `deferPersist` is called only after the artefact put; every resume path (transcribed→route, routed→clean, cleaned→append, needs_target, regenerate) reads S3 on a cold memo. `CleanNote` R8-F8c: `versionAtRead` before the body read, stale = ETag moved OR version moved; no path stores an older body's view as current after an append. Auto-clean refuses checklists before `RecordCleanRequest`. `GetNoteDetail`'s goroutine: one channel, joined, no leak. Idempotency: every creating/re-pointing POST/PATCH/PUT is wrapped (`routes.go:25-103`); unwrapped mutations are DELETEs, push subscriptions (deduped by endpoint) and devices (capped).
- **Checklist stack**: 26 invariants, 24 pinned by named tests (line rule and `MaxDepth` via the shared fixture; depth clamps; merge parent/leaf/reopen/never-ticks/marker-and-blank bounds; exactly-once append and resume; `keepTick` three ways; `replaceChecklistItems` idempotent/deletion/DB6-7/three-level; no_content withdrawal; retranscribe keeps ticks; delete/move take exactly the marker's paragraph; `SplitOutput` shape/cap/invented/tick-safety/DB6-11/three levels; no auto-clean for checklists; CleanNote stale on ETag or version; checklist cleans in tasks only; Indic marks). The two unpinned are BE-1 (open-item loss) and BE-9 (marker never fed to `ItemsFromLines`). Crash points: claim → stamp → body → refresh → complete; `extractItems` keeps `CaptureCleanPrevious` before overwriting; a stale previous is idempotent in `replaceChecklistItems`; delete removes the copy. Regenerate: DB6-7 placement, shared-parent release, no_content withdrawal. `keepTick`, `withBox` CRLF, `mergeParent` block end, `parentOf`. One `ParseLine` for Go and TS via `checklist-lines.json` incl. `max_depth`.
- **Logs**: 63 distinct slog keys in non-test code; content-shaped ones are `raw` and `question` (both `obs.Redact` shapes) and `hint` (static operator text); `error` never wraps a title/body/transcript/reply (`router returned unknown action %q` is an enum). `logRoutingDecision` is counts only.
- **Swallowed errors**: 14 sites judged; 12 deliberate and logged (`markAudioProcessedIfSafe`, `releaseAppendClaim`, `cleanNoteAfter`, hints, push, worker discard, oversized orphan…), response-writer `_ =` standard; the two silent ones are BE-11.
- **Dead code**: `deadcode -test ./...` empty; `staticcheck` empty; `go vet` empty. Without `-test`: 5 `WithClock`/`SetMetricOutput` seams, `Pipeline.Run` (documented test seam, `pipeline.go:273`), and the three fake packages. `pipeline.Config` 21 fields all read; every env var the template sets is read and vice versa; no purge/batch leftovers (`internal/purge` is the live sweep); 182 lowercase test helpers, 0 orphaned.
- **Tests**: `go test -race -count=3 -p 2 ./...` — 29 packages ok, no flake, 31 s. Coverage at head (no 30 Sept per-package table exists in the repo to diff against; `round-6-proposals.md` P3 lists file sizes only): pipeline 88.5, service 85.7, handler 82.1, repository 79.9, provider 79.9, cleanup 99.6, routing 100, ask 97.7, llm 87.5, match 86.7, obs 86.1, breaker 94.1, usage 93.8, upload 81.6, auth 90.2, middleware 92.1, purge 92.9, push 90.9, storagesnap 89.5, awscost 93.7, ssmparam 91.7, meter 100, chintanctl 60.3, httperr 60.5, keys 42.0, model 39.4, memory 32.4, dynamofake 18.1, worker 14.4, api none. Test counts: service 209, pipeline 192, handler 111, repository 94, chintanctl 44, provider 39, cleanup 31, routing 21. Tests that only test fakes: `dynamofake_test.go` (1) and `memory/*_test.go` (4) pin the fakes' DynamoDB/S3 condition semantics, which the repository and pipeline suites then rely on — appropriate, not padding. Live eval needs the owner's key (`TestLiveEval`, documented); replay harness skips without recordings (known).
- **Prompt text**: the reverted R7-10a `[checklist]` marker and the R6-RT-8 "never a whole sentence" sentence are absent from `routing/prompt.go` and `prompts.md` (grep); the Titles rule is the older "one to five words" (`prompt.go:78`).
- **chintanctl encoders**, backup raw-item path, r7/r8 attribute survival — see BE-13.

## What is good (keep)

1. Every row and body write is conditional (version, stamp, ETag) and every loser re-reads; the append's exactly-once protocol is self-describing in code and now pinned (R7-20), and R7-16a's memo did not weaken it — the stamp and the refresh still read fresh.
2. `vet`, `staticcheck` and `deadcode -test` are all empty on 33 k lines; no unused config or env var; no orphan test helper; race ×3 clean in 31 s.
3. `decide()` is genuinely pure and the record/replay key (model + both prompts) is the right design — once recorded, a prompt edit without a re-record fails in CI.
4. One `ParseLine` shared by Go and TS through a fixture that also pins `MaxDepth`; the R7-19 drift class cannot recur silently. Backup is raw-item and blob-first, so two rounds of schema growth cost chintanctl zero edits.
5. The source-reading invariant tests (`TestHandlerDispatchesEveryTaskConstant`, `TestEveryAlarmedMetricIsRolledUp`) are the right shape for this codebase; BE-3's reverse check and BE-6's length ceiling are each a 20-line copy of them. Log hygiene holds under grep.
