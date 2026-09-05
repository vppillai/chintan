# A voice append and an editor save on the same note

The worker appends a recording's paragraph to a note body that the person may
be typing into at the same moment. This note is the protocol that keeps both
edits, and why each part of it is there. Code: `Pipeline.append` and
`refreshNoteIndex` (`backend/internal/pipeline/pipeline.go`),
`NotesService.UpdateNote` (`backend/internal/service/notes.go`),
`Store.StampNoteAppend` / `ClearNoteAppend` (`backend/internal/repository`).

## The two writers

The note is two things: the body in S3 and the index row in DynamoDB. The row
carries `version`, the optimistic-concurrency counter every writer conditions
on, and the body carries an ETag.

The **worker's append** writes the body first (a conditional PUT on the ETag it
read, paragraph and marker in one write) and refreshes the row afterwards
(`GetNote`, re-derive snippet and search text from the body, `PutNote`, which
bumps `version`). The body write moves the ETag; only the refresh moves the
version.

The **editor's save** (`PATCH` with `body` and `version`) reads the body's ETag
and the row, checks the version it was sent against the row's, writes the body
conditionally on the ETag with the stored markers carried onto the client's
text (`CarryCaptureMarkers`), then `PutNote`s the row.

## The hole

Read the row before the append's refresh (version unchanged) and the body's
ETag after the append's write (ETag current): both checks pass. The save then
writes the client's text — which predates the paragraph — with the marker
carried, so the body says "this capture's paragraph is here" and the paragraph
is not. `CompleteCaptureAppend` marks the capture appended; every retry finds
the marker and does nothing. The dictation survives only in `clean.txt`
(review 2026-09-05 round 2, S1).

The window is the append's write-to-refresh span, one S3 GET and PUT, one
GetItem, one S3 GET, one PutItem: tens of milliseconds, against an autosave
every 1.2 s into the note the recording files into.

## Why the version alone cannot close it

`version` is not a witness of the body write, because the worker bumps it
afterwards. No ordering of the save's two reads catches a body write whose bump
has not landed.

The obvious fix — bump the version before the body write — narrows the window
and then walks the client into it. The client's response to a version-only
conflict (#53, F14) is to re-read and, finding the same text at a newer
version, re-send at once. Against a pre-bump that re-send carries the current
version, reads its ETag after the body write, and passes both checks: a rare
race made deterministic.

## The protocol

1. **Stamp before writing.** After `ClaimCaptureAppend` succeeds and before the
   body write, one conditional `UpdateItem` on the row:
   `SET version = version + 1, appending_capture = <id>, appending_at = <now>`
   with `version = <read>` as the condition (`StampNoteAppend`). Named
   attributes, not a whole-row `PutNote`: the worker must not carry a stale
   `cleaned_body` or `search_text` over a concurrent writer's, and the version
   should move for exactly the reason the save is told about. A lost race
   re-reads and stamps again. Two captures appending to one note wait on each
   other's stamp for up to `appendStampWait` before stamping over it, so the
   row's one stamp always names the write in flight; only a holder that died
   mid-append is ever stamped over.
2. **Clear after indexing.** `refreshNoteIndex` clears the two attributes in
   the same `PutNote` that publishes the paragraph's snippet and search text,
   so the row's version is once more a witness of the body. An append that
   hands its claim back without writing clears the stamp with
   `ClearNoteAppend`, conditional on the stamp still naming it.
3. **Refuse the save.** `UpdateNote` refuses a body write with
   `ErrAppendInProgress` while `appending_capture` is set and `appending_at` is
   younger than `AppendClaimLease` — checked **before** the version, because
   the stamp bumped the version and a version conflict is what sends the client
   round the F14 rebase. Metadata-only saves go through and carry the stamp
   forward. A stamp past the lease was left by a worker that died between
   stamping and writing; the lease is what lets that capture be retried, and
   the same bound lets the editor save again.
4. **Read the ETag first.** `UpdateNote` reads the body's ETag before the row,
   so the version is the later witness. Belt and braces with the stamp: the one
   hole left after 1–3 was the stamp and the body write both landing inside the
   save's few-millisecond gap between its two reads, and with the ETag read
   first that ordering fails the version check instead. The object key is
   derived from the ids (`keys.NoteMarkdown`) so it is known before the row is
   read; a row naming another key falls back to reading after.

## The wire

`PATCH /v1/notes/{id}` answers the refusal as **409** in the version-conflict
problem shape (`current_version` present) plus `"reason": "append_in_progress"`
and `Retry-After: 2`. The client repeats the **same** save unchanged after the
header and does not rebase; once the paragraph is in the body the next answer
is a plain 409 and the normal conflict prompt is the right outcome. The reason
is a fixed string the frontend matches, like `type` for the spend cap. Fixture:
`problemAppendInProgress` in `frontend/src/api/__fixtures__/responses.ts`.

## What is still open

A worker killed between the stamp and the body write leaves the stamp on the
row for the length of the claim lease (twenty minutes), during which body saves
to that note are refused and the capture reads `appending`. That is the same
window the claim itself imposes on the capture, and the same recovery: the
user's retry after the lease. It is one Lambda kill inside a hundred
milliseconds, and the honest price of a mutex with a lease.
