import { useQuery } from '@tanstack/react-query';
import { useEffect, useId, useRef, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { queryKeys, useNotes } from '@/api/queries.ts';
import type { NoteWire, SettingsWire } from '@/api/schema.ts';
import { openItemsText, parseChecklist } from '@/features/notes/checklist.ts';
import { formatRowTime } from '@/features/notes/groups.ts';
import { AUTO_LANGUAGE, LANGUAGES, languageName } from '@/features/settings/languages.ts';
import { useCachedNote, useCachedNotes } from '@/offline/useNotesCache.ts';

/**
 * Where this recording will be filed, shown and changeable before you speak.
 *
 * "Into · New note" by default, or the note's title when the screen was opened
 * from one ("Record into this") or with `?note=`. Tapping it unfolds a short
 * list — New note first, then the most recent notes — and choosing sets the
 * capture's target before Send. The backend has always accepted `note_id` on
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

/** Enough to find a note made this week without a search field. */
const RECENT_LIMIT = 20;

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

  /*
   * The sheet is an overlay over the controls (finding 10), so a tap beside
   * it or Escape closes it, as the ⋮ menu's does; before, the pill was the
   * only way to put it away.
   */
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent): void => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setOpen(false);
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

  const notes: NoteWire[] =
    served.data?.pages.flatMap((page) => page.items) ?? cached.data ?? [];
  const recent = notes.slice(0, RECENT_LIMIT);

  const chosen = noteId ? (notes.find((note) => note.id === noteId) ?? opened.data) : undefined;
  const title = noteId === null ? 'New note' : (chosen?.title ?? 'This note');
  const language = transcriptionLanguage(noteId, chosen, settings.data);

  const choose = (id: string | null): void => {
    onChoose(id);
    setOpen(false);
  };

  return (
    <div ref={rootRef} className="target-chooser">
      <button
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
        <span className="target-chooser__into">Into</span>{' '}
        <span className="target-chooser__title">{title}</span>{' '}
        <span className="target-chooser__caret" aria-hidden="true">
          ▾
        </span>
      </button>

      {open && (
        <div
          id={listId}
          className="target-chooser__sheet"
          role="group"
          aria-label="Where this recording goes"
        >
          <ul className="target-chooser__list" role="list">
            <li>
              <button
                type="button"
                className="target-chooser__option"
                aria-pressed={noteId === null}
                onClick={() => {
                  choose(null);
                }}
              >
                New note
              </button>
            </li>
            {recent.map((note) => (
              <li key={note.id}>
                <button
                  type="button"
                  className="target-chooser__option"
                  aria-pressed={note.id === noteId}
                  onClick={() => {
                    choose(note.id);
                  }}
                >
                  <span className="target-chooser__option-title">{note.title}</span>
                  {/* Part of the name on purpose: two "Roof repair"s are as
                      alike to a screen reader as to the eye. */}
                  <span className="target-chooser__option-meta">{optionMeta(note)}</span>
                </button>
              </li>
            ))}
          </ul>
          {recent.length === 0 && (
            <p className="target-chooser__empty">
              {served.fetchStatus === 'paused'
                ? 'No notes are on this device yet. The router will file it once you reconnect.'
                : 'No notes yet — this will start the first one.'}
            </p>
          )}
        </div>
      )}

      {language && <p className="target-chooser__language">{language}</p>}
    </div>
  );
}

/**
 * What tells two notes with the same title apart: when it was last touched
 * and how it begins, cut at forty characters so the row stays one line.
 *
 * Characters as a reader counts them, not code points. In Malayalam, Tamil or
 * Hindi the fortieth code point is a vowel sign or a virama as often as a
 * letter, and a cut there strands the sign — a dotted circle on screen, a
 * broken syllable in the option's name — or takes it off its consonant. The
 * cut is marked, because the name is read where the CSS ellipsis is not.
 */
const GRAPHEMES = new Intl.Segmenter(undefined, { granularity: 'grapheme' });
const META_SNIPPET_GRAPHEMES = 40;

export function optionMeta(note: NoteWire): string {
  const when = formatRowTime(note.updated_at);
  // A checklist's snippet is its raw `- [ ]` lines; the row and the Move
  // sheet show the open items as words (QA 2026-09-21, finding 9).
  const text =
    note.kind === 'checklist'
      ? openItemsText(parseChecklist(note.snippet ?? ''))
      : (note.snippet ?? '');
  const graphemes = Array.from(GRAPHEMES.segment(text.trim()), (s) => s.segment);
  const snippet =
    graphemes.length > META_SNIPPET_GRAPHEMES
      ? `${graphemes.slice(0, META_SNIPPET_GRAPHEMES).join('')}…`
      : graphemes.join('');
  return [when, snippet].filter(Boolean).join(' · ');
}

/**
 * The line under the pill, or `null` while the answer is not on the device
 * yet: the settings have not arrived, or the target is a note whose record
 * has not. The rule is the server's (`transcriptionLanguage` in the
 * pipeline): the note's own language, else the tenant default, else English.
 * Said in the language's own script, since the person it is for reads that.
 */
export function transcriptionLanguage(
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
