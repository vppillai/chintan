import { useQuery } from '@tanstack/react-query';
import { Suspense, lazy, useEffect, useId, useMemo, useRef, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { queryKeys, useArchiveNote, useDeleteNoteForever, useNotes } from '@/api/queries.ts';
import type { NoteWire, SettingsWire } from '@/api/schema.ts';
import { AUTO_LANGUAGE, LANGUAGES, languageName } from '@/features/settings/languages.ts';
import { useCachedNote, useCachedNotes } from '@/offline/useNotesCache.ts';

import { useCaptureStore } from './store.ts';

/**
 * Where this recording will be filed, shown and changeable before you speak.
 *
 * "Chintan decides" by default, or "Into ⟨title⟩" when the screen was opened
 * from one ("Record into this") or with `?note=`. Tapping it unfolds a short
 * list — Chintan decides first, then the most recent notes — and choosing sets the
 * capture's target before Send. A field above the list finds any note the
 * device holds, not only the recent twenty, and what it is given becomes a
 * new note to record into when no note is the one: "New note “Tax 2026”"
 * is `POST /v1/notes` with that title, then that note as the target, so the
 * recording is filed into it as into any other. Making one needs the
 * server, so offline the field only finds. The backend has always accepted `note_id` on
 * `POST /v1/captures`; nothing in the UI offered it, so the only ways to file
 * into a particular note were to say its name and hope the router agreed, or
 * to fix it afterwards from the "needs a target" row.
 *
 * The list is the library the app already holds. Online it is the notes
 * query, which the library screen has almost certainly just fetched; offline
 * it is the corpus cached on the device — recording works with no connection,
 * so the chooser has to as well.
 *
 * Under the pill, the language the recording will be transcribed in: the
 * target note's, else the default from You. Whisper is told one language per
 * recording and nothing on this screen said which, so a Malayalam dictation
 * sent under an English default failed with no warning it could have.
 */

/** Enough to find a note made this week before anything is typed. */
const RECENT_LIMIT = 20;

// The sheet is a chunk of its own: most recordings never open it.
const TargetChooserSheet = lazy(() =>
  import('./TargetChooserSheet.tsx').then((module) => ({ default: module.TargetChooserSheet })),
);

export interface TargetChooserProps {
  noteId: string | null;
  onChoose: (noteId: string | null) => void;
  /**
   * After Send the target has left the device and cannot be changed here;
   * after a failure there is nothing to aim.
   */
  disabled?: boolean;
  /**
   * Whether to fetch the list yet. The capture screen holds it back until the
   * microphone is live: on a cold launch of the shortcut the request competed
   * with `getUserMedia` for a slow link's first seconds, and the list is not
   * needed until the user reaches for the pill.
   */
  fetchList?: boolean;
}

/** The pill and the first option when no note is chosen: the router decides where it goes. */
export const DEFAULT_TARGET_LABEL = 'Chintan decides';

export function TargetChooser({
  noteId,
  onChoose,
  disabled = false,
  fetchList = true,
}: TargetChooserProps) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const api = useApi();
  const rootRef = useRef<HTMLDivElement>(null);
  const pillRef = useRef<HTMLButtonElement>(null);

  /*
   * The sheet is an overlay over the controls (finding 10), so a tap beside
   * it or Escape closes it, as the ⋮ menu's does; before, the pill was the
   * only way to put it away. Escape also hands focus back to the pill: the
   * option it was on is unmounted, and focus would otherwise fall to the
   * body and the next Tab start from the top of the document.
   */
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent): void => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setOpen(false);
        pillRef.current?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('keydown', onKeyDown, true);
    };
  }, [open]);

  const served = useNotes({ state: 'active' }, { enabled: fetchList });
  // Held back with the list, for the same reason: nothing but the microphone
  // request goes out until the stream is live. Same key as `useSettings`, so
  // a You visit or a settings save has usually answered it already.
  const settings = useQuery({
    queryKey: queryKeys.settings(),
    queryFn: () => api.getSettings(),
    enabled: fetchList,
  });
  const cached = useCachedNotes('active');
  // The note this screen was opened from may not be among the recent twenty,
  // and its own screen has just cached the full record — so its title is on
  // the device even when the list cannot be reached.
  const opened = useCachedNote(noteId ?? undefined);

  const notes: NoteWire[] = useMemo(
    () => served.data?.pages.flatMap((page) => page.items) ?? cached.data ?? [],
    [served.data, cached.data],
  );
  const recent = useMemo(() => notes.slice(0, RECENT_LIMIT), [notes]);

  /*
   * The note "New note “…”" made, until it is kept. Picking another target,
   * or leaving without sending anything into it, leaves a note that is only
   * a title; it is discarded then, as the note screen discards an untouched
   * placeholder (archive, then purge) — but only once the server says it is
   * still empty with no capture, so a recording or a typed thought already
   * on its way into it keeps it.
   */
  const [made, setMade] = useState<NoteWire | null>(null);
  const madeId = useRef<string | null>(null);
  const archiveNote = useArchiveNote();
  const deleteForever = useDeleteNoteForever();
  const discardIfUntouched = useRef((id: string) => {
    void api.getNote(id).then(
      (note) => {
        if (note.body.trim() !== '' || (note.captures?.length ?? 0) > 0) return;
        void archiveNote.mutateAsync(id).then(() => deleteForever.mutate(id), () => undefined);
      },
      () => undefined,
    );
  });
  useEffect(
    () => () => {
      const id = madeId.current;
      if (!id) return;
      // A take still aimed at it — recording, or sent and uploading — keeps it.
      const { model } = useCaptureStore.getState();
      if (model.noteId === id && model.state !== 'idle') return;
      discardIfUntouched.current(id);
    },
    [],
  );
  /** Every note the device holds or the server has listed, newest first, for the field. */
  const everything = useMemo(() => {
    const byId = new Map<string, NoteWire>();
    for (const note of [...notes, ...(cached.data ?? [])]) {
      if (!byId.has(note.id)) byId.set(note.id, note);
    }
    return [...byId.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  }, [notes, cached.data]);
  const chosen = noteId
    ? (everything.find((note) => note.id === noteId) ??
      opened.data ??
      (made?.id === noteId ? made : undefined))
    : undefined;
  /*
   * No target means the router chooses, and it may well file into a note
   * that already exists — "add this to my roof note" is the hint under the
   * pill. The default read "Into New note", which promised the opposite
   * (R7-14). A new note is made by naming it in the field, never by default.
   */
  const title = noteId === null ? DEFAULT_TARGET_LABEL : (chosen?.title ?? 'This note');
  const language = transcriptionLanguage(noteId, chosen, settings.data);

  return (
    <div ref={rootRef} className="target-chooser">
      <button
        ref={pillRef}
        type="button"
        className="target-chooser__pill"
        aria-expanded={open}
        aria-controls={listId}
        disabled={disabled}
        onClick={() => {
          setOpen((wasOpen) => !wasOpen);
        }}
      >
        {/* Real spaces between the spans: an accessible name is the text
            nodes run together, and "IntoNew note" is not a name. */}
        {noteId !== null && (
          <>
            <span className="target-chooser__into">Into</span>{' '}
          </>
        )}
        <span className="target-chooser__title">{title}</span>{' '}
        <span className="target-chooser__caret" aria-hidden="true">
          ▾
        </span>
      </button>

      {open && (
        <Suspense fallback={null}>
          <TargetChooserSheet
            id={listId}
            noteId={noteId}
            everything={everything}
            recent={recent}
            paused={served.fetchStatus === 'paused'}
            onChoose={(id, created) => {
              const previous = madeId.current;
              if (previous && previous !== id) {
                madeId.current = null;
                discardIfUntouched.current(previous);
              }
              if (created) {
                setMade(created);
                madeId.current = created.id;
              }
              onChoose(id);
              setOpen(false);
            }}
          />
        </Suspense>
      )}

      {language && <p className="target-chooser__language">{language}</p>}
    </div>
  );
}

/**
 * The line under the pill, or `null` while the answer is not on the device
 * yet: the settings have not arrived, or the target is a note whose record
 * has not. The rule is the server's (`transcriptionLanguage` in the
 * pipeline): the note's own language, else the tenant default, else English.
 * Said in the language's own script, since the person it is for reads that.
 */
function transcriptionLanguage(
  noteId: string | null,
  note: NoteWire | null | undefined,
  settings: SettingsWire | undefined,
): string | null {
  if (!settings) return null;
  if (noteId && !note) return null;
  const code = note?.language || settings.default_language || 'en';
  if (code === AUTO_LANGUAGE) return 'Language detected per recording';
  const native = LANGUAGES.find((entry) => entry.code === code)?.native ?? languageName(code);
  return `Transcribed as ${native}`;
}
