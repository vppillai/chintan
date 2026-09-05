import { useQueryClient } from '@tanstack/react-query';
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react';
import { flushSync } from 'react-dom';
import { useNavigate, useParams } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { queryKeys, useNote, useSettings } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { PullToRefresh } from '@/components/PullToRefresh.tsx';
import { useLocalUpload } from '@/features/capture/FilingRow.tsx';
import type { CaptureModel } from '@/features/capture/machine.ts';
import { languageName } from '@/features/settings/languages.ts';
import { useAutoGrow } from '@/hooks/useAutoGrow.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNote } from '@/offline/useNotesCache.ts';

import { CleanedPanel } from './CleanedPanel.tsx';
import {
  FindBar,
  markMatches,
  useReportTotal,
  useScrollToActiveMatch,
  type FindTarget,
} from './FindBar.tsx';
import { NoteActions, noteLanguageFieldId, type NotePanelKind } from './NoteActions.tsx';
import {
  NoteTabList,
  noteTabId,
  noteTabPanelId,
  useNoteTab,
  type NoteTab,
  type NoteTabDescriptor,
} from './NoteTabs.tsx';
import { Recordings } from './Recordings.tsx';
import { SAVE_LABELS } from './autosave.ts';
import { FIND_CLOSED, findMatches, findReducer, type FindState, type FindAction } from './find.ts';
import { describeMoment, describeRecordings } from './groups.ts';
import { useNoteEditor, type NoteEditor } from './useNoteEditor.ts';
import { countWords, describeWords } from './words.ts';

/**
 * A note.
 *
 * Top to bottom: the way back, the title, one line of metadata, then a strip
 * of segments — Text · Cleaned · Recordings (N) — and the one panel it
 * selects, with the action bar at the foot. The strip sticks under the banner
 * while the panel scrolls, so the recordings are one tap away from anywhere
 * in a long note rather than a screen or five below its last paragraph, which
 * is where they sat when body and recordings were one page. The text is the
 * document; the cleaned view is the worker's rewrite of the whole of it; the
 * recordings are its sources.
 */
export function NoteDetailScreen() {
  const { id } = useParams<{ id: string }>();
  const online = useOnline();
  const { data: served, isLoading, fetchStatus, error } = useNote(id);
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
  const [selectingRecordings, setSelectingRecordings] = useState(false);

  /*
   * Which of the action bar's disclosures is open. Held here rather than in
   * the bar because the meta line opens one of them: "· Malayalam" up by the
   * title is a fact about the note, and tapping a fact should go to where it
   * is set.
   */
  const [panel, setPanel] = useState<NotePanelKind | null>(null);
  const openDetails = useCallback(() => {
    if (!note) return;
    // Rendered synchronously so the select exists to take the focus: a
    // keyboard or screen-reader user lands on the control the fact came from.
    flushSync(() => {
      setPanel('details');
    });
    document.getElementById(noteLanguageFieldId(note.id))?.focus();
  }, [note]);

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
    /*
     * Three different sentences, because they are three different situations
     * and the screen used to say one of them for all. A note that is simply
     * not on this device was reported as one that "may have been archived or
     * purged" — describing a deletion that never happened, to a user who could
     * see the note one screen earlier.
     */
    const offline = paused || !online || (error instanceof ApiError && error.isOffline);
    const unanswered = !offline && (patienceOver || (error instanceof ApiError && error.isRetryable));

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
              ? 'This note isn’t saved on this device, and the server hasn’t answered yet.'
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
        language={editor.model.draft.language ?? ''}
        onOpenDetails={openDetails}
      />

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
        localUpload={localUpload}
        onSelectingRecordings={setSelectingRecordings}
        find={find}
        dispatchFind={dispatchFind}
        findBarId={findBarId}
        findInputRef={findInputRef}
      />

      {/*
        While recordings are being selected their own bar takes the foot of
        the screen, so the note's action bar steps aside rather than stacking
        under it. Hidden, not unmounted: an open Details or Share panel is
        still there when the selection ends. The bar is outside the panels, so
        it is there on every tab.
      */}
      <NoteActions
        note={note}
        editor={editor}
        hidden={selectingRecordings}
        // A conflict is resolved before anything else, and at a laptop height
        // the open Details panel covered the banner's two buttons. The panel
        // steps aside while the banner is up and is back as it was after.
        open={editor.model.state === 'conflict' ? null : panel}
        onOpenChange={setPanel}
      />
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
  localUpload,
  onSelectingRecordings,
  find,
  dispatchFind,
  findBarId,
  findInputRef,
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  localUpload: CaptureModel | null;
  onSelectingRecordings: (selecting: boolean) => void;
  find: FindState;
  dispatchFind: (action: FindAction) => void;
  findBarId: string;
  findInputRef: RefObject<HTMLInputElement | null>;
}) {
  const [tab, selectTab] = useNoteTab(note.id);
  // The upload on its way counts: it is a row on the tab already.
  const count = (note.captures?.length ?? 0) + (localUpload ? 1 : 0);
  const tabs: NoteTabDescriptor[] = [
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
  const searchable = tab !== 'recordings';
  const target: FindTarget | null =
    find.open && find.query !== '' && searchable
      ? { query: find.query, active: find.active, onTotal }
      : null;

  return (
    <>
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
            editor={editor}
            find={target}
            onDismissFind={() => {
              dispatchFind({ type: 'close' });
            }}
          />
        ) : tab === 'cleaned' ? (
          <CleanedPanel note={note} editor={editor} find={target} />
        ) : (
          <Recordings
            note={note}
            localUpload={localUpload}
            onSelectingChange={onSelectingRecordings}
          />
        )}
      </NotePanel>
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
    >
      {children}
    </div>
  );
}

/**
 * The editable body: as tall as its text, and the page is what scrolls.
 *
 * While the find bar has a query the textarea gives way to a read-only mirror
 * of the same text in the same box, because a `<mark>` cannot be drawn inside
 * a textarea. Closing the bar — or tapping the mirror — brings the textarea
 * back with the caret on the match that was current, so finding a word and
 * editing it is one gesture, not a find followed by a hunt.
 */
function TextPanel({
  editor,
  find,
  onDismissFind,
}: {
  editor: NoteEditor;
  find: FindTarget | null;
  /** A tap on the mirror: close the bar and go back to editing. */
  onDismissFind: () => void;
}) {
  const body = editor.model.draft.body;
  // Measured here, in the panel, so re-opening the Text tab measures again:
  // a hook in the screen would keep a ref to a textarea that had left the
  // document and never see the one that replaced it.
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  const mirrorRef = useRef<HTMLElement>(null);
  useAutoGrow(bodyRef, body);

  const query = find?.query ?? '';
  const matches = useMemo(() => findMatches(body, query), [body, query]);
  const mirrored = find !== null;
  useReportTotal(find, matches.length);
  useScrollToActiveMatch(mirrorRef, find?.active ?? null, matches.length);

  /*
   * Where the caret goes when the textarea returns: the match that was
   * current when the mirror was last shown. Remembered in a ref because by
   * the time the textarea is back, `find` is null and the match is gone.
   */
  const lastActive = useRef<{ start: number; end: number } | null>(null);
  const current = find ? matches[find.active] : undefined;
  useEffect(() => {
    if (current) lastActive.current = current;
  }, [current]);
  // Declared after the remembering effect, so the caret is placed from the
  // match remembered by this render, not the one before it.
  const wasMirrored = useRef(false);
  useEffect(() => {
    if (wasMirrored.current && !mirrored) {
      const textarea = bodyRef.current;
      const range = lastActive.current;
      if (textarea) {
        // The mirror and the textarea are the same box, so the match is where
        // the mark was and the page need not move; `preventScroll` keeps the
        // browser from re-centring on the whole field.
        textarea.focus({ preventScroll: true });
        if (range) {
          try {
            textarea.setSelectionRange(range.start, range.end);
          } catch {
            /* A browser that will not place the caret still has the focus. */
          }
        }
      }
    }
    wasMirrored.current = mirrored;
  }, [mirrored]);

  if (find) {
    return (
      <section
        ref={mirrorRef}
        className="note-body-mirror prose"
        aria-label="Note body, read-only while finding"
        // A pointer's way back to editing. The keyboard's is Escape in the
        // bar, which does the same thing; the mirror itself is text, not a
        // control, so a screen reader can read it and its marks.
        onClick={onDismissFind}
      >
        {markMatches(body, matches, find.active)}
      </section>
    );
  }

  return (
    <>
      <label className="visually-hidden" htmlFor="note-body">
        Note body
      </label>
      <textarea
        id="note-body"
        ref={bodyRef}
        className="note-body-input prose"
        value={body}
        rows={6}
        onChange={(event) => {
          editor.edit({ body: event.target.value });
        }}
        onBlur={() => void editor.saveNow()}
      />
    </>
  );
}

/** How long "Loading…" is allowed to stand before the screen says what it knows. */
export const LOADING_PATIENCE_MS = 6_000;

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
 * "Updated today 14:02 · house · 3 recordings · 4:12 · 412 words · Malayalam".
 *
 * The tags and the word count are the draft's, not the server's, so adding a
 * tag in the bar or typing a sentence shows up here at once rather than after
 * the save lands.
 *
 * The language is the last fact, and only when it is worth a word: the
 * note's own choice when it differs from the default, or the default itself
 * when that is not English. An English note under an English default says
 * nothing — the common case should not carry a label. It is a button because
 * the control that sets it is in the Details panel at the foot, and the
 * owner's trial found it there by accident or not at all.
 */
function NoteMeta({
  note,
  tags,
  body,
  language,
  onOpenDetails,
}: {
  note: NoteDetailWire;
  tags: readonly string[];
  body: string;
  /** The draft's `language`: a code, `auto`, or the empty string to inherit. */
  language: string;
  onOpenDetails: () => void;
}) {
  const { data: settings } = useSettings();
  const updated = describeMoment(note.updated_at);
  const parts = [
    // "Updated today 14:02", but "Updated 6 Aug 09:14" — a month keeps its case.
    updated ? `Updated ${updated.replace(/^(Today|Yesterday)/, (day) => day.toLowerCase())}` : null,
    ...tags,
    describeRecordings(note),
    describeWords(countWords(body)),
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
 * start with nothing beneath it goes to the library directly. React Router
 * numbers its entries in `history.state.idx`, and `useBackGuard` seeds the
 * library under any deep link, so the fallback is rarely taken — it is here
 * for the case where it is.
 */
function BackLink() {
  const navigate = useNavigate();
  return (
    <button
      type="button"
      className="back-link"
      onClick={() => {
        const index = (window.history.state as { idx?: number } | null)?.idx ?? 0;
        if (index > 0) void navigate(-1);
        else void navigate(ROUTES.notes);
      }}
    >
      <Icon name="back" size={18} />
      <span className="visually-hidden">Back to </span>Notes
    </button>
  );
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
  if (model.state === 'clean') return null;

  if (model.state === 'conflict') {
    return (
      <div className="save-conflict" role="alert">
        <p className="save-conflict__title">{SAVE_LABELS.conflict}</p>
        <p className="save-conflict__body">
          A voice capture or another device saved this note while you were editing. Nothing
          has been overwritten — choose which version to keep.
        </p>
        <div className="save-conflict__actions">
          <button type="button" className="save-conflict__action" onClick={editor.takeTheirs}>
            Use the newer version
          </button>
          <button
            type="button"
            className="save-conflict__action"
            onClick={() => {
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
    <p
      className="save-indicator"
      data-state={model.state}
      role="status"
      aria-live="polite"
    >
      {model.error ?? SAVE_LABELS[model.state]}
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
