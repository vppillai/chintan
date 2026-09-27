# Regenerating a note from its recordings

The prompts change — the items prompt of 2026-09-26 turned "add milk to the
shopping list" from the item `add milk to the shopping list` into `Milk` — and
a note made before the change keeps the words the old prompt produced.
Regeneration re-runs the one pipeline stage that depends on a prompt over
every recording the note holds, from the transcript that is already stored,
and puts each recording's new words where its old ones stand. Nothing is
transcribed again; the title is not touched. Code:
`service.RegenerableCaptures` / `NotesService.RequestRegenerate`
(`backend/internal/service/note_regenerate.go`), `Pipeline.RegenerateNote`
and `replaceChecklistItems` (`backend/internal/pipeline/regenerate.go`,
`append.go`), the handler in `handler/notes.go`, `chintanctl regenerate`
(`backend/cmd/chintanctl/regenerate.go`) and the menu item in
`frontend/src/features/notes/NoteActions.tsx`.

## What is re-done, and what is not

For a plain note each recording's paragraph is cleaned again with the current
cleanup prompt (`Pipeline.clean`) from the transcript the pipeline kept — the
routed one, with the words spoken to the app already removed, when the router
or the instruction strip left one; the raw one otherwise — and replaced under
its marker by the same path a retranscription uses (`replaceCaptureParagraph`).
Text the person edited inside that paragraph is overwritten, and the confirm
says so. For a checklist each recording's items are extracted again with the
current items prompt (`Pipeline.extractItems`) and swapped for the items it
produced last time; ticks are kept (`keepTick`, below); items the person typed
have no recording and stay. The whole-note cleaned view is regenerated
afterwards when the note has one (`cleanNoteAfter`), once, after the last
recording, rather than after each — with `auto_clean` every append would
have queued a run and every run but the last would have been superseded
after its model call was billed.

Not re-run: transcription (that is the per-recording "Transcribe again",
`POST /v1/captures/{id}/retranscribe`, which costs a speech call and may
change the words themselves), routing (the destination is decided), the
instruction strip (its answer is in the routed transcript), and the
post-routing language check (the transcript is the thing being kept). A
verbatim note has nothing to regenerate: its cleanup is bypassed, so no
prompt had a hand in its words.

## Which recordings

`service.RegenerableCaptures` is the one rule, applied by the API's request
path and by the worker when an operator's task names no recordings, so the
two roads regenerate the same set. A recording qualifies when it is
`appended` and has a transcript (`RawKey`). For a plain note its paragraph
must still be under its marker with text in it: the editor carries a marker
to the end of the body when the person rewrites its paragraph
(`CarryCaptureMarkers`, `append-vs-autosave.md`), and replacing what is no
longer there would land a second copy beside the person's words — so such a
recording is skipped and not counted. A checklist is different, see below.
At most two hundred recordings per request (`MaxRegenerateCaptures`), the
newest two hundred the store lists. The listing is GSI1's projection — no
`last_progress_at` to judge a stuck capture by, and a row written back from
it would drop the language, the source and the timing record — so it only
picks the candidates, and each is read whole before it is judged or reset;
the `dynamofake` table double is what caught that.

## The two roads

**From the app.** `POST /v1/notes/{id}/regenerate` (JWT, idempotent) does
for every qualifying recording what `RetranscribeCapture` does for one: the
row goes back to `transcribed` with its clean artefact and its append claim
cleared (`ResetForRegenerate`) — the transcript keys stay — and the worker
is invoked once with the note and the ids (`TaskRegenerateNote`,
`Invocation.CaptureIDs`). 202 `{status: queued, captures: N}`; `N` is zero,
and nothing is queued, when nothing qualifies. The spend gate answers first,
as it does for `/clean`: the run is `N` cleanup calls, each reserved and
priced like any other (`meter.OpCleanup`; measured p50 143 µ$ a call,
`prompts.md`), so a capped instance is told before anything is reset. A
failed Invoke puts the rows back (`restoreAfterFailedHandOff`), so the
person's retry a moment later is not met with "in flight".

**From the operator.** `chintanctl regenerate --instance dev --tenant <id>
(--note <id> | --all)` plans from the rows and one bucket listing — the
appended recordings with a transcript, in the active, non-verbatim notes —
prices each as the worker reserves for its call (transcript bytes/4 tokens
in and the same out, `meter.DefaultPrices` on `--llm-model`, MiniMax-M3 by
default) and prints the count and the estimate; it reads no body, so the
count is an upper bound of what the worker will run. Dry run is the default,
as for every chintanctl command; `--apply` asks the operator to type `yes`
(`--yes` skips it) and then queues one `regenerate-note` task per note with
no ids, through the stack's `WorkerFunctionLiveAliasArn`. The worker chooses
and resets the recordings itself on that road, one note per invocation, so
the command writes nothing to the table. It needs `lambda:InvokeFunction`
on the worker, which the agent principal deliberately lacks.

## In flight, and progress

The captures' statuses are the whole of the state. A second request while
any capture of the note is non-terminal and not stuck (`CaptureStuck`) — this
regeneration, or a recording being filed — or while the row carries a young
append stamp (`AppendInProgress`) is 409 `this note is being regenerated, or a
recording is being filed into it; wait for it to finish`. The app's poll and
strip are the ones a fresh recording gets: `useNote` polls while any capture
is non-terminal (`capturePollInterval`), the `FilingBanner` shows the newest
one moving, and the ⋮ item reads "Regenerating…" and is off until the last
lands; the body refreshes as each paragraph is replaced. No note-level
`regenerate_state` was added: it would have been a second copy of what the
rows already say, to be kept in step on every path that touches them.

The worker runs the recordings one at a time, oldest first, so the appends
to one body never wait on each other's stamp. A recording that fails on its
own terms — the provider, the spend cap — keeps that verdict for the strip
to show and Retry to resume from its transcript, and the run goes on; an
infrastructure fault returns the error, Lambda retries the task, and the
recordings already appended are skipped by their status, so nothing is
billed twice. `CaptureRegenerated{Outcome}` counts each; the cleanup calls
land under the usual `cleanup` op in `GET /v1/usage`.

## Checklists: items found by their words

A list only ever spoken to keeps each recording's items under its marker,
and they are replaced there. But every save from the Items tab — a tick, a
drag — carries every marker to the end of the body with nothing under it
(`checklists.md`, "The append rule"), so on any list that has been used the
marker says which recordings the list has and nothing about which lines are
theirs. Regeneration therefore finds a recording's previous items by their
words: `extractItems` reads the recording's clean artefact — one item per
line, exactly what the last append rendered — before overwriting it, and
`replaceChecklistItems` removes those lines from the list, each once, case
and whitespace aside, and writes the new items where the first of them stood.
Everything else keeps its order, typed items included. The marker stays
where it was: put back above the new lines it would claim every line down
to the next marker, typed ones too, for the next delete or move. When none
of the old words are left the person deleted that recording's items, and
nothing is put back. A recording whose transcript, extracted again, names
nothing to add — "create a shopping list" — becomes `no_content` and its old
items come out (`dropReplacedItems`), since a `no_content` recording owns no
lines.

`keepTick` carries a tick to the new line with the same words, wherever the
new prompt put it; when no words match at all and the counts agree it falls
back to the line, as it did before, so a retranscription that reworded every
item is carried as it always was. A tick that cannot be placed either way
is dropped: an open item the person can tick again beats a tick on the
wrong item. The same rules now apply to "Transcribe again" on a checklist,
which until 2026-09-27 could only replace under the marker and, on a used
list, added its new items beside the old ones.

## Rejected

A note-level `regenerate_state` on the wire (a second copy of the captures'
state); running the recordings as `N` separate invocations (`N` concurrent
appends to one body, each waiting on the last's stamp); per-capture
auto-clean during a run (`N` whole-note calls, `N−1` superseded); reading
the note body in `chintanctl` to make the count exact (one GET per note on
`--all` for a number the worker reports anyway); a `--dry-run` flag beside
the house convention that dry run is the default.
