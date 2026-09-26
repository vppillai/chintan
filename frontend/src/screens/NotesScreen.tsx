import { useQueryClient } from '@tanstack/react-query';
import { Suspense, lazy, useCallback, useId, useMemo } from 'react';

import { SERVER_SEARCH_DEBOUNCE_MS, useNotes, useSearch, useSearchCorpus } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { PullToRefresh } from '@/components/PullToRefresh.tsx';
import { Wordmark } from '@/components/Wordmark.tsx';
import { useAskThread } from '@/features/ask/useAskThread.ts';
import { PasskeyNudge } from '@/features/auth/PasskeyNudge.tsx';
import { FilingRow } from '@/features/capture/FilingRow.tsx';
import { ResumePrompt } from '@/features/capture/ResumePrompt.tsx';
import { groupByDay, splitPinned } from '@/features/notes/groups.ts';
import { mergeResults, rankLocal } from '@/features/search/localSearch.ts';
import { useDebouncedValue } from '@/hooks/useDebouncedValue.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

import { LibraryField } from './library/LibraryField.tsx';
import { LibraryList, noteForHit } from './library/LibraryList.tsx';
import { useLibraryParams } from './library/useLibraryParams.ts';
import { LibrarySelectionBar, useLibrarySelection } from './library/useLibrarySelection.tsx';

// The screen's parts live under `library/`; the tests keep importing these from here.
export { chipScrollBy } from './library/LibraryField.tsx';
export { HOLD_TO_DELETE_MS } from './library/useLibrarySelection.tsx';

/*
 * The Ask panel is a chunk of its own (round-3 T47): the thread, its markdown
 * and the save-as-note flow are for the one mode most launches never enter.
 * The hook that holds the thread stays here, because the field submits to it.
 */
const AskPanel = lazy(() =>
  import('@/features/ask/AskPanel.tsx').then((m) => ({ default: m.AskPanel })),
);

/**
 * The library. Home.
 *
 * One screen for the active notes, the archive and search, because they are
 * one list with three filters, not three destinations: a search field that
 * narrows the list as you type from the corpus already on the device, a row of
 * chips — All, Checklists, one per tag, Archived — and the rows grouped by day
 * beneath. All of it lives in the URL (`q`, `kind`, `tag`, `view`), so a filter
 * is shareable, survives reload and is what Back returns to
 * (`useLibraryParams`). Checklists is the one place every checklist can be
 * found from, which is what the owner asked for; it combines with a tag and
 * with the archive like any filter.
 *
 * The field has a second mode, Ask (`mode=ask`, backlog D5): the same box
 * takes a question instead of a filter, Enter sends it, and the Ask panel
 * stands in for the list with the answer and the notes it came from
 * (`LibraryField`). The mode is in the URL like the filters; the question and
 * the thread are not (`features/ask/thread.ts`).
 *
 * Bulk select carries over from the two screens this replaces
 * (`useLibrarySelection`). Selection starts from a row — press and hold it,
 * with a finger or a mouse alike, or pick Select from its ⋮ (see `NoteRow`).
 *
 * This component is the composition: it reads the filters, fetches the lists
 * and the search, and hands the rows to `LibraryList`. What it keeps for
 * itself is what the parts have to share — the rows in screen order, which
 * the selection ranges over.
 */
export function NotesScreen() {
  const params = useLibraryParams();
  const { view, tag, kind, asking, query } = params;
  const trimmed = query.trim();
  const searching = trimmed.length > 0;
  const askThread = useAskThread();

  const listId = useId();
  const askPanelId = useId();
  const online = useOnline();
  const queryClient = useQueryClient();

  /*
   * Pull down at the top to ask again. Everything the library shows is
   * invalidated — the lists (the tag chips are read from them), the filing
   * rows — because the gesture means "is this current?", not "reload one
   * query". The promise settles when the refetches do, which is when the
   * indicator lets go.
   */
  const refresh = useCallback(
    () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: ['notes'] }),
        queryClient.invalidateQueries({ queryKey: ['captures'] }),
      ]),
    [queryClient],
  );

  const list = useNotes({ state: view, ...(tag ? { tag } : {}), ...(kind ? { kind } : {}) });
  const cached = useCachedNotes(view, { prefetchBodies: true });
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = list;
  const loadMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);
  /*
   * The archive, fetched alongside the active list so the Archived chip can
   * carry its count. In the archived view this is the same query as `list`
   * and costs nothing extra.
   */
  const archived = useNotes({ state: 'archived' });
  // The checklists, for their chip's count, on the same terms as the archive.
  const checklists = useNotes({ state: 'active', kind: 'checklist' });
  /*
   * The tag chips are the tags on the active notes the device holds
   * (round-3 T46): every page the user has seen plus the search corpus,
   * which carries tags in its light projection. A Home mount used to fan out
   * five GETs and five preflights, one of them `GET /v1/tags` for the chip
   * names alone; the tag editor on the note screen still asks the server.
   */
  const activeCache = useCachedNotes('active');
  /*
   * The search corpus — every active note with its searchable body — fetched
   * once and written to the device, so the instant search below can find a
   * sentence from a transcript rather than only a note's first line. Read
   * back through `cached`; this only fills it.
   */
  useSearchCorpus(online);

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
   * archived on another device. The cache knows nothing of tags or kinds, so
   * those filters are applied here by hand.
   */
  const serverNotes = useMemo(
    () => list.data?.pages.flatMap((page) => page.items),
    [list.data],
  );
  const notes: NoteWire[] = useMemo(
    () =>
      serverNotes ??
      cached.data?.filter(
        (note) =>
          (!tag || (note.tags ?? []).includes(tag)) && (!kind || (note.kind ?? 'note') === kind),
      ) ??
      [],
    [serverNotes, cached.data, tag, kind],
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
   *
   * Ranked over the server's rows enriched with what the device holds for
   * them: the corpus row's `search_text`, or a full note's body. The list
   * itself never carries either — it is fetched constantly and renders none
   * of it — so without this the instant search would see only snippets while
   * online, and only offline would it match what the server matches.
   */
  const searchable = useMemo(() => {
    const device = new Map((cached.data ?? []).map((note) => [note.id, note]));
    return notes.map((note) => {
      const held = device.get(note.id);
      // The server's row wins every field it has; the device supplies the text.
      return held ? { ...held, ...note } : note;
    });
  }, [notes, cached.data]);
  const local = useMemo(() => rankLocal(searchable, trimmed), [searchable, trimmed]);
  /*
   * The server is asked for the word once the typing pauses, not for every
   * letter on the way to it: a keystroke's worth of results is already on
   * screen from the corpus above, and eight requests for "flashing" answered
   * out of order is what a per-keystroke query produced.
   */
  const settled = useDebouncedValue(trimmed, SERVER_SEARCH_DEBOUNCE_MS);
  const server = useSearch(settled, { enabled: online && view === 'active' });
  const hits = useMemo(
    () => mergeResults(local, server.data?.items ?? []),
    [local, server.data],
  );
  const serverUnavailable = view === 'active' && (!online || server.isError);
  // Still waiting on the server: either the debounce has not let the word
  // through yet, or the request is in flight.
  const serverPending =
    searching && view === 'active' && online && (settled !== trimmed || server.isFetching);

  const { pinned, rest } = useMemo(() => splitPinned(notes), [notes]);
  const groups = useMemo(() => groupByDay(rest), [rest]);
  // In the order they are on screen, so Shift-click ranges over what the eye sees.
  const visible: NoteWire[] = useMemo(
    () =>
      searching
        ? hits.map((hit) => noteForHit(hit, notes))
        : [...pinned, ...groups.flatMap((group) => group.notes)],
    [searching, hits, notes, pinned, groups],
  );
  const selection = useLibrarySelection(visible);

  const archivedCount = archived.data?.pages.reduce((sum, page) => sum + page.items.length, 0);
  const checklistCount = checklists.data?.pages.reduce(
    (sum, page) => sum + page.items.length,
    0,
  );
  const tagNames = useMemo(() => {
    // The server's unfiltered page joins the device's copy, so the chips are
    // there on a first visit before the corpus has been written.
    const unfiltered = view === 'active' && !tag && !kind ? (serverNotes ?? []) : [];
    const names = new Set([...(activeCache.data ?? []), ...unfiltered].flatMap((note) => note.tags ?? []));
    // A tag from the URL that nothing on the device carries still gets its
    // chip, or the filter would be applied with nothing on screen saying so.
    if (tag) names.add(tag);
    return [...names].sort((a, b) => a.localeCompare(b));
  }, [activeCache.data, serverNotes, view, tag, kind]);

  // Known once something — the server or the device — has answered.
  const count = serverNotes !== undefined || fromCache ? notes.length : undefined;

  return (
    <div className="screen library" data-selecting={selection.selecting || undefined}>
      <PullToRefresh onRefresh={refresh} />

      {/*
        One row: the brand leads, "Notes · 12" is its quiet right end. The
        shell's banner used to say the name above, the h1 said "Notes" and
        the tab said Home — the same place named three times in the top 45
        percent of a phone (round-3 T17) — so on this screen the banner holds
        nothing and the wordmark is here, in the same lockup the banner wears
        elsewhere. The h1 stays first in the DOM and the brand is moved before
        it by `order` (home.css), so a screen reader still hears "Notes, 12"
        and the a11y sweep and the route tests still find a heading that
        starts with "Notes"; the visual emphasis is the CSS's. The day is
        gone from the row (owner feedback 2026-09-26): the group label under
        it already says Today. The count is what has been loaded, with "+"
        while there is more. The archive is headed "Archived": under "Notes"
        its only label was a pressed chip at the far end of a row scrolled to
        its start, off a phone's screen, and with notes in it the view was
        indistinguishable from Home (QA 2026-09-21, finding 6).
      */}
      <header className="screen__header library-header">
        {/* Focusable so a control that leaves the page — a receipt's × — can hand focus here. */}
        <h1 className="library-heading" tabIndex={-1}>
          <span className="library-heading__title">
            <span>{view === 'archived' ? 'Archived' : 'Notes'}</span>
            {count !== undefined && (
              <>
                <span aria-hidden="true" className="library-heading__separator">
                  ·
                </span>
                <span className="library-heading__count numeric">
                  {count}
                  {list.hasNextPage ? '+' : ''}
                </span>
              </>
            )}
          </span>
        </h1>
        <p className="library-brand">
          <Wordmark />
        </p>
      </header>

      <LibraryField
        {...params}
        thread={askThread}
        askPanelId={askPanelId}
        listId={listId}
        tagNames={tagNames}
        checklists={{ count: checklistCount, more: checklists.hasNextPage }}
        archived={{ count: archivedCount, more: archived.hasNextPage }}
      />

      {asking && (
        <Suspense fallback={null}>
          <AskPanel id={askPanelId} thread={askThread} />
        </Suspense>
      )}

      {/*
        What the microphone produced, filed at the top of the library rather
        than floating over it: a recording stranded by a killed tab is offered
        back first, then whatever the pipeline is still working on.
      */}
      {!asking && !searching && view === 'active' && (
        <>
          {/* Only once there is a note (round-3 T56): the first screen is about the first recording. */}
          {notes.length > 0 && <PasskeyNudge />}
          <ResumePrompt />
          <FilingRow />
        </>
      )}

      {!asking && (
        <LibraryList
          id={listId}
          view={view}
          tag={tag}
          kind={kind}
          list={list}
          online={online}
          paused={paused}
          fromCache={fromCache}
          showingCached={showingCached}
          notes={notes}
          pinned={pinned}
          groups={groups}
          searching={searching}
          trimmed={trimmed}
          hits={hits}
          serverPending={serverPending}
          serverUnavailable={serverUnavailable}
          selection={selection}
          loadMore={loadMore}
          archived={{ count: archivedCount, more: archived.hasNextPage }}
        />
      )}

      <LibrarySelectionBar selection={selection} view={view} asking={asking} />
    </div>
  );
}
