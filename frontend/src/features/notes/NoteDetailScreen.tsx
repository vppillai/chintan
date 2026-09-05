import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useNavigate, useParams } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { queryKeys, useNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { PullToRefresh } from '@/components/PullToRefresh.tsx';
import { useLocalUpload } from '@/features/capture/FilingRow.tsx';
import type { CaptureModel } from '@/features/capture/machine.ts';
import { useAutoGrow } from '@/hooks/useAutoGrow.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNote } from '@/offline/useNotesCache.ts';

import { CleanedPanel } from './CleanedPanel.tsx';
import { NoteActions } from './NoteActions.tsx';
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

      <NoteMeta note={note} tags={editor.model.draft.tags} body={editor.model.draft.body} />

      <SaveIndicator editor={editor} />

      {/*
        Keyed by the note: the tab strip's memory and the panels' own state
        belong to one note, and "Open <title>" after a move walks from one
        note to another without remounting this screen.
      */}
      <NoteViews
        key={note.id}
        note={note}
        editor={editor}
        localUpload={localUpload}
        onSelectingRecordings={setSelectingRecordings}
      />

      {/*
        While recordings are being selected their own bar takes the foot of
        the screen, so the note's action bar steps aside rather than stacking
        under it. Hidden, not unmounted: an open Tags or Share panel is still
        there when the selection ends. The bar is outside the panels, so it is
        there on every tab.
      */}
      <NoteActions note={note} editor={editor} hidden={selectingRecordings} />
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
}: {
  note: NoteDetailWire;
  editor: NoteEditor;
  localUpload: CaptureModel | null;
  onSelectingRecordings: (selecting: boolean) => void;
}) {
  const [tab, setTab] = useNoteTab(note.id);
  // The upload on its way counts: it is a row on the tab already.
  const count = (note.captures?.length ?? 0) + (localUpload ? 1 : 0);
  const tabs: NoteTabDescriptor[] = [
    { id: 'text', label: 'Text' },
    { id: 'cleaned', label: 'Cleaned' },
    { id: 'recordings', label: 'Recordings', count },
  ];

  return (
    <>
      <NoteTabList noteId={note.id} tabs={tabs} value={tab} onChange={setTab} />
      <NotePanel noteId={note.id} tab={tab}>
        {tab === 'text' ? (
          <TextPanel editor={editor} />
        ) : tab === 'cleaned' ? (
          <CleanedPanel note={note} editor={editor} />
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

/** The editable body: as tall as its text, and the page is what scrolls. */
function TextPanel({ editor }: { editor: NoteEditor }) {
  // Measured here, in the panel, so re-opening the Text tab measures again:
  // a hook in the screen would keep a ref to a textarea that had left the
  // document and never see the one that replaced it.
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  useAutoGrow(bodyRef, editor.model.draft.body);

  return (
    <>
      <label className="visually-hidden" htmlFor="note-body">
        Note body
      </label>
      <textarea
        id="note-body"
        ref={bodyRef}
        className="note-body-input prose"
        value={editor.model.draft.body}
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
 * "Updated today 14:02 · house · 3 recordings · 4:12 · 412 words".
 *
 * The tags and the word count are the draft's, not the server's, so adding a
 * tag in the bar or typing a sentence shows up here at once rather than after
 * the save lands.
 */
function NoteMeta({
  note,
  tags,
  body,
}: {
  note: NoteDetailWire;
  tags: readonly string[];
  body: string;
}) {
  const updated = describeMoment(note.updated_at);
  const parts = [
    // "Updated today 14:02", but "Updated 6 Aug 09:14" — a month keeps its case.
    updated ? `Updated ${updated.replace(/^(Today|Yesterday)/, (day) => day.toLowerCase())}` : null,
    ...tags,
    describeRecordings(note),
    describeWords(countWords(body)),
  ].filter((part): part is string => Boolean(part));

  // A real " · " between the facts, not a CSS pseudo-element: a screen reader
  // reads text, and "housereading list3 recordings" is not a sentence.
  return <p className="note-meta">{parts.join(' · ')}</p>;
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
