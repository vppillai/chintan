# You and About

The two screens behind the You tab: the settings (`/settings`,
`frontend/src/features/settings/SettingsScreen.tsx`) and the page about the
app (`/about`, `frontend/src/screens/AboutScreen.tsx`), plus the export the
"Your data" card drives (`backend/internal/service/export.go`,
`backend/internal/handler/export.go`, `ExportCard.tsx`). `/usage` is a
screen of its own and `usage-accounting.md` owns it; the routes and the
"‹ You" rule are `navigation.md`'s.

## You

The account first, then cards, each a title, one line on what it is for,
its controls as rows, and a footnote whose first sentence stays in view
with the rest behind a native `<details>` labelled More, reading Less once
open (`SettingsCard.tsx`, the `foot`/`more` props). The account header,
then seven cards, in the order `SettingsScreen.test.tsx` holds them to:

| Card | What it holds |
|---|---|
| Account (`AccountHeader.tsx`) | The initial in a roundel and the email from the id token, how long ago the session began, and Sign out as a text action that confirms first (`auth.md`). |
| Recording & transcription | `LanguageSelect` for `default_language` (auto-detect explained, with the one-line Malayalam advice beneath it) and `Keep recordings for`, a `<select>` over `RETENTION_TIERS = [0, 7, 30, 90, 365]`, the tiers the server stores (`retention.md`). |
| Appearance | A three-way `Segmented` control, Ink & Paper / Nocturne / System, each with a swatch; applied on the device at once (`useTheme`) and saved like the rest. |
| Passkeys (`PasskeyCard.tsx`) | The hand-off to the managed login's passkey page and its result (`auth.md`). |
| Notifications (`NotificationsCard.tsx`) | The one Web Push switch and the four sentences for when it cannot be offered (`push.md`). |
| Devices & shortcuts (`DevicesCard.tsx`) | Inbox keys: the list, the mint form and the connect recipes (curl, iPhone or Apple Watch Shortcuts, Android HTTP Shortcuts). Folded by default behind a summary that counts keys and flags one expiring soon; `#devices` (`DEVICES_ANCHOR`, the bare `devices`), which About links to, opens it (`inbox.md`). |
| Your data (`ExportCard.tsx`) | One `DownloadButton`: everything as one JSON file. |
| About & support | `Usage this month` carrying the month's figure (`formatDollars`) and opening `/usage`; `About <app>`; `Source on GitHub`; and the Version row (`VersionFootnote`). |

Every control saves itself the moment it changes: a change is a whole
`PUT /v1/settings` (`useSaveSettings`) built on the stored record, so
nothing is saved before that record has loaded, and what renders after the
PUT is what the server stored, not what the device sent — the server rounds
a retention number down to a tier, and the screen shows the rounded value.
There is no Save button and nothing is ever unsaved. The status line beside
the title says only what is happening — loading, Saving…, a Saved tick for
`SAVED_TICK_MS` (2.5 s), or a failure with Try again — and reserves no
space while it has nothing to say. The daily spend cap is never named here,
even when the instance has one (`usage-accounting.md`).

## About

A page to read, not a settings screen, written for the person who has been
handed the app and wants to know what happens to their voice: the mark, the
app's name and one-sentence description from `config` (`VITE_APP_NAME`,
`VITE_APP_DESCRIPTION`), then three sections found by their icons —
**How filing works**, the five steps a recording takes (Record, Transcribe,
Route, Clean up, Append) as an ordered list and prose on how the router
decides, cleanup, Regenerate, and the one sentence that sends the reader to
Devices & shortcuts on You; **Your data**, a definition list of where each
kind lives (sign-in in Cognito, notes in DynamoDB, recordings and
transcripts in S3) in the account of whoever runs the instance, the two
providers and what they are not asked to retain, the device's offline copy,
retention and the archive's thirty days, and the spend cap mentioned once
and never its amount; **Privacy & open source**, the MIT licence, no
analytics, links to the repository and the backlog, and the build line.
"‹ You" goes back when You is beneath it and otherwise replaces itself with
You. `AboutScreen.test.tsx` pins the three headings and each section's
promises.

## The version

CI injects `git describe --tags --always` as `VITE_VERSION`
(`config.version`; a local build shows `LOCAL_VERSION`). `describeVersion`
(`VersionFootnote.tsx`) shows a build exactly on a tag as the tag, one past
a tag as the tag and then, quieter, `+<n> (<sha>[, dirty])`, and anything
else verbatim. It is real text in a `<code>`, selectable, the first thing a
bug report needs; About adds the instance name when it is not `dev`
(`VersionFootnote.test.tsx`).

## Export

`POST /v1/export` (idempotent, `idempotency.md`) answers 202 with an
`ExportJob` (`id`, `status`, `url`, `expires_at`, `bytes`) and
`GET /v1/export/{exportId}` reports it. The job shape lets the endpoint
become asynchronous without a client change; the work runs inline in
`ExportService.Start`, and the job is `ready` by the time it is returned,
because a whole personal corpus is a few hundred index reads.

The file is one JSON document, not an archive. `build` writes:
`version` (2), `tenant_id`, `exported_at`, the `settings` record,
`notes` — every active and archived note (`DrainNotes`, at most
`maxExportNotes` = 2000, `truncated` set when the cap is reached) with its
index fields, its `body` with the append markers stripped exactly as
`GET /v1/notes/{id}` strips them, its `cleaned` view (body, mode,
`generated_at`, `stale`) when it has one, and its `captures` (at most
`maxExportCapturesPerNote` = 500) — then `unrouted_captures`, the recordings
that never reached a note, and `artifact_keys`: for every capture the S3
keys of its audio, raw, routed and clean text, segments and peaks, so a
restore can find the binaries the JSON deliberately does not inline
(`docs/ops/backup-restore.md`). Tests:
`TestExportEnumeratesNotesRatherThanAnAllowlistOfKinds`,
`TestExportIncludesACaptureThatNeverReachedANote`,
`TestExportCarriesTheCleanedViewWhenThereIsOne`.

The document and its job record live at
`tenants/<tenant>/exports/<id>/{export.json,job.json}`; the id is checked
against `exportIDRe` (`^[A-Za-z0-9_-]{1,64}$`) before it enters a key, so
another tenant's id addresses a key under the caller's own prefix and is
absent (`TestExportKeyRefusesATenantIDThatWouldEscapeItsPrefix`,
`TestGetRefusesAnExportIDThatCouldNotHaveBeenIssuedHere`). Both objects
carry `chintan-artifact=export`, which the bucket expires after one day
(`retention.md`); the download URL is presigned per read for
`exportURLTTL` (15 minutes) and never stored
(`TestStartDoesNotPersistThePresignedURLItReturns`,
`TestGetMintsAFreshURLForAReadyExport`).

On You, `exportBlob` starts the job once, polls every `EXPORT_POLL_MS`
(1.5 s) for at most `EXPORT_TIMEOUT_MS` (two minutes), fetches the bytes
itself and hands them to the shared save path — a cross-origin presigned
URL opened directly would display in the tab rather than save — under
`<app>-notes-<local day>.json` (`exportFilename`); the button reads
"Preparing your file…" while the job runs (`ExportCard.test.tsx`).
`chintanctl export` is the operator's path to the same document. There is
no delete-account control; `chintanctl erase` is that path.

History: `docs/backlog.md`.
