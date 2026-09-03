import { useId, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';

import {
  useBulkArchiveNotes,
  useBulkDeleteNotes,
  useBulkPurgeNotes,
  useBulkRestoreNotes,
  useNotes,
  useSearch,
  useTags,
} from '@/api/queries.ts';
import { ApiError } from '@/api/problem.ts';
import type { NoteState, NoteWire } from '@/api/schema.ts';
import { ARCHIVED_VIEW } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { NoteRow } from '@/components/NoteRow.tsx';
import { FilingRow } from '@/features/capture/FilingRow.tsx';
import { ResumePrompt } from '@/features/capture/ResumePrompt.tsx';
import { groupByDay } from '@/features/notes/groups.ts';
import { mergeResults, rankLocal, type MergedHit } from '@/features/search/localSearch.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

/**
 * The library. Home.
 *
 * One screen for the active notes, the archive and search, because they are
 * one list with three filters, not three destinations: a search field that
 * narrows the list as you type from the corpus already on the device, a row of
 * chips — All, one per tag, Archived — and the rows grouped by day beneath.
 * All of it lives in the URL (`q`, `tag`, `view`), so a filter is shareable,
 * survives reload and is what Back returns to.
 *
 * Bulk select carries over from the two screens this replaces. In the active
 * view the actions are Archive and Delete forever; in the archive, Restore and
 * Delete forever. Deleting is gated by a typed word in both, because it is the
 * one thing here that cannot be undone.
 */
export function NotesScreen() {
  const [params, setParams] = useSearchParams();
  const view: NoteState = params.get('view') === ARCHIVED_VIEW ? 'archived' : 'active';
  const tag = params.get('tag');
  const query = params.get('q') ?? '';
  const trimmed = query.trim();
  const searching = trimmed.length > 0;

  const inputId = useId();
  const listId = useId();
  const online = useOnline();

  /*
   * Filters are *replaced* in the URL, not pushed. Typing must not turn Back
   * into a character-by-character undo, and a chip is a way of looking at the
   * list, not a place the user went — Back from the library should leave the
   * library, not step through every filter they tried on the way.
   */
  const setFilter = (changes: Record<string, string | null>): void => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(changes)) {
      if (value === null || value === '') next.delete(key);
      else next.set(key, value);
    }
    setParams(next, { replace: true });
  };

  const list = useNotes({ state: view, ...(tag ? { tag } : {}) });
  const cached = useCachedNotes(view);
  /*
   * The archive, fetched alongside the active list so the Archived chip can
   * carry its count. In the archived view this is the same query as `list`
   * and costs nothing extra.
   */
  const archived = useNotes({ state: 'archived' });
  const tags = useTags();

  /*
   * Multi-select, for doing something to several notes at once rather than one
   * at a time from inside each note's own screen. Confined to component state
   * rather than the URL: leaving the screen is exactly when "which notes were
   * selected" should stop mattering.
   */
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [confirming, setConfirming] = useState<'archive' | 'delete' | 'restore' | 'purge' | null>(
    null,
  );
  const bulkArchive = useBulkArchiveNotes();
  const bulkDelete = useBulkDeleteNotes();
  const bulkRestore = useBulkRestoreNotes();
  const bulkPurge = useBulkPurgeNotes();
  const bulkBusy =
    bulkArchive.isPending || bulkDelete.isPending || bulkRestore.isPending || bulkPurge.isPending;

  const toggleSelect = (noteId: string): void => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(noteId)) next.delete(noteId);
      else next.add(noteId);
      return next;
    });
  };

  const exitSelecting = (): void => {
    setSelecting(false);
    setSelectedIds(new Set());
  };

  /*
   * TanStack *pauses* a query when the browser reports no connection: it does
   * not run and it does not fail, so neither `isLoading` nor `isError` is ever
   * true. The screen used to fall through both and render the brand-new-user
   * empty state directly under a banner saying "Offline — showing saved
   * notes.". To someone with a full library walking into a tunnel, their whole
   * library had been deleted.
   */
  const paused = list.fetchStatus === 'paused';

  /*
   * The device's copy, shown only when the server has answered nothing at all.
   *
   * The condition is `data === undefined`, not "offline": a server that has
   * answered is the authority even when it answered with an empty list, and
   * falling back on an empty *response* would resurrect notes the user had just
   * archived on another device. The cache knows nothing of tags, so the tag
   * filter is applied here by hand.
   */
  const serverNotes = useMemo(
    () => list.data?.pages.flatMap((page) => page.items),
    [list.data],
  );
  const notes: NoteWire[] = useMemo(
    () =>
      serverNotes ?? cached.data?.filter((note) => !tag || (note.tags ?? []).includes(tag)) ?? [],
    [serverNotes, cached.data, tag],
  );
  const fromCache = serverNotes === undefined && notes.length > 0;
  /*
   * Labelled only once it is clear the server is not going to answer. While a
   * fetch is still in flight the cached list is simply *shown* — instantly,
   * which is the whole point of holding it — and saying "saved on this device"
   * over a list about to be replaced would be noise.
   */
  const showingCached = fromCache && (!online || paused || list.isError);

  /*
   * Search. The corpus already on screen is ranked on every keystroke, so the
   * first result appears before the network answers and search works with no
   * connection at all; `GET /v1/search` then extends it with what only the
   * server can see — transcripts. The server is asked only online and only for
   * active notes, which is all it indexes.
   */
  const local = useMemo(() => rankLocal(notes, trimmed), [notes, trimmed]);
  const server = useSearch(trimmed, { enabled: online && view === 'active' });
  const hits = useMemo(
    () => mergeResults(local, server.data?.items ?? []),
    [local, server.data],
  );
  const serverUnavailable = view === 'active' && (!online || server.isError);

  const groups = useMemo(() => groupByDay(notes), [notes]);
  const visible: NoteWire[] = searching ? hits.map((hit) => noteForHit(hit, notes)) : notes;
  const nothingToShow = visible.length === 0;

  const archivedCount = archived.data?.pages.reduce((sum, page) => sum + page.items.length, 0);
  const tagNames = (tags.data?.items ?? []).map((item) => item.name);
  // A tag from the URL that the server no longer lists still gets its chip,
  // or the filter would be applied with nothing on screen saying so.
  if (tag && !tagNames.includes(tag)) tagNames.push(tag);

  const selectableIds = visible.map((note) => note.id);
  const allSelected = selectableIds.length > 0 && selectedIds.size === selectableIds.length;

  return (
    <div className="screen library">
      <header className="screen__header">
        <h1>Notes</h1>
        {visible.length > 0 && (
          <button
            type="button"
            className="screen__action"
            onClick={() => {
              if (selecting) exitSelecting();
              else setSelecting(true);
            }}
          >
            {selecting ? 'Cancel' : 'Select'}
          </button>
        )}
      </header>

      <form
        className="search-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
        }}
      >
        <label className="visually-hidden" htmlFor={inputId}>
          Search notes
        </label>
        <input
          id={inputId}
          className="search-input"
          type="search"
          value={query}
          placeholder="Search titles, tags, transcripts"
          autoComplete="off"
          aria-controls={listId}
          onChange={(event) => {
            setFilter({ q: event.target.value });
          }}
        />
      </form>

      <div className="chips" role="group" aria-label="Filter notes">
        <Chip
          label="All"
          pressed={view === 'active' && !tag}
          onClick={() => {
            setFilter({ view: null, tag: null });
          }}
        />
        {tagNames.map((name) => (
          <Chip
            key={name}
            label={name}
            pressed={tag === name}
            onClick={() => {
              setFilter({ tag: tag === name ? null : name });
            }}
          />
        ))}
        <Chip
          label={
            <>
              Archived
              {archivedCount !== undefined && (
                <>
                  {' · '}
                  <span className="numeric">
                    {archivedCount}
                    {archived.hasNextPage ? '+' : ''}
                  </span>
                </>
              )}
            </>
          }
          name={archivedCount === undefined ? 'Archived' : `Archived · ${String(archivedCount)}`}
          pressed={view === 'archived'}
          onClick={() => {
            setFilter({ view: view === 'archived' ? null : ARCHIVED_VIEW });
          }}
        />
      </div>

      {/*
        What the microphone produced, filed at the top of the library rather
        than floating over it: a recording stranded by a killed tab is offered
        back first, then whatever the pipeline is still working on.
      */}
      {!searching && view === 'active' && (
        <>
          <ResumePrompt />
          <FilingRow />
        </>
      )}

      {list.isLoading && !paused && !fromCache && (
        <p className="screen__count" role="status">
          Loading…
        </p>
      )}

      {showingCached && (
        <p className="screen__count" role="status">
          Saved on this device. Recordings and transcripts need a connection.
        </p>
      )}

      {searching && (
        <p className="screen__count" aria-live="polite">
          {`${String(hits.length)} ${hits.length === 1 ? 'result' : 'results'}${
            server.isFetching ? ' so far…' : ''
          }`}
        </p>
      )}

      {/*
        The server search did not run, or ran and failed. Said even when
        nothing matched — that is the one case where the user most needs to
        know a note they own was not actually looked for.
      */}
      {searching && serverUnavailable && (
        <p className="search-offline" role="status">
          {online
            ? 'The server search did not respond, so only notes on this device were searched.'
            : 'Searching offline — notes on this device only. Transcripts are not included.'}
        </p>
      )}

      {(!online || paused) && nothingToShow && !searching && (
        <p className="screen__empty" role="status">
          {view === 'archived'
            ? 'You are offline, so the archive could not be loaded.'
            : 'You are offline and no notes are cached on this device yet. They will appear when you reconnect.'}
        </p>
      )}

      {/*
        A real control, not an instruction for a gesture the app does not
        implement. The previous copy said "Pull down to try again." — there is
        no pull-to-refresh anywhere in the codebase.
      */}
      {list.isError && online && !paused && nothingToShow && (
        <div className="screen__empty" role="alert">
          <p>{failureMessage(list.error)}</p>
          <div className="screen__actions">
            <button
              type="button"
              className="screen__action"
              onClick={() => void list.refetch()}
              disabled={list.isFetching}
            >
              {list.isFetching ? 'Trying…' : 'Try again'}
            </button>
          </div>
        </div>
      )}

      {searching && nothingToShow && !server.isFetching && (
        <p className="screen__empty">
          Nothing matches &ldquo;{trimmed}&rdquo;
          {view === 'archived' ? ' in the archive.' : ' in the notes searched.'}
        </p>
      )}

      {!searching &&
        online &&
        !paused &&
        !list.isLoading &&
        !list.isError &&
        nothingToShow &&
        (view === 'archived' ? (
          <p className="screen__empty">Nothing is archived.</p>
        ) : tag ? (
          <p className="screen__empty">No notes are tagged &ldquo;{tag}&rdquo;.</p>
        ) : (
          <p className="screen__empty">Tap Record to make your first note.</p>
        ))}

      {searching ? (
        <ul id={listId} className="note-list" role="list">
          {hits.map((hit) => (
            <li key={hit.noteId}>
              <NoteRow
                note={noteForHit(hit, notes)}
                excerpt={hit.excerpt}
                highlight={trimmed}
                selectable={selecting}
                selected={selectedIds.has(hit.noteId)}
                onToggleSelect={toggleSelect}
              />
            </li>
          ))}
        </ul>
      ) : (
        <div id={listId} className="note-groups">
          {groups.map((group) => (
            <section key={group.label} className="note-group" aria-label={group.label}>
              <h2 className="note-group__label">{group.label}</h2>
              <ul className="note-list" role="list">
                {group.notes.map((note) => (
                  <li key={note.id}>
                    <NoteRow
                      note={note}
                      selectable={selecting}
                      selected={selectedIds.has(note.id)}
                      onToggleSelect={toggleSelect}
                    />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}

      {/*
        Cursor pagination is on every list endpoint by contract, so the library
        loads a page at a time rather than assuming the corpus is small.
      */}
      {!searching && list.hasNextPage && (
        <button
          type="button"
          className="load-more"
          onClick={() => void list.fetchNextPage()}
          disabled={list.isFetchingNextPage}
        >
          {list.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}

      {selecting && (
        <div className="bulk-bar" role="toolbar" aria-label="Bulk actions">
          <button
            type="button"
            className="screen__action"
            onClick={() => {
              setSelectedIds(allSelected ? new Set() : new Set(selectableIds));
            }}
          >
            {allSelected ? 'Deselect all' : 'Select all'}
          </button>
          <p className="screen__count" role="status">
            <span className="numeric">{selectedIds.size}</span> selected
          </p>
          {view === 'active' ? (
            <button
              type="button"
              className="screen__action"
              disabled={selectedIds.size === 0 || bulkBusy}
              onClick={() => {
                setConfirming('archive');
              }}
            >
              {bulkArchive.isPending ? 'Archiving…' : 'Archive'}
            </button>
          ) : (
            <button
              type="button"
              className="screen__action"
              disabled={selectedIds.size === 0 || bulkBusy}
              onClick={() => {
                setConfirming('restore');
              }}
            >
              {bulkRestore.isPending ? 'Restoring…' : 'Restore'}
            </button>
          )}
          {/*
            Delete without the detour through the archive. From the active list
            it is the same two server operations — archive, then purge — behind
            one typed confirmation, because "select all, delete" is what
            emptying a library actually looks like, and making it two screens
            did not make it safer, only slower. From the archive it is the
            batch purge alone.
          */}
          <button
            type="button"
            className="screen__action screen__action--destructive"
            disabled={selectedIds.size === 0 || bulkBusy}
            onClick={() => {
              setConfirming(view === 'active' ? 'delete' : 'purge');
            }}
          >
            {bulkDelete.isPending || bulkPurge.isPending ? 'Deleting…' : 'Delete forever'}
          </button>
        </div>
      )}

      <ConfirmDialog
        open={confirming === 'archive'}
        title={`Archive ${countLabel(selectedIds.size)}?`}
        body="They leave your notes and move to the archive, where you can restore them until they are deleted."
        confirmLabel="Archive them"
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          bulkArchive.mutate(Array.from(selectedIds), { onSuccess: exitSelecting });
        }}
      />

      <ConfirmDialog
        open={confirming === 'restore'}
        title={`Restore ${countLabel(selectedIds.size)}?`}
        body="They leave the archive and return to your notes."
        confirmLabel="Restore them"
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          bulkRestore.mutate(Array.from(selectedIds), { onSuccess: exitSelecting });
        }}
      />

      {/*
        Typing a fixed word rather than each note's own title — the single-note
        purge's gate — because requiring every selected title typed in one
        dialog does not scale past a couple of notes and would just get pasted
        anyway; this is still a deliberate second step, not a bare "OK".
      */}
      <ConfirmDialog
        open={confirming === 'delete' || confirming === 'purge'}
        title={`Delete ${countLabel(selectedIds.size)} forever?`}
        body="Their recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on."
        confirmLabel="Delete them forever"
        requireText="delete"
        requireLabel='Type "delete" to confirm'
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          const ids = Array.from(selectedIds);
          const mutation = confirming === 'purge' ? bulkPurge : bulkDelete;
          setConfirming(null);
          mutation.mutate(ids, { onSuccess: exitSelecting });
        }}
      />
    </div>
  );
}

function Chip({
  label,
  name,
  pressed,
  onClick,
}: {
  label: ReactNode;
  /** The accessible name, when the visible label is more than plain text. */
  name?: string;
  pressed: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="chip"
      aria-pressed={pressed}
      aria-label={name}
      onClick={onClick}
    >
      {label}
    </button>
  );
}

/**
 * The row to draw for a search hit. A local hit is the note itself; a hit only
 * the server returned — a transcript match on a note beyond the loaded pages —
 * has a title and an excerpt but no timestamp, so it renders undated rather
 * than being dropped.
 */
function noteForHit(hit: MergedHit, notes: readonly NoteWire[]): NoteWire {
  return (
    notes.find((note) => note.id === hit.noteId) ?? {
      id: hit.noteId,
      title: hit.title,
      snippet: hit.excerpt,
      updated_at: '',
      version: 0,
      archived: false,
    }
  );
}

function countLabel(count: number): string {
  return `${String(count)} ${count === 1 ? 'note' : 'notes'}`;
}

/**
 * The server's own wording where there is one, so a 401 reads as "sign in
 * again" rather than as a generic fault the user cannot act on.
 */
function failureMessage(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  return 'Your notes could not be loaded.';
}
