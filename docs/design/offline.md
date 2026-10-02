# Offline

What the device keeps, what it owes the server, and what each screen does
with no connection. Code: the schema (`frontend/src/offline/db.ts`), the
note cache (`notesCache.ts`, read through `useNotesCache.ts`), the edit
queue (`queue.ts`, `queuedEdits.ts`, drained by `useOfflineQueue.ts`), the
banner (`OfflineBanner.tsx`), the browser's own word on connectivity
(`hooks/useOnline.ts`) and the recording that waits for a connection
(`features/capture/useResendOnReconnect.ts`). The service worker's precached
shell is `shell.md`'s; the note editor's version and conflict prompt are
`append-vs-autosave.md`'s.

## The database

One IndexedDB database, `chintan`, at `DB_VERSION = 2`, opened once through
`openChintanDB` and reopened when Safari closes the handle behind a
backgrounded page (`terminated`). Four stores, each one job:

| Store | Holds | Why |
|---|---|---|
| `captureChunks` | audio as it is produced, as `ArrayBuffer`s keyed `<localId>:<index>` with the index zero-padded to six digits so key order is chunk order, indexed `byLocalId` | a crash or a killed tab does not lose the recording (`capture-ux.md`) |
| `captures` | per-recording metadata | the progress card is rebuilt from disk on a cold start |
| `mutations` | the edit queue, indexed `byCreatedAt` | an edit made offline reaches the server later |
| `notes` | the note corpus, indexed `byUpdatedAt` | a note read once is readable and searchable offline |

`clearAllLocalData` empties all four; sign-out is its only caller, because
every store holds one person's data and a queued edit left behind would
flush under the next session's token.

## The note cache

Every list the user sees and every note they open passes through the cache
on its way to the screen (`cacheNoteList` in `useNotes` and
`useSearchCorpus`, `cacheNoteDetail` in `useNote` and `recordSavedNote`, all
in `api/queries/notes.ts`), so the cache is a side effect of using the app
rather than a sync. A row is stored whole, with two flags lifted out:
`detail` (whether `body` and `captures` are present) and `archived`, so the
library and the archive read back separately. A delete-forever calls
`forgetNote`: an offline library that lists a note the user destroyed is the
one disagreement between the two copies that is not tolerable.

Two rules shape the store, both in `supersedes`:

- **A list row is not a note.** `GET /v1/notes` carries no body, so a row
  cached from it is `detail: false` and `cachedNote(id, { requireDetail })`
  refuses to hand it to the note screen. A real title over an empty body
  invites typing, and the first keystroke would queue a PATCH that erases
  the note.
- **Newer wins, and a full note is never displaced by a row of the same
  vintage.** `version` decides where both sides have one; `updated_at`,
  fixed-width RFC3339 by contract, is the fallback. Among list rows of one
  vintage, a row carrying `search_text` (from the corpus request) keeps its
  place over the plain library row, so the words search relies on do not
  vanish between corpus fetches.

`cacheNoteList` reads the page's own rows by key before opening its write
transaction and issues every `put` without an intervening `await`: an
IndexedDB transaction commits when its microtask queue drains, so a read
awaited inside it closes it and the puts are lost. Tests: `notesCache.test.ts`
("what the device keeps", "the searchable body survives the library row").

### Reading it

`useCachedNotes(state)` and `useCachedNote(id)` (`useNotesCache.ts`) read
the device's copy as queries under `['notes', 'offline', …]` with
`networkMode: 'always'` (the list at `staleTime: 0`, the note at
`staleTime: 5_000`). The hooks are separate from
`useNotes` because TanStack *pauses* a server query when the browser reports
no connection — it neither runs nor fails — so a fallback cannot live inside
it. The key sits under the `notes` prefix so a mutation's invalidation and
the banner's lookup see the device's copies along with the server lists.

### The first page's bodies

The library asks for `prefetchBodies`; the capture screen and Ask, which
read the same rows, do not, so a cold launch of the Record shortcut has no
GETs competing with `getUserMedia`. Once rows are on the device,
`usePrefetchBodies` waits for idle (`whenIdle`: `requestIdleCallback` with a
10 s timeout, or a 2 s delay where Safari lacks it), skips entirely when
`navigator.onLine` is false, and fetches one at a time the newest
`PREFETCH_BODIES = 20` active rows the device holds as list rows alone
(`notesWithoutBody`). Each `id@version` is asked for once per session;
passes repeat while rows land during one, so a one-row page arriving first
does not end the pass at one body. The session is checked on both sides of
every GET: sign-out must not leave a body on the device for the next person.
Tests: `useNotesCache.test.tsx` ("the first page's bodies reach the device on
their own"); `e2e/offline.spec.ts` "a note never opened on this device is
still readable offline once the list has been seen" and "every first-page
body lands even when a one-row list page arrives first".

## The edit queue

`mutations` holds one kind, `updateNote`: a note PATCH is the one write made
without a button the user is looking at. Archive, restore, retry and
set-target report their failure to the person who pressed them, and a
capture upload resumes from the audio on disk. Each entry carries the
`Idempotency-Key` minted at enqueue time (`idempotency.md`), so a flush that
partly succeeded replays rather than double-applies.

**One entry per note.** The editor (`useNoteEditor`) catches an offline
`ApiError` and calls `enqueueReplacing` under `queuedEditId(noteId)` =
`updateNote:<id>`, folding the new fields over whatever body is already held
(`queuedEditBody`): a PATCH carries the whole note, so three offline edits are
one write made three times. The replacement keeps `createdAt` (the order of
intent) and mints a fresh key (a different payload under the same key would
replay the earlier response). The editor then patches its caches as a landed
save would, at the same version, so the note reopened offline shows the
edit and the next keystroke builds on it.

**The flush** (`queue.flush`, run by `useOfflineQueue`) walks entries oldest
first and never overlaps itself: a reconnect and a window focus landing
together share one pass, because two passes replayed the same PATCH and the
loser marked a delivered edit dead. It stops at the first offline error.
A terminal status — `TERMINAL_STATUSES = {400, 403, 404, 409, 413, 422}` —
retires the entry at the attempt ceiling (`markDead`) rather than deleting
it; any other failure counts an attempt, and an entry at
`MAX_ATTEMPTS = 8` is skipped and kept. Kept, not dropped: the editor reads
the queue back (`queuedEditFor`) to say "did not save, and here is why"
instead of promising a sync that is not coming. 401 is not terminal (a
refresh fixes the next flush) and nor is 429 (a rate limit, or the spend
cap, means not today).

**409 is terminal.** The queued PATCH carries the version the note had when
it loaded, and that never changes, so replaying it is eight guaranteed
conflicts for an outcome settled at the first. The entry stays; back online,
the editor reads the held body, fetches the server's copy and shows the
conflict prompt — Keep my edits, or Use the newer version
(`append-vs-autosave.md`). Keep sends the held body on the newer version;
either choice clears the entry (`clearQueuedEdit`).

`useOfflineQueue` models the drain as a query with `networkMode: 'always'`,
`refetchOnReconnect`, `refetchOnWindowFocus` and a 60 s `refetchInterval`
for the captive portal that keeps `navigator.onLine` true; offline it only
counts. After a pass that applied or failed anything it invalidates
`queued-edit`, `note` and `notes`, so the screen stops saying "will sync"
over an edit the server has. Tests: `queue.test.ts` ("flush", "flush runs one
pass at a time", "what counts as terminal"); `e2e/offline.spec.ts` from "an
edit made offline is queued on the device" through "a queued edit refused as
a conflict can be dropped".

## The banner

`OfflineBanner`, in the shell's banner row, says two things and only when
true: "Offline — showing saved notes." or "Offline — nothing is saved on this
device yet.", decided by whether any `['notes']` query in the TanStack cache
holds an item (there is no query persister, so an offline cold start has
nothing cached before a read lands); and "Waiting to sync N changes." /
"Syncing N changes." from the queue's depth. Online with an empty queue it
renders nothing. `useOnline` is `navigator.onLine` with the `online`/`offline`
events, enough to decide whether to attempt a flush; whether a request
worked is the request's to say. Tests: `OfflineBanner.test.tsx`.

## What works where

- **Home** (`screens/NotesScreen.tsx`): the cached list is shown whenever
  the server has answered nothing at all — `data === undefined`, not
  "offline", because an empty answer is authoritative and falling back on
  it would resurrect a note archived on another device. `tag` and `kind`
  are applied by hand (the cache knows neither). The caption "saved on this
  device" appears only once the fetch is paused, errored or the device is
  offline; a server failure while online says so instead, with Retry
  (`serverFailed` in `screens/library/LibraryList.tsx`). The tag chips come
  from the cached active notes. Search ranks the cached corpus on every
  keystroke (`search.md`). The + for a typed note says it needs a
  connection: the queue holds edits to notes that exist, not notes.
- **A note**: the cached full note stands in while the server has not
  answered (`offlineCopy`); a note the device holds only as a row reads
  "Not on this device" with the offline sentence, never "archived or
  purged". The editor saves into the queue as above; the Recordings tab
  treats a paused or network-failed artifacts query as unreachable
  (`RecordingRow` `unreachable`) rather than as audio that is gone.
- **Capture**: recording works; the bytes are on disk, and a send that
  fails for want of a network is retried once per offline→online transition
  by `useResendOnReconnect` (mounted in the shell), only for a take the user
  asked to send. The target chooser and the filing row name notes from the
  cache.
- **Ask** needs the server; the panel reads the cache only for a source's
  date and snippet.
- **You, About, Usage**: the screens draw from the precached shell;
  settings come from the session's prefetch and usage needs the server.

## History

`docs/backlog.md` and `docs/history/` hold the decisions behind these rules.
