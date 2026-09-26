# Proposed file splits (pure moves; no behaviour change)

All line numbers are main @ 8f80f59. Each split keeps the public names and their import paths where an
index/re-export is cheap, so tests and callers do not move.

## frontend/src/features/capture/FilingRow.tsx (866 lines) → 5 files

| New file | Moves from FilingRow.tsx | Lines |
|---|---|---|
| `capture/filing/model.ts` | `Stage`, `STAGES`, `stageIndex` (49–81), `STUCK_AFTER_MS`/`isStuck` (82–107), `RETRY_ACCEPTED_*`/`retryAccepted` (108–119), `describe` (120–147), `FILED_ROWS_MAX`/`capFiledRows` (238–257), `retryMessage` (444–447) | ~110 |
| `capture/filing/useLocalUpload.ts` | `HANDOFF_GRACE_MS`, `useLocalUpload` (148–237) | ~90 |
| `capture/filing/FilingItem.tsx` | `FilingItemProps`, `FilingItem` (448–619), `FilingStages` (620–663) | ~215 |
| `capture/filing/TargetPrompt.tsx` | `TargetPrompt` (664–781), `BrowsePicker` (782–866) | ~200 |
| `capture/FilingRow.tsx` (stays) | `FilingRow` (258–354), `LocalUploadItem` (355–443) | ~190 |

`FilingBanner.tsx` imports `FilingItem` from its new file. `FilingRow.test.tsx` (1,220 lines) splits the same way:
`model.test.ts` (stage/stuck/cap cases), `FilingItem.test.tsx`, `TargetPrompt.test.tsx`, `FilingRow.test.tsx`.
Do this before the notifications-grouping stream touches the receipts: that stream then edits `model.ts` and
`FilingRow.tsx`, not an 866-line file.

## frontend/src/features/notes/Recordings.tsx (1,148 lines) → 4 files

| New file | Moves | Lines |
|---|---|---|
| `notes/recordings/labels.ts` | `justLanded` (508), `isFromDevice` (520), `sourceLabel` (530), `heardAs` (554), `retranscribeLabel` (565), `failureText` (611), `describeOutcome` (619), `filedLabel` (658) | ~130 |
| `notes/recordings/useRetranscribeCapture.ts` | `Notice`, `useRetranscribeCapture` (539–607) | ~70 |
| `notes/recordings/RecordingRow.tsx` | `RecordingRowProps`, `RecordingRow` (675–1125), `PlainScrubber` (1126–) | ~460 |
| `notes/Recordings.tsx` (stays) | `Recordings` (92–507): list, selection, move/delete, download | ~420 |

`Recordings.test.tsx` (1,132 lines) → `labels.test.ts` (pure functions, no DOM), `RecordingRow.test.tsx`, `Recordings.test.tsx`.

## frontend/src/screens/NotesScreen.tsx (986 lines; the component is 816) → 5 files

| New file | What leaves `NotesScreen()` |
|---|---|
| `screens/library/useLibraryParams.ts` | `view`/`tag`/`kind`/`asking`/`q` from `useSearchParams` and every `setParams` writer (replace-not-push) |
| `screens/library/useLibrarySelection.ts` | selection set, Shift-range, Escape, the bar's actions (Delete → `useBulkArchiveNotes` + `showDeleted`/Undo, Restore, Delete forever + `ConfirmDialog` with `HOLD_TO_DELETE_ABOVE`/`HOLD_TO_DELETE_MS`) |
| `screens/library/LibraryField.tsx` | the search/ask `<input>`, the Ask glyph, `narrowField`, plus `Chip` (916–945) and `chipScrollBy` (946–961) as the chips row |
| `screens/library/LibraryList.tsx` | `PinnedGroup` + `groupByDay` day sections + `LoadMore` + the offline/"Saved on this device" captions + `noteForHit`/`countLabel`/`failureMessage` (962–986) |
| `screens/NotesScreen.tsx` (stays) | composition, `PullToRefresh`, `FilingRow`, `ResumePrompt`, `PasskeyNudge`, the lazy `AskPanel` |

Tests (`NotesScreen.test.tsx`, `.pins.test.tsx`, `.checklist.test.tsx`) keep rendering `NotesScreen`; none move.
`NARROW_FIELD_QUERY`, `HOLD_TO_DELETE_ABOVE`, `HOLD_TO_DELETE_MS` stay exported from `NotesScreen.tsx` (tests import them).

## frontend/src/api/queries.ts (1,070 lines) → 6 files, one re-export

`api/queries/notes.ts` (39–601: keys, useNotes, useSearchCorpus, useNote, recordSavedNote, the list patch/snapshot/restore
helpers, usePinNote, useReorderPins, create/archive/restore/undo/forever/bulk, useSearch),
`api/queries/settings.ts` (602–653: useSettings, useSaveSettings, useUsage),
`api/queries/ask.ts` (654–717), `api/queries/captures.ts` (718–1039: poll intervals, isFilingRelevant, usePendingCaptures,
newlyAppendedNoteIds, refreshAppendedNote, delete/move/retry/setTarget), `api/queries/devices.ts` (1040–),
and `api/queries.ts` becomes `export * from './queries/notes.ts'` … so no import changes anywhere.
Delete `useTags` (585–601, unused) on the way.

## backend/internal/pipeline/pipeline.go (2,174 lines, 55 funcs) → 6 files, same package

| New file | Functions (current line) |
|---|---|
| `pipeline.go` | `New` 185, `Run` 251, `RunUpload` 260, `runCapture` 275, `RejectOversizedCapture` 363, `run` 409, `wantsNoteLanguage` 533 |
| `transcribe.go` | `transcribe` 606, `transcriptionLanguage` 734, `languageOutcome` 769, `newTranscriptDocument` 805, `seconds` 825, `contentTypeForAudioKey` 2158 |
| `route.go` | `stripInstructions` 559, `route` 836, `decideTarget` 952, `preferExistingTitle` 1004, `titleNames` 1030, `normalizeTitle` 1044, `routeWithRetries` 1051, `routeOnce` 1103, `routeRetryReason` 1146, `estimateCandidateTokens` 2105, `fallbackNoteTitle` 2123, `noteTouchedAt` 2150 |
| `clean.go` | `clean` 1170, `extractItems` 1261, `cleanupLanguage` 1351, `tokenUsage` 2088, `estimateTokens` 2098 |
| `append.go` | `append` 1362, `stampNoteAppend` 1497, `anotherAppendInFlight` 1531, `finishAppend` 1549, `paragraphInNote` 1571, `appendToken` 1585, `releaseAppendClaim` 1594, `replaceCaptureParagraph` 1615, `keepTick` 1637, `checklistItems` 1655, `appendToNote` 1687, `refreshNoteIndex` 1747 |
| `status.go` | `setStatus` 1791, `persist` 1807, `markAudioProcessedIfSafe` 1843, `verifyPeaks` 1878, `concede` 1909, `handleProviderError` 1975, `isDeadline` 2042, `providerForStage` 2051, `markFailed` 2065 |

No export changes, no test moves. `gofmt -l`, `go vet`, `go test -race ./internal/pipeline/` are the check.
A *package* split is not proposed (see owner decision O1).

## frontend/src/styles/shell.css (3,311 lines) → re-homed into the sheets that already exist

| shell.css section (header line) | Goes to |
|---|---|
| library heading, day groups, chips, note rows, tag chips (198–700) | `home.css` (232 today) |
| filing rows and receipts (1082–1360) | `capture.css`, or a new `filing.css` if the notifications stream redesigns them |
| players, scrubber, transcript (1363–1750) | new `recordings.css` |
| note screen, tabs, meta, Details sheet (1751–2214) | `notes.css` (190 today) |
| cleaned panel / Split up (2215–2592) | new `cleaned.css` |
| recordings list, selection bar, swipe tray (2593–2900) | `recordings.css` |
| remaining: app shell, tab bar, toast, dialogs, buttons, utilities | `shell.css` (~900 lines) |

Pure moves; `index.css` gains the imports; `LAYOUT_SHOTS=1 bun run e2e -- layout` before/after must be pixel-identical.
Delete the 236 dead-rule lines (evidence/css-unused.txt and the rule list in the report) first.
