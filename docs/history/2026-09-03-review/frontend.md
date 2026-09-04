# Chintan frontend review (2026-09-03)

Scope: every non-test file under `frontend/src/`, `sw.ts`, `vite.config.ts`, `manifest.config.ts`, `index.html`, `package.json`, `eslint.config.js`, `playwright.config.ts`, all `e2e/*.spec.ts`, test titles for all 37 unit-test files plus a close read of the recorder/uploader/CaptureScreen/ProgressCard/contract tests. Build, `bun pm ls`, `bun audit` and a sourcemap composition run on the linux box. Backend consulted only where a frontend claim depends on it (`capture_status.go`, `pipeline.go`, `scripts/ci-build-site.sh`).

Size: 12,333 LOC non-test TS/TSX, 7,576 LOC unit tests + fixtures + test utils, 3,007 LOC e2e, 2,469 LOC CSS. 401 `it(`/`test(` unit cases, 68 e2e cases (several are matrices). Bundle: one chunk, 445 KB raw / 138 KB gz JS; 38 KB CSS; SW 18.5 KB.

## Summary

The code is unusually well-reasoned line by line: the capture state machine is pure and total, the token lifecycle is single-flight and generation-guarded, PKCE is done correctly, idempotency keys are minted once and held across retries, and the comments are honest about what v1 got wrong. The unit tests test behaviour through fakes at real boundaries (fetch, MediaRecorder, IndexedDB via fake-indexeddb), not implementation. The contract fixtures generated from the Go router are the best idea in the repo.

The problems are at the seams the tests do not cross, and they cluster around the one promise the product makes — a recording is never lost:

1. **The durability story has a hole exactly where it matters.** Chunks stream to IndexedDB from the first `ondataavailable`, but the `captures` index record that makes them findable is written only when the machine reaches `review` (`store.ts:81`). A tab killed, reloaded or evicted *while recording* — the phone-in-a-car case the spec names — leaves audio on disk that nothing can list, offer back, upload or even prune. `ResumePrompt` reads `captures`, not `captureChunks`. This is Critical.
2. **Three High correctness bugs in the capture/edit path:** a disk-write failure moves the machine to `review` but never stops the recorder (mic stays open and, after the screen unmounts, invisible); the note autosave has no in-flight guard so two overlapping PATCHes with the same `version` produce a self-inflicted 409 "changed elsewhere" dialog on slow networks; and the offline-queue flush can run twice concurrently (reconnect refetch + Background Sync message → `refetchQueries` with `cancelRefetch`), which can mark a successfully-applied edit as permanently dead.
3. **iOS is the target platform and the code is written for Chromium.** No `terminated` handler on the cached IDB connection (Safari drops it after backgrounding), an `AudioContext` created per recording and never closed (WebKit caps concurrent contexts), the PKCE pending state in `sessionStorage` (lost when iOS kills a standalone PWA during the Cognito round-trip), no `navigator.storage.persist()`, and zero WebKit test coverage. The wake lock, `mute`/`ended` handling and `visibilitychange` flush are real and correct; the background-suspension reality is not surfaced in the UI while it is happening.
4. **Security is adequate for one user, with one structural caveat.** Tokens including the 7-day Cognito refresh token live in `localStorage`; there is no CSP (GitHub Pages cannot set headers and there is no `<meta http-equiv>`). All rendering is via React text nodes (no `innerHTML` anywhere), OAuth `state` is checked, `redirect_uri`/`logout_uri` are fixed — no open redirect. The SW never caches API responses. The biometric "vault" returns a full token set that then sits in `localStorage` like any other, so it protects nothing after the first unlock.
5. **Over-engineering for one user is substantial but not egregious** — roughly 1,800–2,300 LOC (15–19% of non-test source) is dead, duplicated, or machinery that exists to satisfy a spec sentence rather than a user: Background Sync that cannot do any work, five queued-mutation kinds that nothing enqueues, a sheet reducer used only by its tests, dead API wrappers, a three-way theme system, twin Notes/Archive screens, bulk-select, the design-token linter, and a WebAuthn flow whose payoff is skipping one redirect per week.
6. **Performance is fine for the use case.** 138 KB gz is acceptable; react-router 7 is 25% of the bundle for seven flat routes and react-dom is 37% — neither is a problem at this scale. The real cost is the progress-card poll: four list requests every 4 s (including `status=all`) while a capture is in flight, plus four more on every window focus.

Ranking below is honest: one Critical, six High. Everything else is Medium/Low and much of it is optional.

## Critical

### C1. A recording in progress is unrecoverable after reload/kill — the index record is written too late

- **Where:** `src/features/capture/store.ts:81-96` (record written on `review` transition only); `src/features/capture/recorder.ts:192-203` (chunks persisted from first `ondataavailable`); `src/features/capture/buffer.ts:92-97` (`unconfirmedCaptures` reads `captures` only); `src/features/capture/ResumePrompt.tsx:31-38`.
- **What is wrong:** `appendChunk` writes every 3 s chunk to `captureChunks` keyed `${localId}:${index}`, but the `captures` row that `unconfirmedCaptures()` lists is created only when the machine reaches `review`, i.e. after Stop/cap/track-ended completes. If the document dies while `recording`/`paused`/`stopping` — reload, iOS jettisoning a backgrounded PWA, a crash, low memory during a 20-minute dictation — the chunks are on disk and nothing can find them. They are also never pruned (no orphan sweep exists; `grep captureChunks` shows only buffer.ts and db.ts), so the store grows silently.
- **Why it matters:** This is the exact scenario the buffer's own doc comment (`buffer.ts:5-8`) says it exists for, and spec §5.3/§10.3. The e2e "stranded recording is offered back after a reload" (`offline.spec.ts:72`) only covers the post-Stop case, so the suite passes while the promise is false. The store comment at `store.ts:71-79` describes fixing this class of bug for the "offline before create" case and stops one state too late.
- **Fix:** Write the `captures` row on `streamReady` (`uploadedAt: null`, `bytes: 0`, `durationMs: 0`, `contentType` from the encoder) and update `bytes/durationMs/peaks` on `review`. In `unconfirmedCaptures()`, also adopt orphan chunk groups (`getAllKeysFromIndex('captureChunks','byLocalId')` grouped by `localId`) that have no row, synthesising a row from chunk bytes. Verify on Safari that a partial `audio/mp4` (no final chunk) is decodable by the backend — WebKit's MediaRecorder needs to emit fragmented MP4 for that to hold; on Chromium partial WebM decodes fine. If Safari's partial MP4 is not decodable, the fix must also periodically `requestData()` plus finalise-and-restart segments, which is a larger change.

## High

### H1. A failed chunk write leaves the microphone open with no UI saying so

- **Where:** `recorder.ts:200-202` emits `recorderError` on `persistChunk` rejection but does not call `stop()`; `machine.ts:293-302` turns `recorderError` with `bytes > 0` into `review`; `CaptureScreen.tsx:79-86` resets the store on unmount after `uploaded`; `RecordingIndicator.tsx:32` hides when `!isCaptureBusy`.
- **What is wrong:** On the persist-failure path the machine says "Ready to send" while the `MediaRecorder`, tick timer, analyser timer, stream tracks and wake lock are all still live. If the user taps Send, the upload proceeds from whatever landed on disk; on `uploaded` the screen navigates home and `reset()` puts the model at `idle`, so `RecordingIndicator` disappears — but the controller's recorder is still running (nothing ever called `teardown()`). The OS mic indicator stays on until the app is killed; every subsequent chunk write fails and re-emits `recorderError`, which in `review` state produces a fresh model object each 3 s (needless re-renders). The `onerror` path (`recorder.ts:205-210`) does call `stop()`; this path was missed. `recorder.test.ts` "surfaces a failed disk write" asserts the event, not that the mic stopped, so the test enshrines the bug.
- **Why it matters:** On iOS the trigger is realistic (see I1: a terminated IDB connection makes every `put` reject). Battery, privacy indicator, and a wake lock held indefinitely.
- **Fix:** In the persist catch, `void this.stop()` after emitting (mirroring `onerror`). Also make `store.reset()`/`discard()` call `controller.cancel()` defensively, and have `RecordingIndicator` derive "mic open" from the controller (`controller.current() !== null`) rather than the model.

### H2. Autosave has no in-flight guard: overlapping PATCHes fabricate a conflict

- **Where:** `src/features/notes/useNoteEditor.ts:82-96` (`save` proceeds when `state` is `dirty` or `error`); `src/features/notes/autosave.ts:111-122` (`applyEdit` sets `dirty` even while `saving`); `useNoteEditor.ts:110-114` (version bumped as `current.version + 1` after success).
- **What is wrong:** Type → 1.2 s debounce → save #1 starts with `version N` (state `saving`). Keep typing → `applyEdit` → `dirty` → 1.2 s → `save()` sees `dirty`, starts save #2 also with `version N` (the reducer has not seen `saveSuccess` yet). Save #1 returns 200 (server now N+1); save #2 returns 409 → `conflict` → the user is told "A voice capture or another device saved this note while you were editing" about their own keystrokes, and must choose "Use the newer version" (loses the last burst) or "Keep my edits". The `visibilitychange` flush (`:230-238`) and unmount flush (`:254-256`) can produce the same overlap. On cellular a 3 s PATCH is normal.
- **Why it matters:** Core editing flow; the conflict dialog is the one UI that should never be wrong. `autosave.test.ts` "stays dirty when the user typed while the request was in flight" tests the reducer only; nothing tests two concurrent `save()` calls.
- **Fix:** Serialise: keep an `inFlight: Promise | null`; if a save is running, set a `pendingResave` flag and re-run `save()` when it settles, using the *response's* `version` (the PATCH returns `NoteWire`) rather than `current.version + 1`.

### H3. The offline-queue flush can run twice concurrently and mark a delivered edit as dead

- **Where:** `src/offline/useOfflineQueue.ts:138-140` (`flushNow` → `queryClient.refetchQueries`, default `cancelRefetch: true`); `:151-157` (`listenForFlushRequests` calls `flushNow` on the SW's `FLUSH_QUEUE`); `:129` (`refetchOnReconnect: true`); `src/offline/queue.ts:183-212` (`flush` has no mutex; `markDead` at `:149-157` re-`put`s the entry); `:226-230` (`isTerminal` treats any 4xx except 401 — including 409 and 429 — as dead).
- **What is wrong:** Connectivity returns → TanStack's `refetchOnReconnect` starts flush A. The Background Sync `sync` event fires on the same reconnect → SW posts `FLUSH_QUEUE` → `flushNow()` → `refetchQueries` cancels A's *promise* (the queryFn keeps running; nothing checks a signal) and starts flush B. Both read the same `pending()` list and both `run()` the same `updateNote` with the same idempotency key. Depending on the server's in-progress idempotency handling, B gets a 409/4xx → `markDead()` → after A's `remove()` the dead row is re-inserted with `attempts = 8` → `reconcileQueued` (`autosave.ts:241-243`) shows "That edit did not save" for an edit the server has. Separately, a non-spend-cap 429 (rate limit) is also "terminal" here although `ApiError.isRetryable` says otherwise, and a spend-capped `retryCapture` would be killed on first attempt instead of waiting for tomorrow.
- **Why it matters:** A retry system whose failure mode is a permanent false "did not save" is worse than none. Trigger is Chromium/Android only (Background Sync), i.e. not the user's primary device, hence High not Critical.
- **Fix:** Simplest: delete Background Sync (see O1) — the SW cannot do the work itself, so it only ever duplicates the foreground path. Otherwise add a module-level `flushing` promise in `queue.flush` that concurrent callers await, and narrow `isTerminal` to `400, 403, 404, 409, 413, 422`.

### H4. `AudioContext` is created per recording and never closed

- **Where:** `recorder.ts:53-59` (`createAudioContext`), `:176` (one per session), `:349-364` (`teardown` closes nothing; `session` is retained for `envelope()`).
- **What is wrong:** Each `start()` allocates a new `AudioContext` with a `MediaStreamSource` + `AnalyserNode`. Nothing calls `close()`. After N recordings there are N live contexts.
- **Why it matters:** WebKit limits concurrent `AudioContext`s (historically 4 on iOS); `new AudioContext()` then throws, is caught, and returns `null` → no live waveform and no start/stop tones, silently, on the platform the product targets — the eyes-off feedback that §5.3 calls a requirement degrades after the fourth recording in a session. On all platforms it is a leak of a real-time audio thread.
- **Fix:** `void this.session?.audioContext?.close()` in `teardown()` after `stopFeedback` has played (or play the stop tone before stopping, then close on `onstop`), and null the reference. Keep the peaks array; it does not need the context.

### H5. IndexedDB connection handle never recovers from termination (iOS)

- **Where:** `src/offline/db.ts:117-143` (`dbPromise` cached forever; `openDB` called without `terminated`/`blocking`/`blocked` callbacks).
- **What is wrong:** Safari (and occasionally Chrome under pressure) abnormally terminates IDB connections when a page is backgrounded. `idb` exposes `terminated()` for exactly this; it is not wired, so the cached promise resolves to a dead connection and every later `appendChunk`, `saveCaptureRecord`, `enqueueReplacing`, `cacheNoteList` rejects until the page is reloaded. Every caller swallows or downgrades the error (`store.ts:93`, `queries.ts:80`, `useNoteEditor.ts:145`), so the app keeps running with its offline story silently disabled — and `appendChunk` failures feed H1.
- **Fix:** Pass `terminated: () => { dbPromise = null; }` (and `blocking: () => db.close()` for future upgrades) to `openDB`. One line each.

### H6. Unsent audio in `review`/`failed` becomes invisible after Back

- **Where:** `machine.ts:160-167` (`isCaptureBusy` excludes `review` and `failed`); `RecordingIndicator.tsx:32`; `ResumePrompt.tsx:31-38` (`staleTime: Infinity`, comment "Nothing else creates entries behind the app's back" — false: `store.ts:81` and `uploader.ts:229` do); `CaptureScreen.tsx:62-72` (only the capture route re-shows it).
- **What is wrong:** Stop a recording (or have an upload fail), then press system Back — a pocket or a steering wheel does this. The sheet is not locked (`review` is not busy), Home shows the plain record hero, `RecordingIndicator` is hidden, and `ResumePrompt` was fetched once at boot so it does not list the new `captures` row. The audio is safe on disk but nothing on screen says a recording is waiting; only tapping Record (which re-enters the old screen via `hasBufferedAudio`) or a full reload reveals it.
- **Fix:** Invalidate `UNSENT_CAPTURES_KEY` from the store's `review`/`failed` transitions and from `uploadCapture` outcomes (or give the query `staleTime: 0` + `refetchOnWindowFocus`); and either include `review`/`failed`-with-bytes in `isCaptureBusy` for the indicator only, or render `ResumePrompt` from the live store as well as from disk.

## Medium

### M1. Refresh token in `localStorage`, no CSP
- **Where:** `src/api/tokens.ts:35,105-137`; `src/api/session.ts:205-212`; `index.html` (no `<meta http-equiv="Content-Security-Policy">`).
- **What:** `chintan.tokens.v2` holds id, access and the 7-day refresh token. Any XSS is a week of account access and it survives the tab. GitHub Pages cannot send headers, so a meta CSP is the only mitigation and there is none. Mitigating facts: no `innerHTML`/`dangerouslySetInnerHTML` anywhere, no third-party scripts, all transcript/title/body rendering is React text. Risk is low for one user; the structure is still the weakest point.
- **Fix:** Add a meta CSP (`default-src 'self'; connect-src 'self' <api> <cognito> https://*.s3.*.amazonaws.com; media-src blob: https://*.s3.*; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'` — note the two inline `<script>`s in `index.html` need hashes or moving into the bundle). Consider keeping only the refresh token in IndexedDB and the id token in memory; it does not fix XSS but shrinks the window to the current session.

### M2. A Cognito 5xx/429 on refresh signs the user out
- **Where:** `session.ts:134-143` (any non-offline error → `clear()`); `session.ts:191-198` (`CognitoRefresher` maps every non-OK to an http error; only 400 is `invalid_grant`); no retry, no timeout on the refresh fetch.
- **What:** A transient 5xx or throttling from `/oauth2/token` deletes the token set and forces a hosted-UI login. Only `invalid_grant`/`invalid_request` (400) genuinely mean the refresh token is dead.
- **Fix:** `clear()` only on 400/401; treat 5xx/429 like offline (throw without clearing) and let the next request retry.

### M3. `alreadyLanded` conflates "left `uploaded`" with "audio arrived"
- **Where:** `src/features/capture/uploader.ts:86-93,154-158`; backend `pipeline.go:211-235` (`RejectOversizedCapture` deletes the object and marks the row `failed`).
- **What:** On resume, any status other than `uploaded` causes `confirm()` → local audio pruned. The backend's oversize path deletes the server object and flips to `failed`; the client then deletes the only remaining copy. Today's client cap (32 MB) makes oversize unlikely, and there is no stale-`uploaded` sweeper in the backend (verified by grep), so this is latent — but it will become data loss the day anyone adds "mark captures with no object after N hours as failed", which `chintanctl reconcile`'s `stuck_capture` finding invites.
- **Fix:** Confirm only on statuses that imply bytes were processed (`transcribing`…`appended`, `needs_target`, `no_content`, `spend_capped`), or better, have the server expose `object_received`/`has_audio` and check that.

### M4. Progress-card poll is four list requests every 4 s, plus four on every focus
- **Where:** `src/api/queries.ts:348-382` (`usePendingCaptures` fires `pending`, `failed`, `needs_target`, `all` in parallel; `refetchInterval` 4 s while any non-terminal); `ApiProvider.tsx:27` (`refetchOnWindowFocus: true` globally).
- **What:** A two-minute pipeline costs ~120 API-gateway/Lambda invocations and ~30 `status=all` scans (default limit 50, newest-first, "every capture the tenant has ever made"). `ProgressCard.test.tsx` "polls all three filters, not pending alone" enshrines this. For one user the dollars are trivial; the battery/cellular cost while driving is not.
- **Fix:** One `listCaptures({ status: 'all', limit: 20 })` per poll and filter client-side (the card already filters `recentlyAppended`), or add a server filter `status=attention` that returns pending ∪ failed ∪ needs_target ∪ recently appended. Back the interval off (the `capturePollInterval` helper at `:304-309` already exists and is used only by the dead `useCapture`).

### M5. PKCE pending state in `sessionStorage` is fragile on iOS standalone
- **Where:** `src/features/auth/pending.ts:8-11,30-38`.
- **What:** The verifier and `state` live in `sessionStorage` across the Cognito round-trip. On an iOS home-screen PWA, switching apps to read an MFA SMS often gets the process killed; `sessionStorage` does not survive that, so the redirect back finds no pending flow → "That sign-in could not be completed. Please try again." (`useAuth.ts:164-166`). The 10-minute TTL argument for `sessionStorage` (`pending.ts:23-28`) is already implemented, so `localStorage` would be equally safe.
- **Fix:** Store pending auth in `localStorage` (or IndexedDB) with the existing TTL and single-use `takePending`.

### M6. No `navigator.storage.persist()`; storage is best-effort
- **Where:** nowhere (grep confirms).
- **What:** Chrome/Android may evict best-effort origins under pressure — the buffered audio and queued edits with it. Safari exempts installed home-screen apps from its 7-day script-storage cap but not the same app opened in a Safari tab. One call at first record.
- **Fix:** `await navigator.storage?.persist?.()` on first `start()`; surface `estimate()` when free space is low relative to `MAX_BYTES`.

### M7. Network-first navigation with a 4 s timeout on deep links, including the "Record" shortcut
- **Where:** `src/sw.ts:57,109-133,150-152`; `manifest.config.ts:79-87` (`shortcuts[0].url = /capture`).
- **What:** `precacheAndRoute` (registered first, `directoryIndex: 'index.html'`) answers the `start_url` cache-first, so ordinary launches are instant — contradicting the file's "network-first shell" rationale but harmless. Every *other* navigation — the manifest's "Record a thought" shortcut (`/capture`), any bookmark, any deep link — goes through `networkFirst`, which on GitHub Pages returns a 404 (non-OK → falls back to the shell) after a round-trip, or waits up to 4 s on a hanging cellular link before serving the shell. The reason network-first existed (v1's `config.js`) is gone: config is compiled in and the update prompt handles new builds.
- **Fix:** Serve the precached shell for all same-origin navigations (`createHandlerBoundToURL` + `NavigationRoute`) and drop `networkFirst` and the runtime cache; ~40 LOC less.

### M8. `useNotes`/`useNote` both feed the IDB corpus and the query cache: three copies of every note
- **Where:** `src/api/queries.ts:68-119` (`remember` side-effect writes IDB on every fetch, then invalidates `['notes','offline']`); `src/offline/notesCache.ts`, `src/offline/useNotesCache.ts`; `NotesScreen.tsx:98-108`, `ArchiveScreen.tsx:67-70`, `SearchScreen.tsx:38-58`, `NoteDetailScreen.tsx:29-40` each hand-merge server data with the device copy.
- **What:** Server state lives in TanStack; the same rows live in the `notes` IDB store; four screens each re-implement "prefer server, fall back to device, label it" with slightly different conditions (`NotesScreen` labels only when `!online || paused || isError`; `NoteDetailScreen` labels whenever `!served`). `OfflineBanner.useHasCachedNotes` (`OfflineBanner.tsx:47-90`) then walks the QueryCache on every cache event to guess whether anything is cached, because "there is no query persister".
- **Fix:** Use `@tanstack/query-persist-client` + `idb-keyval`/`idb` persister (~20 LOC) and delete `notesCache.ts`, `useNotesCache.ts`, the `notes` store and the four merges (~300 LOC + 100 test LOC). Search's local ranking can read the persisted `notesCorpus` query. Trade-off: one more dependency.

### M9. Version derived as `current.version + 1` instead of from the PATCH response
- **Where:** `useNoteEditor.ts:108-114`.
- **What:** The response carries the stored `NoteWire`; ignoring it makes the next save 409 if the server ever bumps by more than one (e.g. a second write that re-derives the snippet). Also compounds H2.

### M10. Peaks envelope assumes uniform sampling
- **Where:** `recorder.ts:333-338` (analyser sampled by `setInterval` 50 ms), `peaks.ts:65-89` (`downsample` divides samples evenly across `duration_ms`).
- **What:** Backgrounded tabs throttle intervals to ≥1 s; iOS suspends them. Any stretch of a recording spent backgrounded contributes ~20× fewer samples, so the stored waveform's time axis is skewed relative to the audio and the scrubber's playhead lands on the wrong bar. Cosmetic, but it undermines the "scrub by waveform" feature the spec calls the visual centre.
- **Fix:** Timestamp each sample (`now - startedAt`) and bucket by time in `downsample`.

### M11. Bundle is a single chunk; react-router is a quarter of it
- **Evidence (remote build, sourcemap `sourcesContent` proxy):** react-dom 37%, app 28%, react-router 25% (372 KB of source for 7 flat routes), @tanstack/query-core 6%, react 1%, idb 0.8%, zustand 0.1%. 445 KB raw / 138 KB gz; no `manualChunks`, no `React.lazy`.
- **What:** Acceptable for a PWA that is precached, but every deploy re-downloads the whole thing. `NoteDetailScreen` + player + transcript (~1,000 LOC) and Settings/Biometric could be lazy routes; react-router's data-router features (loaders, actions, `RouterProvider`) are unused — `createBrowserRouter` is used only for `basename` and `useNavigate/useSearchParams`.
- **Fix:** Not urgent. If touched: `React.lazy` the three secondary screens; or replace react-router with `wouter` (~2 KB) since `useBackGuard`/`sheetForPath` already treat the URL as the state.

### M12. `DownloadButton` revokes the blob URL synchronously after `click()`
- **Where:** `src/components/DownloadButton.tsx:57-69`.
- **What:** Safari/iOS can cancel the download when the object URL is revoked in the same tick as the synthetic click. Defer revocation (`setTimeout(..., 0)` or on `focus`).

### M13. Hard-coded theme literals in `manifest.config.ts` and Cognito CSS drift from tokens
- **Where:** `manifest.config.ts:15-27,58-59`; README §"hosted UI".
- **What:** The token lint forces the manifest colours out of `src/`; `theme_color` cannot follow the user's theme anyway. The README documents that `config/instances/*.yaml`'s `pwa:` block is decoration. Pick one source or delete the YAML block.

## Low

- **L1.** `pending.returnTo` is written (`useAuth.ts:211`) and never read; sign-in always lands on the base URL. Either navigate to it after `session.set()` (validate it is a same-origin path) or delete the field. `pending.ts:15-21`.
- **L2.** `index.html:28-30` comment says the redirect decoder undoes "public/404.html"; the file is generated by `scripts/ci-build-site.sh:186` at the Pages root, not in `public/`. Comment drift.
- **L3.** Dead API wrappers: `matchNotes`, `startExport`, `getExport`, `health`, `ready` (`endpoints.ts:49-55,135-140,269-275`), `useTags`, `useCapture`, `capturePollInterval` (`queries.ts:261-264,304-309,384-401`), `bufferedBytes` (`buffer.ts:66-69`). ~90 LOC.
- **L4.** `sheetReducer`, `INITIAL_SHEET` (as export), `isSheetLocked`, `pathForSheet`, `SheetEvent` are used only by `sheet.test.ts` (13 tests). Production uses `sheetForPath` and `pathForTab`. ~70 LOC + 60 test LOC.
- **L5.** `Waveform.tsx:61` calls `getComputedStyle` every animation frame (style recalc per frame). Read the colour once per effect and on theme change.
- **L6.** `UpdatePrompt` never calls `registration.update()`; a standalone PWA kept alive for days only checks on navigation. Add an `update()` on `visibilitychange` → visible.
- **L7.** `sw.ts:114-124` caches every OK navigation response in the runtime cache keyed by full URL (`/?/notes/<id>`); unbounded but tiny. Goes away with M7.
- **L8.** `chooseEncoder` fallback (`audio.ts:56`) reports `audio/webm` for a browser that supports none of the listed types, which may be `audio/mp4` in reality; the server then mislabels the object. Prefer refusing (`unsupported`) or sniffing the first chunk's `Blob.type`.
- **L9.** `client.ts:219` trusts `problem.status` from the body over the real HTTP status.
- **L10.** `ProgressCard.tsx:93` module-level `dismissed` set persists across sign-out; harmless for one user.
- **L11.** `SignOutSetting` and `BiometricSetting` share the `['webauthn','status']` key with different `queryFn`s (one records the enrolment hint, one does not) — whichever mounts first wins.
- **L12.** `useBackGuard` seeds history with two async `navigate` calls on first mount (`useBackGuard.ts:34-37`); under React 19 StrictMode dev this runs once thanks to the ref, fine, but it also runs on the OAuth callback landing (`/?code=…`), pushing a second entry before `cleanCallbackFromUrl` `replaceState`s — Back once returns to a URL with `code=` stripped only from the latest entry. Cosmetic.
- **L13.** `ResumePrompt.tsx:57` casts `record.contentType as 'audio/webm'`; type lie, value correct. Make `StoredCapture.contentType: CaptureContentType`.
- **L14.** `bun audit`: 4 high in `fast-uri` via `eslint → ajv` and `vite-plugin-pwa → workbox-build → ajv`. Build-time only; nothing ships. `bun update` when upstream pins move.

## iOS/PWA reality check

What the code gets right for iOS: `viewport-fit=cover` and safe-area/dvh usage (7 sites in `shell.css`), `apple-mobile-web-app-*` metas and absolute icon links, `mute`/`unmute`/`ended` track handlers, Screen Wake Lock with re-acquire on `visibilitychange` (`wakeLock.ts:38-43`; supported since iOS 16.4), `Blob.arrayBuffer` and Blob-in-IDB fallbacks (`buffer.ts:20-33`, `db.ts:66-71`), `visibilitychange` autosave flush, `audio/mp4` in the encoder list, and a `ConfirmDialog` that does not depend on `<dialog>`.

Where it pretends, or is silent:

1. **Background recording.** When the screen locks or the app is backgrounded, iOS mutes the capture track (sometimes ends it) and suspends timers/JS. The machine handles `trackMuted` as a flag only (`machine.ts:322-326`) and the UI shows "The recording was interrupted" only *after* stopping (`CaptureScreen.tsx:142-146`). While it is happening the timer keeps counting (wall-clock, so correct) and the waveform goes flat — the user in a pocket hears nothing and sees nothing. The wake lock helps in a mount; it does nothing against the lock button. State plainly, while recording, "Microphone paused by the system" when `interrupted && state === 'recording'`, and consider auto-pausing on `mute` so the elapsed counter matches captured audio.
2. **`AudioContext` cap** (H4). Fourth-plus recording in a session silently loses waveform and tones on WebKit.
3. **IDB termination** (H5) and **no `persist()`** (M6).
4. **Partial `audio/mp4` decodability** (C1). Whether a recording without its final chunk is decodable depends on WebKit emitting fragmented MP4 with `timeslice`. Unverified; the durability claim rests on it for Safari.
5. **`audioBitsPerSecond: 24_000`** (`recorder.ts:51`) and `sampleRate: 16000` (`audio.ts:19-25`) are hints Safari largely ignores (AAC ~64 kbps+). The "3.6 MB per 20 minutes" comment (`machine.ts:63`) is Chromium-only; Safari is closer to 10 MB. Still under caps.
6. **Standalone-mode OAuth.** Modern iOS keeps the cross-origin Cognito navigation inside the standalone web view and returns in-scope, so the flow works; the fragility is the `sessionStorage` pending state (M5) when the process is killed mid-flow, and `redirect_uri` being the base URL means the user always lands on Home (L1).
7. **`navigator.vibrate`** is unimplemented on iOS (acknowledged in `feedback.ts:9`) — so on iOS the only eyes-off feedback is the tone, which H4 kills after four recordings.
8. **Service worker lifetime.** iOS evicts SW registrations and caches for home-screen apps not opened for a while (~weeks). After that a cold launch at `start_url` hits Pages directly (fine), but the "Record" shortcut at `/capture` hits Pages' 404 → root `404.html` redirect trick → works, with two extra navigations. Acceptable; just not the instant launch the manifest shortcut implies.
9. **Background Sync** does not exist on WebKit; the code says so (`backgroundSync.ts:25-27`). It adds nothing on iOS and a race on Chromium (H3).
10. **Zero WebKit test coverage.** Playwright runs `chromium` only (`playwright.config.ts:42-47`); the fake-media-device flags are Chromium-only. Nothing exercises `audio/mp4`, Safari's MediaRecorder, or WebKit IDB. Adding a `webkit` project for the non-capture specs (auth, notes, offline reads, playback with a WAV) is cheap and would catch M12-class issues.

## Over-engineering / deletable

Estimates are non-test source LOC unless noted; tests that only exist for the deleted code go with it.

| Item | Where | LOC (src / tests) | Why it can go |
|---|---|---|---|
| O1. Background Sync | `offline/backgroundSync.ts`, `sw.ts:155-183`, `useOfflineQueue.ts:145-157` | ~100 / 0 | The SW has no session or API client, so it only posts a message to an open tab that is already flushing on reconnect. Causes H3. |
| O2. Generic mutation queue | `offline/payloads.ts`, `useOfflineQueue.ts:21-61`, `db.ts:27-33` | ~100 / ~40 | Six `QueuedMutationKind`s declared, one (`updateNote`) ever enqueued. Collapse to "one pending PATCH per note". |
| O3. Hand-rolled notes cache + four screen-level merges | `offline/notesCache.ts`, `offline/useNotesCache.ts`, `OfflineBanner.useHasCachedNotes`, merge logic in 4 screens | ~350 / ~100 | Replace with TanStack persist-client + idb persister (M8). |
| O4. WebAuthn biometric unlock (frontend half) | `settings/webauthn.ts`, `settings/BiometricSetting.tsx`, `auth/enrolment.ts`, unlock branches of `useAuth.ts`, revoke branch of `signOut.ts`, `SignedOutScreen` unlock UI | ~500 / ~450 (`unlock.spec.ts` 299, gate tests) | Payoff is skipping one hosted-UI redirect per 7-day refresh window; the unlocked token set lands in `localStorage` like any other so it adds no security. Plus the backend vault/KMS/SSM key machinery. Biggest single simplification available. |
| O5. Three-way theme system | `theme/*`, inline pre-paint script, `SettingsScreen` Appearance section, `settings.theme` round-trip to the server | ~230 / ~140 | `prefers-color-scheme` in `tokens.css` alone gives "follow system" with zero JS. |
| O6. Design-token linter | `scripts/check-tokens.mjs` | ~120 / 0 | One developer; the constraint pushes literals into `manifest.config.ts` and forces canvas code to `getComputedStyle` per frame (L5). |
| O7. Sheet reducer | `app/sheet.ts` reducer half | ~70 / ~60 | Only tests call it (L4). |
| O8. Dead API surface | `endpoints.ts`, `queries.ts` (L3) | ~90 / 0 | Never called. |
| O9. Twin Notes/Archive screens and rows | `screens/NotesScreen.tsx`, `screens/ArchiveScreen.tsx`, `NoteRow` vs `ArchivedRow` | ~200 / ~60 | One `NotesList state=` component with a `meta` render prop. |
| O10. Bulk select/archive/restore/purge | both screens, `queries.ts:197-249`, `ConfirmDialog.requireText` | ~200 / ~80 | For a single user's voice notes, per-note archive is enough; keep "empty archive" as one button if wanted. |
| O11. Hand-rolled `ConfirmDialog` | `components/ConfirmDialog.tsx` | ~120 saved | `<dialog showModal()>` gives trap/Escape/inert; the stated reason (jsdom) is the test environment dictating production code. Vitest can run `browser` mode or the dialog can be e2e-only. |
| O12. `store.__configure` seam, `dispatch` export | `store.ts:44-45,167-173` | ~15 | Inject deps via a factory instead. |
| O13. `contract-requests` + fixtures | `api/__fixtures__/*`, `contract*.test.ts` | keep | Genuinely valuable; the one piece of test infrastructure that pays for itself. |

Total realistically deletable: **~1,800–2,300 LOC of 12,333 non-test source (15–19%)**, plus ~900 LOC of tests that exist only for that code, plus CSS for the theme/bulk UIs. O4 alone also removes a KMS key, an SSM parameter, and three backend handlers.

Duplicated state today: TanStack query cache ↔ IDB `notes` store (same rows), TanStack `['captures','progress-card']` ↔ IDB `captures` ↔ Zustand capture model (three views of one capture), `queued-edit` query ↔ IDB `mutations` ↔ editor reducer `queued` state (the code itself calls this out at `autosave.ts:216-232` and derives rather than mirrors, which is the right instinct — it just still needs three layers to do it).

## Test quality

**Unit (401 cases, 37 files).** Mostly behaviour-level and well-targeted. The good pattern is consistent: fakes at real boundaries (`fetch` stub in `test/providers.tsx`, `FakeRecorder`/`FakeTrack` in `recorder.test.ts`, `fake-indexeddb`), assertions on observable outcomes (events emitted, requests made, text on screen). Titles read as requirements ("does not sign the user out when the refresh fails because they are offline"). The contract tests (`contract.test.ts`, 25 cases) assert against fixtures generated by the Go router — that is real integration value at unit-test cost.

Weaknesses:
- **Seams untested.** `machine.test.ts` (32) and `recorder.test.ts` (17) each test their half; `store.ts` — the glue that decides when the `captures` row is written (C1) and what happens after a persist failure (H1) — has no test. `CaptureScreen.test.tsx` (5) tests arming only.
- **Some tests enshrine bugs:** "surfaces a failed disk write rather than recording into nothing" (`recorder.test.ts`) asserts the event and not that the mic stopped; "polls all three filters, not pending alone" (`ProgressCard.test.tsx`) locks in M4.
- **Concurrency is untested everywhere it matters:** two overlapping `save()` calls (H2), two concurrent `flush()` loops (H3), `send()` while the final chunk is still being persisted.
- **Volume skew:** 26 tests for `ProgressCard`, 25 for `autosave` reducer, 13 for the sheet reducer nobody calls; 4 for `useNoteEditor`, 0 for `store.ts`, 0 for `sw.ts` (only reachable via e2e).
- Rough count: ~70% behaviour, ~20% reducer/pure-function specification (fine, cheap), ~10% testing internals (sheet reducer, `__configure`, manifest helper).

**E2E (68 cases, Chromium only, ~3,000 LOC).** The API is stubbed by `page.route` interception (`e2e/fixtures.ts:259-547`), and Cognito by a second route on a fake origin; the service worker, IndexedDB, `MediaRecorder` (fake device), canvas and the real production build are exercised. What they actually prove: the client sends the right requests with the right headers/keys, honours the offline/queue ordering, survives a reload with audio on disk, completes the PKCE round-trip through a real cross-origin redirect, and renders a11y-clean in both themes. They do *not* prove anything about the backend contract beyond what the stub implements (which is a hand-maintained second implementation of the API — the same class of drift the contract fixtures exist to prevent), and nothing about WebKit.

Worth keeping: `capture.spec`, `offline.spec`, `auth.spec`, `unlock.spec` (if O4 stays), `playback.spec` — each comment cites a real production bug it fenced. Reduce: `layout.spec` (11 viewports × 2 themes × 7 routes; pick 3 viewports), `a11y.spec` (2 themes × 6 routes; one theme per run is enough given tokens drive both). Add: a `webkit` project for the non-microphone specs. The suite rebuilds the app on every run (`playwright.config.ts:53`), so a local iteration is ~1 min before the first test; acceptable.

Verdict: the unit suite is worth its maintenance; the e2e suite is worth about 60% of its current size, and its Chromium-only shape is a false comfort for an iOS product.

## Dependencies

Runtime (6): `react`/`react-dom` 19.2, `react-router` 7.18, `@tanstack/react-query` 5.101, `zustand` 5.0, `idb` 8.0. All current, all maintained. Bundle share (sourcemap proxy): react-dom 37%, react-router 25%, query-core 6%, idb <1%, zustand <1%. `react-router` is the only one disproportionate to its use (7 flat routes, no loaders/actions); `wouter` or a 60-line `useSyncExternalStore` router would drop ~90 KB raw. `zustand` guards one 180-LOC store; harmless. Nothing duplicated.

Dev (21): `vite` 7.3 + `@vitejs/plugin-react`, `vite-plugin-pwa` 1.3 (pulls `workbox-build` → 600+ transitive packages; the SW itself uses 4 workbox modules ≈ 110 KB source → 18 KB output), `vitest` 3.2 + `jsdom` 27 + `fake-indexeddb` 6 + Testing Library, `@playwright/test` 1.62 + `@axe-core/playwright`, `typescript` 5.9, `typescript-eslint` 8, `eslint-plugin-react-hooks` 7, `@types/node` 26. All current. 607 packages in `node_modules` for a 12k-LOC app is normal for this toolchain.

`bun audit`: 4 high, all `fast-uri` (<3.1.6) via `ajv` under `eslint` and `vite-plugin-pwa/workbox-build`. Build-time only; none of it is in `dist/`. `bun update` once upstream moves.

Options: (a) drop `vite-plugin-pwa` and hand-write the 20-line precache list from `dist/.vite/manifest.json` — the SW already avoids `generateSW` and would lose only `injectManifest`'s glob; saves the largest dev-dependency tree. (b) Drop `jsdom` for `happy-dom` (faster, smaller) — marginal. (c) Nothing is unmaintained or abandoned.
