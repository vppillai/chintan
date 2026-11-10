# A voice append and an editor save on the same note

The worker appends a recording's paragraph to a note body that the person may
be typing into at the same moment. This note is the protocol that keeps both
edits, and why each part of it is there. Code: `Pipeline.append`
(`backend/internal/pipeline/append.go`), `NotesService.UpdateNote`
(`backend/internal/service/notes.go`), `Store.StampNoteAppend` /
`ClearNoteAppend` (`backend/internal/repository`). The conditional body write
and the index refresh are `service.RewriteNoteBody` and
`service.RefreshNoteIndex` (`backend/internal/service/capture_edit.go`): one
loop each, shared with the delete and move paths, and the pipeline calls them
with its own edit and `RefreshOptions`.

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
the marker and does nothing. The dictation survives only in `clean.txt`.

The window is the append's write-to-refresh span, one S3 GET and PUT, one
GetItem, one S3 GET, one PutItem: tens of milliseconds, against an autosave
every 1.2 s into the note the recording files into.

## Why the version alone cannot close it

`version` is not a witness of the body write, because the worker bumps it
afterwards. No ordering of the save's two reads catches a body write whose bump
has not landed.

The obvious fix — bump the version before the body write — narrows the window
and then walks the client into it. The client's response to a version-only
conflict is to re-read and, finding the same text at a newer
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
   other's stamp for up to `Config.AppendStampWait` (10 s by default) before
   stamping over it, so the row's one stamp always names the write in flight;
   only a holder that died mid-append is ever stamped over. Every other
   writer of a body that is not the editor stamps the same way, through the
   one loop (`service.StampNoteForBodyWrite`): a move stamps the target and
   the source before it writes either (`moveInto`), a delete stamps the note
   before it cuts the paragraph (`cutFromNote`), and a regeneration stamps a
   checklist before it withdraws a recording's items (`dropReplacedItems`).
   Each clears its stamp with the refresh that follows its write. The two on
   the request path wait too, more briefly (`stampNote`, `noteBusyWait`,
   3 s): the Recordings tab deletes or moves a selection as one request per
   recording, all at once, and each sibling sits out the stamp of the one
   before it. They never stamp over: a stamp that has stood longer than
   that was left by a writer that died before its refresh, and the delete or
   move is refused at once (409 for the delete, the move's "nothing was
   changed; try again") until that writer's retry or the lease clears it.
2. **Clear after completing.** The worker's `RefreshNoteIndex` publishes the
   paragraph's snippet and search text and bumps the version, so the row's
   version is once more a witness of the body; the stamp stays until
   `CompleteCaptureAppend` has marked the capture appended, and then
   `ClearNoteAppend` removes it, conditional on the stamp still naming the
   capture; a retry or redelivery that finds the capture already appended
   clears it too, for the attempt that died between the two. Cleared any earlier, a completion that failed left the paragraph
   open to a save while the capture was unfinished: the edit moved the words
   off the marker, the retry did not recognise its own paragraph, and the
   takeover after the lease wrote the dictation twice. A move and a delete
   clear their stamps with the refresh of each note they wrote
   (`ClearAppendStampFor`); a delete whose refresh failed leaves its stamp
   standing, and its repeat — which finds nothing left to cut — refreshes
   anyway, because the capture is `appended` or the row still names it, and
   clears it. The item withdrawal hands its stamp back when its refresh
   fails, since nothing repeats it. An append, a
   delete or a move that stops without writing hands the stamp back with
   `ClearNoteAppend`.
3. **Refuse the save.** `UpdateNote` refuses a body write with
   `ErrAppendInProgress` while `appending_capture` is set and `appending_at` is
   younger than `AppendClaimLease` — checked **before** the version, because
   the stamp bumped the version and a version conflict is what sends the client
   round the rebase. Metadata-only saves go through and carry the stamp
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

The adapter's half of the ETag contract — the tag `GetWithETag` hands out is
the one `PutIfMatch` must send back, a stale one is `ErrPreconditionFailed`
with the body untouched, `""` is If-None-Match: * — is proven in
`backend/internal/repository/s3_test.go` against an in-memory bucket that
answers as S3 does (`s3fake_test.go`); `memory.Objects` implements those
semantics by construction and cannot test them.

## The wire

`PATCH /v1/notes/{id}` answers the refusal as **409** in the version-conflict
problem shape (`current_version` present) plus `"reason": "append_in_progress"`
and `Retry-After: 2`. The client repeats the **same** save unchanged after the
header and does not rebase; once the paragraph is in the body the next answer
is a plain 409 and the normal conflict prompt is the right outcome. Still
refused after `APPEND_WAIT_LIMIT` waits, the append is stuck behind its lease
(below), not landed: the save fails with "A recording is still being added to
this note. Try again in a few minutes." rather than offering Keep my edits
against a paragraph that never arrived. The offline queue leaves such an entry
for its next pass, counting no attempt (`offline.md`). The reason
is a fixed string the frontend matches, like `type` for the spend cap. Fixture:
`problemAppendInProgress` in `frontend/src/api/__fixtures__/responses.ts`.

## The version the editor saves on

The editor's PATCH carries the note's `version`, and two things bump it
without touching the words: the worker's answer to a clean request (the
request itself only stamps the row, `StampCleanRequest`, and moves nothing),
and the stamp a recording's append, move or delete puts on the row before it
writes the body; the poll only delivers the new version to the editor, it
makes none. `useNoteEditor.ts` therefore has one rule in three
places: **the version moves only when the
words did not.** The sync effect, on a newer `note`, resets a settled editor
to it, and otherwise rebases the draft onto the new version when the
server's text is what this editor last saved (`sameText`); the 409 path
fetches the server's copy and, under the same test, rebases and resends once
(`rebased`) — and when that copy cannot be fetched, fails the save for Try
again rather than building a conflict from a guess; and `adopt(fresh)` is that rule on demand, for a caller holding
a copy it knows is the server's own. `useApplyTidy` (`useTidyList.ts`) is
that caller: a child's effect runs before the editor's own sync effect has
seen the poll that brought the tidy's answer, so without it the answer would
be saved on the pre-clean version and come back 409 — it adopts the poll's
copy first. A real change on the server, words and all, still meets the version check and
the conflict prompt.

## What is still open

A worker killed between the stamp and the body write leaves the stamp on the
row for the length of the claim lease (twenty minutes), during which body saves
to that note are refused and the capture reads `appending`. That is the same
window the claim itself imposes on the capture, and the same recovery: the
user's retry after the lease. It is one Lambda kill inside a hundred
milliseconds, and the honest price of a mutex with a lease.
