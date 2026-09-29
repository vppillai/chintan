import { Link } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import type { useNotes } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { LoadMore } from '@/components/LoadMore.tsx';
import { NoteRow } from '@/components/NoteRow.tsx';
import { PinnedGroup } from '@/features/notes/PinnedGroup.tsx';
import type { NoteGroup } from '@/features/notes/groups.ts';
import type { MergedHit } from '@/features/search/localSearch.ts';

import type { ChipCount } from './LibraryField.tsx';
import type { LibraryParams } from './useLibraryParams.ts';

interface LibraryListProps extends Pick<LibraryParams, 'view' | 'tag' | 'kind'> {
  /** The list's element id, which the field's `aria-controls` names. */
  id: string;
  /** The server's list query, for what it is doing and whether there is more. */
  list: ReturnType<typeof useNotes>;
  online: boolean;
  /** TanStack paused the query: the browser reports no connection. */
  paused: boolean;
  /** The rows are the device's copy, because the server has answered nothing at all. */
  fromCache: boolean;
  /** …and it is clear the server is not going to answer, so the copy is labelled. */
  showingCached: boolean;
  notes: readonly NoteWire[];
  pinned: readonly NoteWire[];
  groups: readonly NoteGroup[];
  /** The search: the trimmed term, and what matched it so far. */
  searching: boolean;
  trimmed: string;
  hits: readonly MergedHit[];
  /** The server's half of the search is still on its way. */
  serverPending: boolean;
  /** The server search did not run, or ran and failed. */
  serverUnavailable: boolean;
  loadMore: () => void;
  archived: ChipCount;
}

/**
 * The rows: pinned notes first, in the order the person dragged them into
 * (`PinnedGroup`, 2026-09-24 B), then the rest under the day they were last
 * touched — or, while searching, the hits in rank order. Above them the one
 * caption the moment calls for (loading, the device's copy, the result
 * count, the server search not answering) or the empty state; below them
 * the next page and the way into the archive.
 */
export function LibraryList({
  id,
  view,
  tag,
  kind,
  list,
  online,
  paused,
  fromCache,
  showingCached,
  notes,
  pinned,
  groups,
  searching,
  trimmed,
  hits,
  serverPending,
  serverUnavailable,
  loadMore,
  archived,
}: LibraryListProps) {
  const nothingToShow = searching
    ? hits.length === 0
    : pinned.length === 0 && groups.every((group) => group.notes.length === 0);

  return (
    <>
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
            serverPending ? ' so far…' : ''
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

      {searching && nothingToShow && !serverPending && (
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
        ) : kind ? (
          <p className="screen__empty">
            No checklists yet. Open a note and turn it into one from Details.
          </p>
        ) : (
          <p className="screen__empty">Tap Record to start your first note.</p>
        ))}

      {searching ? (
        <ul id={id} className="note-list" role="list">
          {hits.map((hit) => (
            <li key={hit.noteId}>
              <NoteRow
                note={noteForHit(hit, notes)}
                excerpt={hit.excerpt}
                highlight={trimmed}
              />
            </li>
          ))}
        </ul>
      ) : (
        <div id={id} className="note-groups">
          {pinned.length > 0 && (
            <PinnedGroup
              notes={pinned}
              // Under a tag or Checklists chip the group is a subset of the
              // pinned notes, and re-ranking a subset from 0 scrambles the rest.
              reorderable={!tag && !kind}
            />
          )}
          {groups.map((group) => (
            <section key={group.label} className="note-group" aria-label={group.label}>
              <h2 className="note-group__label">{group.label}</h2>
              <ul className="note-list" role="list">
                {group.notes.map((note) => (
                  <li key={note.id}>
                    <NoteRow note={note} />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}

      {/*
        Cursor pagination is on every list endpoint by contract, so the library
        loads a page at a time rather than assuming the corpus is small — and
        asks for the next one as the reader nears the end of this one, so the
        day groups run on without a button to press (backlog U3). The button
        is still there, for a keyboard and a screen reader.
      */}
      {!searching && (
        <LoadMore
          hasMore={list.hasNextPage}
          loading={list.isFetchingNextPage}
          onLoad={loadMore}
        />
      )}

      {/*
        The way into the archive from the active list, whatever its chip is
        doing — once there is a library to walk from or an archive to walk
        into. A first-run screen showed "Tap PTT to record your first note, or hold it and talk."
        and then "Archive · 0", the nothing T17 took out of the chips; the
        row hides a zero for the same reason the chip does (QA 2026-09-21,
        finding 13).
      */}
      {!searching &&
        view === 'active' &&
        (notes.length > 0 || (archived.count ?? 0) > 0) && (
          <Link to={ROUTES.archive} className="library-archive">
            <Icon name="archive" size={18} />
            <span>
              Archive
              {archived.count !== undefined && archived.count > 0 && (
                <>
                  {' · '}
                  <span className="numeric">
                    {archived.count}
                    {archived.more ? '+' : ''}
                  </span>
                </>
              )}
            </span>
            <Icon name="chevron-right" size={18} className="library-archive__glyph" />
          </Link>
      )}
    </>
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

/**
 * The server's own wording where there is one, so a 401 reads as "sign in
 * again" rather than as a generic fault the user cannot act on.
 */
function failureMessage(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  return 'Your notes could not be loaded.';
}
