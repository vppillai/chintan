import { useQueryClient } from '@tanstack/react-query';
import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useReducer,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react';
import { flushSync } from 'react-dom';
import { useLocation, useParams } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import {
  CAPTURE_POLL_FAST_MS,
  CAPTURE_POLL_FAST_WINDOW_MS,
  CAPTURE_POLL_INTERVAL_MS,
  CAPTURE_POLL_SLOW_MS,
  queryKeys,
  useArchiveNote,
  useDeleteNoteForever,
  useInFlightCaptures,
  useNote,
  usePollNote,
  useSettings,
} from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { useTabNavigation } from '@/app/useTabNavigation.ts';
import { Icon } from '@/components/Icon.tsx';
import { PullToRefresh } from '@/components/PullToRefresh.tsx';
import { announce } from '@/components/StatusRegion.tsx';
import { FilingBanner } from '@/features/capture/FilingBanner.tsx';
import { useLocalUpload } from '@/features/capture/FilingRow.tsx';
import type { CaptureModel } from '@/features/capture/machine.ts';
import { languageName } from '@/features/settings/languages.ts';
import { useHorizontalSwipe } from '@/hooks/useHorizontalSwipe.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNote } from '@/offline/useNotesCache.ts';

import { CleanedPanel } from './CleanedPanel.tsx';
import { FindBar, type FindTarget } from './FindBar.tsx';
import { NoteMenu } from './NoteActions.tsx';
import { isUntouchedPlaceholder } from './newNote.ts';
import { noteLanguageFieldId, notePanelHeadingId, type NotePanelKind } from './notePanel.ts';
import {
  NoteTabList,
  noteTabId,
  noteTabPanelId,
  useNoteTab,
  type NoteTab,
  type NoteTabDescriptor,
} from './NoteTabs.tsx';
import { Recordings } from './Recordings.tsx';
import { SAVE_LABELS, type SaveState } from './autosave.ts';
import { FIND_CLOSED, findReducer, type FindState, type FindAction } from './find.ts';
import { describeRecordings, formatRowTime } from './groups.ts';
import { describeProgress, parseChecklist, progressOf } from './checklist.ts';
import { describePurge, purgeCountdown } from './purge.ts';
import { TextPanel, type Flash } from './TextPanel.tsx';
import { useNoteEditor, type NoteEditor } from './useNoteEditor.ts';

/*
 * The Details/Share drawer is its own chunk: a launch never opens it, and
 * with the capture screen in the main chunk the drawer's tag, alias and
 * share editors are what made room for it (`app/router.tsx`). It is fetched
 * when a note opens, which the precache makes free after the first visit.
 */
const NoteDrawer = lazy(() => import('./NoteDrawer.tsx').then((m) => ({ default: m.NoteDrawer })));

/**
 * A note.
 *
 * Top to bottom: the way back with Find and the note's ⋮ menu, the title, one
 * line of metadata, then a strip of segments — Text · Cleaned · Recordings (N),
 * or Items · Recordings (N) for a checklist — and the one panel it selects. The strip sticks under the banner while the
 * panel scrolls, so the recordings are one tap away from anywhere in a long
 * note rather than a screen or five below its last paragraph, which is where
 * they sat when body and recordings were one page. The text is the document;
 * the cleaned view is the worker's rewrite of the whole of it; the recordings
 * are its sources.
 *
 * No action bar stands between the strip and the tab bar: the one that did
 * (Details · Share · Archive · Record into this) took a third of a phone with
 * the header and the meta, and its Record sat 30 px above the tab bar's mic.
 * The mic records into this note while it is open, the actions are in the
 * header's ⋮ menu (review 2026-09-21, T6), and Details and Share open as a
 * sheet at the foot (`NoteDrawer`), where the conflict banner also stands.
 */
export function NoteDetailScreen() {
  const { id } = useParams<{ id: string }>();
  const online = useOnline();
  const { data: served, isLoading, fetchStatus, error, dataUpdatedAt } = useNote(id);
  /*
   * A note with a recording still moving through the pipeline is about to
   * change under the reader: while any capture is non-terminal each is asked
   * after on the filing cadence, and the note is read again when one settles.
   * Here and not in `useNote`, which the tab bar reads too.
   */
  useInFlightCaptures(id, served?.captures, dataUpdatedAt);
  const cached = useCachedNote(id);

  /*
   * The device's copy stands in when the server has not answered. Only a full
   * note qualifies — `useCachedNote` refuses a list row — because rendering a
   * real title over an empty body invites the user to type into a note whose
   * text is merely missing, and the next PATCH would erase it.
   */
  const note = served ?? cached.data ?? undefined;
  const offlineCopy = !served && Boolean(cached.data);
  const editor = useNoteEditor(note);

  /*
   * A note just made from Home's + arrives with `focusTitle` in its route
   * state and a placeholder title: the field takes focus with the title
   * selected, so the first keystroke replaces it. Once per arrival — the
   * state stays on the history entry, and a note read again after Back
   * must not steal focus from wherever `useRouteFocus` put it. Keyed on the
   * draft's title, not the note: the editor adopts the note a render later,
   * and a selection made over an empty field is lost when the value lands.
   */
  const location = useLocation();
  const focusTitle = (location.state as { focusTitle?: boolean } | null)?.focusTitle === true;
  const titleRef = useRef<HTMLInputElement>(null);
  const titleFocused = useRef(false);
  const titleReady = editor.model.draft.title.length > 0;
  useEffect(() => {
    if (!focusTitle || !titleReady || titleFocused.current) return;
    titleFocused.current = true;
    titleRef.current?.focus();
    titleRef.current?.select();
  }, [focusTitle, titleReady]);
  /*
   * A placeholder left as it was made — the title untyped, no body, no
   * recording — is discarded when the screen is left (Back, the Home tab,
   * a tap on another note): it was never a note, and a library of "New
   * note" rows is the owner's mistake kept for them. The existing delete
   * path, without its confirm or toast: the archive, then the purge, since
   * the server purges only from the archive. Judged from the draft at the
   * moment of leaving, so a title typed but not yet saved counts as typed;
   * the hooks' own `onSuccess` take the row out of the lists and the
   * device's copy after this component has gone.
   */
  const archiveNote = useArchiveNote();
  const deleteForever = useDeleteNoteForever();
  const discardOnLeave = useRef<string | null>(null);
  // Every render, in an effect: a ref is not read or written while rendering.
  useEffect(() => {
    discardOnLeave.current =
      focusTitle && note && isUntouchedPlaceholder({ ...editor.model.draft, captures: note.captures })
        ? note.id
        : null;
  });
  useEffect(
    () => () => {
      const id = discardOnLeave.current;
      if (!id) return;
      void archiveNote.mutateAsync(id).then(() => deleteForever.mutate(id), () => undefined);
    },
    // Once, at unmount; the refs carry the latest answer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  // The draft's, not the server's: the Details switch changes what this
  // screen is — Items for Text, and no Cleaned tab — the moment it is
  // flipped, not after the save lands.
  const checklist = (editor.model.draft.kind ?? note?.kind ?? 'note') === 'checklist';
  const [selectingRecordings, setSelectingRecordings] = useState(false);
  // The language the note's text is in, for `lang` on everything that
  // renders it — announced, spell-checked and hyphenated as that language
  // rather than as the document's English (review 2026-09-21, T60).
  const { data: settings } = useSettings();
  const lang = contentLanguage(editor.model.draft.language ?? '', settings?.default_language);

  /*
   * Which of the menu's disclosures is open. Held here rather than in the
   * menu because the meta line opens one of them: "· Malayalam" up by the
   * title is a fact about the note, and tapping a fact should go to where it
   * is set.
   */
  const [panel, setPanel] = useState<NotePanelKind | null>(null);
  /*
   * Opening takes the focus with it: the menuitem that asked has just
   * unmounted and the drawer is at the far end of the screen. Rendered
   * synchronously so the target exists — and so that the sheet's
   * `useModalFocus` mount effect, which focuses its first control, has
   * already run: `flushSync` flushes a sync render's passive effects before
   * it returns, so the focus below is the last word. Details lands on the
   * language select — the control the meta line's fact came from — and
   * Share on its heading, with its controls a Tab away.
   */
  const openPanel = useCallback(
    (kind: NotePanelKind) => {
      if (!note) return;
      flushSync(() => {
        setPanel(kind);
      });
      const target =
        kind === 'details' ? noteLanguageFieldId(note.id) : notePanelHeadingId(note.id);
      // The drawer is a lazy chunk whose import starts with this screen's
      // first render; a ⋮ opened inside that window finds no element and
      // focus stays on the menu's trigger, which is the fallback either way.
      document.getElementById(target)?.focus();
    },
    [note],
  );
  const openDetails = useCallback(() => {
    openPanel('details');
  }, [openPanel]);
  // Closed from its X, the drawer hands focus back to the ⋮ it belongs to
  // rather than dropping it on <body>.
  const menuRef = useRef<HTMLButtonElement>(null);
  const changePanel = useCallback((next: NotePanelKind | null) => {
    setPanel(next);
    if (next === null) menuRef.current?.focus();
  }, []);

  // Find in this note: the state lives with the screen so the header's toggle
  // and the bar under the strip — different branches of the tree — share it.
  const [find, dispatchFind] = useReducer(findReducer, FIND_CLOSED);
  const findBarId = useId();
  const findInputRef = useRef<HTMLInputElement>(null);
  const noteOpen = Boolean(note);

  /*
   * Ctrl/⌘+F opens the bar instead of the browser's find, which cannot see
   * into a textarea's text as marks and knows nothing of the tabs. Only while
   * a note is on screen; on a phone there is no such key to press.
   */
  useEffect(() => {
    if (!noteOpen) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return;
      if (event.key !== 'f' && event.key !== 'F') return;
      event.preventDefault();
      dispatchFind({ type: 'open' });
      // Already open: back to the bar, with the query selected to be replaced.
      findInputRef.current?.focus();
      findInputRef.current?.select();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [noteOpen]);

  /*
   * A recording this device is still sending into this note. Send returns
   * here rather than to the library, so the upload's row — the same one the
   * library shows — is the first recording until the server's row takes
   * over; this hook does that hand-over while the screen is the one mounted.
   */
  const localUpload = useLocalUpload(note?.captures ?? [], id ?? '');

  // Pull down at the top to re-read this note — the one thing on this screen
  // that another device, or the pipeline appending a recording, can change.
  const queryClient = useQueryClient();
  const refresh = useCallback(
    () => queryClient.invalidateQueries({ queryKey: queryKeys.note(id ?? '') }),
    [queryClient, id],
  );

  // Paused means offline, not slow: TanStack never runs the query at all, so
  // waiting for it would be waiting forever.
  const paused = fetchStatus === 'paused';

  /*
   * "Loading…" is said only while an answer can still be expected: the
   * browser reports a connection, the query is running, and the device has
   * no copy to show instead. With no connection there is nothing to wait for
   * and the offline sentence is shown at once. And the wait is bounded: a
   * request that hangs rather than failing — the browser insisting it is
   * online on a dead link — sat on "Loading…" for the client's whole retry
   * budget, sixteen seconds and counting in the QA pass (D17), with no way
   * out. After `LOADING_PATIENCE_MS` the screen says what it knows and offers
   * Try again.
   */
  const waiting = (isLoading || cached.isLoading) && !paused && online && !note;
  const patienceOver = useTimedOut(waiting, LOADING_PATIENCE_MS);

  /*
   * Three different sentences below, because they are three different
   * situations and the screen used to say one of them for all. A note that
   * is simply not on this device was reported as one that "may have been
   * archived or purged" — describing a deletion that never happened, to a
   * user who could see the note one screen earlier.
   */
  const offline = paused || !online || (error instanceof ApiError && error.isOffline);
  const unanswered = !offline && (patienceOver || (error instanceof ApiError && error.isRetryable));
  /*
   * No verdict is not a verdict. A notification tap wakes a phone whose
   * radio is not up yet, and the first request fails or hangs before it
   * reaches the server — which has had the note since before the push was
   * sent. The query does not retry on its own and the browser, which never
   * said "offline", fires no reconnect; so the screen stood on "Not on this
   * device" over a note one more request would have shown. While the server
   * has answered nothing final and the browser still reports a connection,
   * the note is asked for again on the filing ladder (`noteRetryInterval`);
   * a refetch during a request still in the air joins it, so this never
   * doubles a slow one. A real 404 is a verdict and is not retried.
   */
  const reasking =
    !note &&
    online &&
    !paused &&
    (unanswered || (error instanceof ApiError && error.isOffline));
  usePollNote(id ?? '', useSince(reasking), noteRetryInterval);

  if (waiting && !patienceOver) {
    return (
      <div className="screen">
        <p className="screen__empty" role="status">
          Loading…
        </p>
      </div>
    );
  }

  if (!note) {
    return (
      <div className="screen">
        <header className="screen__header screen__header--detail">
          <BackLink />
          <h1>{offline || unanswered ? 'Not on this device' : 'Note not found'}</h1>
        </header>
        <p className="screen__empty" role="status">
          {offline
            ? 'You’re offline and this note isn’t saved on this device. It will be here once you have a connection.'
            : unanswered
              ? 'This note isn’t saved on this device, and the server hasn’t answered yet. Trying again…'
              : 'No note with that identifier. It may have been archived or purged.'}
        </p>
        {unanswered && (
          <div className="screen__actions">
            <button
              type="button"
              className="screen__action"
              onClick={() => void refresh()}
              disabled={fetchStatus === 'fetching' && !patienceOver}
            >
              Try again
            </button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="screen note-screen">
      <PullToRefresh onRefresh={refresh} />

      <header className="screen__header screen__header--detail">
        <BackLink />
        <button
          type="button"
          className="note-find-toggle"
          aria-label="Find in note"
          aria-expanded={find.open}
          aria-controls={findBarId}
          onClick={() => {
            dispatchFind({ type: 'toggle' });
          }}
        >
          <Icon name="search" size={20} />
        </button>
        <NoteMenu note={note} triggerRef={menuRef} onOpenPanel={openPanel} />
      </header>

      {offlineCopy && (
        <p className="screen__count" role="status">
          Saved on this device. Edits are kept here and sent when you reconnect.
        </p>
      )}

      {/*
        The screen's heading is the title field's label. The title itself is an
        input, so without this the note screen had no h1 at all (QA D14: axe
        `page-has-heading-one`); the label names the field for the input and
        heads the page for a screen reader, hidden from sight in both roles.
      */}
      <h1 className="visually-hidden">
        <label htmlFor="note-title">Note title</label>
      </h1>
      <input
        ref={titleRef}
        id="note-title"
        className="note-title-input"
        value={editor.model.draft.title}
        onChange={(event) => {
          editor.edit({ title: event.target.value });
        }}
        onBlur={() => void editor.saveNow()}
      />

      <NoteMeta
        note={note}
        tags={editor.model.draft.tags}
        body={editor.model.draft.body}
        checklist={checklist}
        language={editor.model.draft.language ?? ''}
        onOpenDetails={openDetails}
      />

      {note.archived && (
        <p className="note-actions__state" role="status">
          This note is archived. {describePurge(purgeCountdown(note.purge_after))}.
        </p>
      )}

      <SaveIndicator editor={editor} />

      {/*
        Keyed by the note: the tab strip's memory and the panels' own state
        belong to one note, and "Open <title>" after a move walks from one
        note to another without remounting this screen. The find bar's state
        is not keyed: a query carried into the next note is a query the user
        may well want there too, and the panel clamps the active match.
      */}
      <NoteViews
        key={note.id}
        note={note}
        editor={editor}
        checklist={checklist}
        lang={lang}
        localUpload={localUpload}
        onSelectingRecordings={setSelectingRecordings}
        find={find}
        dispatchFind={dispatchFind}
        findBarId={findBarId}
        findInputRef={findInputRef}
      />

      {/*
        While recordings are being selected their own bar takes the foot of
        the screen, so an open Details or Share drawer steps aside rather than
        stacking under it. Hidden, not unmounted: it is still there when the
        selection ends. The drawer is outside the panels, so it is there on
        every tab.
      */}
      <Suspense fallback={null}>
        <NoteDrawer
          note={note}
          editor={editor}
          hidden={selectingRecordings}
          // A conflict is resolved before anything else, and at a laptop height
          // the open Details panel covered the banner's two buttons. The panel
          // steps aside while the banner is up and is back as it was after.
          open={editor.model.state === 'conflict' ? null : panel}
          onOpenChange={changePanel}
        />
      </Suspense>
    </div>
  );
}

/**
 * The segmented strip and the panel it selects. One panel is mounted at a
 * time — the recordings' players and the body's autosize measurement each
 * belong to the panel that is on screen, and a hidden panel's `<audio>`
 * would otherwise keep playing under the text.
 */
function NoteViews({
  note,
  editor,
  checklist,
  lang,
  localUpload,
  onSelectingRecordings,
  find,
  dispatchFind,
  findBarId,
  findInputRef,
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  checklist: boolean;
  /** The text's language tag, or none under Auto-detect. */
  lang: string | undefined;
  localUpload: CaptureModel | null;
  onSelectingRecordings: (selecting: boolean) => void;
  find: FindState;
  dispatchFind: (action: FindAction) => void;
  findBarId: string;
  findInputRef: RefObject<HTMLInputElement | null>;
}) {
  const [chosenTab, selectTab] = useNoteTab(note.id);
  // A checklist has no Cleaned tab (R8-F8: Tidy up list in the ⋮ writes the
  // body instead), so a remembered or linked `cleaned` lands on its Items.
  const tab: NoteTab = checklist && chosenTab === 'cleaned' ? 'text' : chosenTab;
  // The upload on its way counts: it is a row on the tab already.
  const count = (note.captures?.length ?? 0) + (localUpload ? 1 : 0);
  const tabs: NoteTabDescriptor[] = checklist
    ? [
        { id: 'text', label: 'Items' },
        { id: 'recordings', label: 'Recordings', count },
      ]
    : [
        { id: 'text', label: 'Text' },
        { id: 'cleaned', label: 'Cleaned' },
        { id: 'recordings', label: 'Recordings', count },
      ];

  // Another tab is another text: the count is the new panel's to report, and
  // the active match starts again from its first.
  const setTab = (next: NoteTab): void => {
    selectTab(next);
    dispatchFind({ type: 'rewind' });
  };

  /*
   * A swipe across the strip and the panel steps to the neighbouring tab,
   * through the same `setTab` as a tap: Find rewinds, the URL and the
   * session memory follow, focus stays where it was (`useRouteFocus` keys on
   * the pathname). The tab buttons remain the keyboard and screen-reader
   * path; nothing is announced for a finger that can see the strip move.
   */
  const viewsRef = useRef<HTMLDivElement>(null);
  const tabIndex = tabs.findIndex((entry) => entry.id === tab);
  const swipe = useHorizontalSwipe({
    ref: viewsRef,
    at: tabIndex,
    canGo: (direction) => (direction === 'left' ? tabIndex < tabs.length - 1 : tabIndex > 0),
    onSwipe: (direction) => {
      const next = tabs[tabIndex + (direction === 'left' ? 1 : -1)];
      if (next) setTab(next.id);
    },
  });

  /*
   * A new tab starts at its top. The scroll offset is the page's, not the
   * panel's, so from deep in Text a step used to land mid-Cleaned, or at its
   * foot. When the strip is stuck — the region's top is above the scroll
   * region's — the page is moved so the new panel starts just under it; with
   * the note's head still on screen nothing moves. A layout effect, so it
   * lands before paint and before the shell's `useScrollRestore` (a parent,
   * whose layout effect runs after this one) reads the offset for the new
   * history entry. Every tab change, by a swipe, a tap or a key.
   */
  const shownTab = useRef(tab);
  useLayoutEffect(() => {
    if (shownTab.current === tab) return;
    shownTab.current = tab;
    const views = viewsRef.current;
    const main = views?.closest('.app__main');
    if (!views || !main) return;
    const above = views.getBoundingClientRect().top - main.getBoundingClientRect().top;
    if (above < 0) main.scrollTop += above;
  }, [tab]);

  /*
   * What the open panel is asked to find. Nothing, unless the bar is open with
   * a query: the Text panel shows its textarea until there is something to
   * mark. `onTotal` is the dispatch, so it is the same function every render
   * and the panels' report effect runs only when their count changes.
   */
  const onTotal = useCallback(
    (total: number) => {
      dispatchFind({ type: 'total', total });
    },
    [dispatchFind],
  );
  /*
   * What the banner's Show asked to be pointed at: the note's body from
   * before the recording, from which the Text panel works out what it added.
   * `n` makes a second Show of the same landing a new flash.
   */
  const [flash, setFlash] = useState<Flash | null>(null);
  const clearFlash = useCallback(() => {
    setFlash(null);
  }, []);

  const searchable = tab !== 'recordings';
  const target: FindTarget | null =
    find.open && find.query !== '' && searchable
      ? { query: find.query, active: find.active, onTotal }
      : null;

  return (
    <>
      {/*
        A recording still on its way into this note, above the strip: Send
        returns here rather than to the Recordings tab, so the progress has to
        be visible from the text (owner feedback 2026-09-24). Not on the
        Recordings tab, where the row itself wears the same strip and the
        same Retry — two of them a hundred pixels apart said nothing twice.
      */}
      {tab !== 'recordings' && (
        <FilingBanner
          note={note}
          localUpload={localUpload}
          checklist={checklist}
          onShow={(before) => {
            // The addition is drawn on the text; from Cleaned, go there.
            if (tab !== 'text') setTab('text');
            setFlash((was) => ({ before, n: (was?.n ?? 0) + 1 }));
          }}
        />
      )}
      {/* Always mounted, so the sentence is announced when it appears. */}
      <p className="visually-hidden" role="status" aria-live="polite">
        {flash ? 'Added text shown' : ''}
      </p>
      {/* The hook moves the panel and the strip's pill itself; only
          `dragging` comes through React, so a move re-renders nothing. */}
      <div
        ref={viewsRef}
        className="note-views"
        data-swiping={swipe.dragging || undefined}
        {...swipe.handlers}
      >
        <div className="note-strip">
          <NoteTabList noteId={note.id} tabs={tabs} value={tab} onChange={setTab} />
          {find.open && (
            <FindBar
              id={findBarId}
              query={find.query}
              active={find.active}
              total={find.total}
              inputRef={findInputRef}
              disabled={!searchable}
              hint={searchable ? undefined : 'Search works in Text and Cleaned.'}
              onQueryChange={(query) => {
                dispatchFind({ type: 'query', query });
              }}
              onNext={() => {
                dispatchFind({ type: 'next' });
              }}
              onPrevious={() => {
                dispatchFind({ type: 'previous' });
              }}
              onClose={() => {
                dispatchFind({ type: 'close' });
              }}
            />
          )}
        </div>
        <NotePanel noteId={note.id} tab={tab}>
          {tab === 'text' ? (
            <TextPanel
              noteId={note.id}
              editor={editor}
              checklist={checklist}
              lang={lang}
              find={target}
              onDismissFind={() => {
                dispatchFind({ type: 'close' });
              }}
              flash={flash}
              onFlashDone={clearFlash}
            />
          ) : tab === 'cleaned' ? (
            <CleanedPanel note={note} editor={editor} lang={lang} find={target} />
          ) : (
            <Recordings
              note={note}
              lang={lang}
              localUpload={localUpload}
              onSelectingChange={onSelectingRecordings}
            />
          )}
        </NotePanel>
      </div>
    </>
  );
}

function NotePanel({
  noteId,
  tab,
  children,
}: {
  noteId: string;
  tab: NoteTab;
  children: ReactNode;
}) {
  return (
    <div
      role="tabpanel"
      id={noteTabPanelId(noteId, tab)}
      aria-labelledby={noteTabId(noteId, tab)}
      className="note-tabpanel"
      data-swipe-panel
    >
      {children}
    </div>
  );
}

/** How long "Loading…" is allowed to stand before the screen says what it knows. */
export const LOADING_PATIENCE_MS = 6_000;

/**
 * How often a note the server has not answered for is asked for again: the
 * filing poll's own ladder, since a note just announced by a push is the
 * common case — brisk while the answer is most likely a second away, then
 * every few seconds, then every fifteen for as long as the screen stays on
 * it. Module-level, as `usePollNote` requires; `elapsedMs` is since the
 * screen first found itself without an answer.
 */
export function noteRetryInterval(elapsedMs: number): number {
  if (elapsedMs < CAPTURE_POLL_FAST_WINDOW_MS) return CAPTURE_POLL_FAST_MS;
  if (elapsedMs < NOTE_RETRY_BRISK_MS) return CAPTURE_POLL_INTERVAL_MS;
  return CAPTURE_POLL_SLOW_MS;
}

/** Brisk re-asks for this long since the first unanswered read, then the slow cadence. */
const NOTE_RETRY_BRISK_MS = 2 * 60 * 1000;

/**
 * When `active` last became true, or null while it is false — the clock a
 * ladder of re-asks is read against. Stamped from a timer, as `useTimedOut`
 * is, so the render stays pure; the one tick it costs is nothing against the
 * ladder's first rung.
 */
function useSince(active: boolean): number | null {
  const [since, setSince] = useState<number | null>(null);

  useEffect(() => {
    if (!active) return;
    const timer = setTimeout(() => {
      setSince(Date.now());
    }, 0);
    return () => {
      clearTimeout(timer);
      setSince(null);
    };
  }, [active]);

  return since;
}

/**
 * True once `active` has been continuously true for `ms`. Falls back to false
 * the moment `active` does, so a wait that ends is forgotten and the next one
 * starts its own clock.
 */
function useTimedOut(active: boolean, ms: number): boolean {
  const [timedOut, setTimedOut] = useState(false);

  useEffect(() => {
    if (!active) return;
    const timer = setTimeout(() => {
      setTimedOut(true);
    }, ms);
    return () => {
      clearTimeout(timer);
      // The wait this clock measured is over; the next one starts at zero.
      setTimedOut(false);
    };
  }, [active, ms]);

  return active && timedOut;
}

/**
 * "5 Sept · house · 3 rec · 0:17 · Malayalam" — one line, always.
 *
 * The tags are the draft's, not the server's, so adding a tag in Details
 * shows up here at once rather than after the save lands. For a checklist the
 * line also says "3 of 7 done", because how much of a list is left is what
 * someone looks up here. It used to begin "Updated today 14:02" and end with
 * the word count, and wrapped to two lines on a phone (40 px, review
 * 2026-09-21 T6); the time is the row's own short form and the count is gone
 * — a body's length is visible in the body.
 *
 * The language is the last fact, and only when it is worth a word: the
 * note's own choice when it differs from the default, or the default itself
 * when that is not English. An English note under an English default says
 * nothing — the common case should not carry a label. It is a button because
 * the control that sets it is in the Details drawer, and the owner's trial
 * found it there by accident or not at all.
 */
function NoteMeta({
  note,
  tags,
  body,
  checklist,
  language,
  onOpenDetails,
}: {
  note: NoteDetailWire;
  tags: readonly string[];
  body: string;
  checklist: boolean;
  /** The draft's `language`: a code, `auto`, or the empty string to inherit. */
  language: string;
  onOpenDetails: () => void;
}) {
  const { data: settings } = useSettings();
  const parts = [
    formatRowTime(note.updated_at),
    ...tags,
    describeRecordings(note, { short: true }),
    checklist ? describeProgress(progressOf(parseChecklist(body))) : null,
  ].filter((part): part is string => Boolean(part));

  const effective = effectiveLanguage(language, settings?.default_language);

  // A real " · " between the facts, not a CSS pseudo-element: a screen reader
  // reads text, and "housereading list3 recordings" is not a sentence.
  return (
    <p className="note-meta">
      {parts.join(' · ')}
      {effective && (
        <>
          {parts.length > 0 && ' · '}
          <button type="button" className="note-meta__language" onClick={onOpenDetails}>
            <span className="visually-hidden">Transcription language: </span>
            {languageName(effective)}
          </button>
        </>
      )}
    </p>
  );
}

/**
 * The `lang` tag for the note's text: the note's language, else the default,
 * and none under Auto-detect or while the default is still being fetched — a
 * tag that says "English" over Malayalam is worse than no tag, and under
 * `auto` nobody knows. Pure and exported for the test.
 */
export function contentLanguage(
  noteLanguage: string,
  defaultLanguage: string | undefined,
): string | undefined {
  const effective = noteLanguage || defaultLanguage;
  return !effective || effective === 'auto' ? undefined : effective;
}

/**
 * The language a recording made into this note is transcribed in, when that
 * is worth saying: `null` for an English note under an English default. Pure
 * and exported so the rule is pinned by a test rather than by reading JSX.
 */
export function effectiveLanguage(
  noteLanguage: string,
  defaultLanguage: string | undefined,
): string | null {
  const fallback = defaultLanguage ?? 'en';
  const effective = noteLanguage || fallback;
  if (effective === fallback && fallback === 'en') return null;
  return effective;
}

/**
 * ‹ Notes.
 *
 * Goes back through history when there is history to go back through, so a
 * note opened from a filtered library returns to that filter; only a cold
 * start with nothing beneath it takes Home in this entry's place
 * (`goBackTo`, the one back-link rule). `useBackGuard` seeds the library
 * under any deep link, so the fallback is rarely taken — it is here for the
 * case where it is.
 */
function BackLink() {
  const { goBackTo } = useTabNavigation();
  return (
    <button
      type="button"
      className="back-link"
      onClick={() => {
        goBackTo(ROUTES.notes);
      }}
    >
      <Icon name="back" size={18} />
      <span className="visually-hidden">Back to </span>Notes
    </button>
  );
}

/**
 * The indicator's word, said through the shell's one region rather than from
 * a live region of its own, which a tick's toast and the editor's status used
 * to overlap (review 2026-10-01, FE-4). Only an outcome is said — saved,
 * queued, failed: "Saving…" and "Unsaved changes" follow every tick and
 * keystroke in the same task and would talk over what the editor just said
 * ("Marked done" → "Saving…"); the visible line still shows them.
 */
function SaveWord({ state, text }: { state: SaveState; text: string }) {
  const outcome = state === 'saved' || state === 'queued' || state === 'error';
  useEffect(() => {
    if (outcome) announce(text);
  }, [outcome, text]);
  return text;
}

/**
 * Autosave state, rendered.
 *
 * Every autosave state is rendered, failures included, with real CSS behind
 * it. An "unsaved" indicator that is only a class with no rule is invisible on
 * every screen.
 */
function SaveIndicator({ editor }: { editor: ReturnType<typeof useNoteEditor> }) {
  const { model } = editor;
  /*
   * "Keep my edits" over a recording whose paragraph cannot be added back
   * asks twice: the first tap says what it would cost, the second does it.
   * Keyed by the server version the conflict is against, so a new conflict
   * starts unconfirmed without an effect resetting anything.
   */
  const [discardConfirmedFor, setDiscardConfirmedFor] = useState<number | null>(null);
  if (model.state === 'clean') return null;

  if (model.state === 'conflict') {
    const theirs = model.theirs;
    const recording = theirs?.recording ?? false;
    const addition = theirs?.addition ?? null;
    const mustConfirm = recording && addition === null;
    const confirmed = discardConfirmedFor === theirs?.version;
    return (
      <div className="save-conflict" role="alert">
        <p className="save-conflict__title">{SAVE_LABELS.conflict}</p>
        <p className="save-conflict__body">
          {recording
            ? 'A recording was filed into this note while you were editing. Nothing has been overwritten — keeping only your edits removes its text from the note.'
            : 'A voice capture or another device saved this note while you were editing. Nothing has been overwritten — choose which version to keep.'}
          {mustConfirm && confirmed && ' Tap Keep my edits again to keep your text without the recording’s.'}
        </p>
        <div className="save-conflict__actions">
          <button type="button" className="save-conflict__action" onClick={editor.takeTheirs}>
            Use the newer version
          </button>
          {addition !== null && (
            <button
              type="button"
              className="save-conflict__action"
              onClick={() => {
                editor.keepBoth();
                void editor.saveNow();
              }}
            >
              {recording ? 'Keep my edits and add the recording' : 'Keep my edits and add the new text'}
            </button>
          )}
          <button
            type="button"
            className="save-conflict__action"
            onClick={() => {
              if (mustConfirm && !confirmed) {
                setDiscardConfirmedFor(theirs?.version ?? null);
                return;
              }
              editor.keepMine();
              void editor.saveNow();
            }}
          >
            Keep my edits
          </button>
        </div>
      </div>
    );
  }

  return (
    <p className="save-indicator" data-state={model.state}>
      <SaveWord state={model.state} text={model.error ?? SAVE_LABELS[model.state]} />
      {model.state === 'error' && (
        <button
          type="button"
          className="save-indicator__retry"
          onClick={() => void editor.saveNow()}
        >
          Try again
        </button>
      )}
    </p>
  );
}
