import { useCallback, useEffect, useReducer, useRef } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import type { NoteDetailWire } from '@/api/schema.ts';

import {
  AUTOSAVE_DELAY_MS,
  editorReducer,
  hasUnsavedWork,
  initialEditor,
  type EditorModel,
  type NoteDraft,
} from './autosave.ts';

function draftFrom(note: NoteDetailWire): NoteDraft {
  return {
    title: note.title,
    body: note.body,
    aliases: note.aliases ?? [],
    tags: note.tags ?? [],
  };
}

export interface NoteEditor {
  model: EditorModel;
  edit: (patch: Partial<NoteDraft>) => void;
  saveNow: () => Promise<void>;
  takeTheirs: () => void;
  keepMine: () => void;
}

/**
 * Debounced autosave with visible state and real conflict handling.
 *
 * On 409 the server sends `current_version` in the problem document; the
 * current note is re-read so the user is shown what actually changed rather
 * than a bare error. Neither copy is written until they choose.
 */
export function useNoteEditor(note: NoteDetailWire | undefined): NoteEditor {
  const api = useApi();
  const [model, dispatch] = useReducer(
    editorReducer,
    initialEditor({ title: '', body: '', aliases: [], tags: [] }, 0),
  );

  const loadedId = useRef<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  /**
   * Mirrors the reducer state so the debounce callback and the unload handler
   * see the latest draft rather than the one captured when they were created.
   *
   * Written in an effect, not during render. Effects flush within a frame and
   * the debounce is 1.2 seconds, so the mirror is never stale by the time
   * anything reads it.
   */
  const latest = useRef(model);
  useEffect(() => {
    latest.current = model;
  }, [model]);

  useEffect(() => {
    if (!note) return;
    if (loadedId.current === note.id) return;
    loadedId.current = note.id;
    dispatch({ type: 'reset', draft: draftFrom(note), version: note.version });
  }, [note]);

  const save = useCallback(async () => {
    const current = latest.current;
    if (!note) return;
    if (current.state === 'conflict') return;
    if (current.state !== 'dirty' && current.state !== 'error') return;

    const attempted = current.draft;
    dispatch({ type: 'saveStart' });

    try {
      await api.updateNote(note.id, {
        version: current.version,
        title: attempted.title,
        body: attempted.body,
        aliases: attempted.aliases,
        tags: attempted.tags,
      });
      // The PATCH response carries the note, but re-reading the version from
      // it is enough: the text we sent is the text now stored.
      dispatch({
        type: 'saveSuccess',
        version: current.version + 1,
        draft: attempted,
      });
    } catch (error) {
      if (error instanceof ApiError && error.isConflict) {
        const theirs = await api.getNote(note.id).catch(() => null);
        dispatch({
          type: 'conflict',
          theirs: theirs ? draftFrom(theirs) : attempted,
          version: theirs?.version ?? error.currentVersion ?? current.version + 1,
          message: 'This note changed somewhere else while you were editing.',
        });
        return;
      }
      dispatch({
        type: 'saveError',
        message: error instanceof ApiError ? error.userMessage : 'Could not save.',
      });
    }
  }, [api, note]);

  const edit = useCallback(
    (patch: Partial<NoteDraft>) => {
      dispatch({ type: 'edit', patch });
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        void save();
      }, AUTOSAVE_DELAY_MS);
    },
    [save],
  );

  const saveNow = useCallback(async () => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    await save();
  }, [save]);

  /**
   * A stable handle on the current `save`, so the unmount flush below can stay
   * a mount-only effect. Keying that effect on `save` would make it fire its
   * cleanup on every note change, which is not what "the screen went away"
   * means.
   */
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  }, [save]);

  /** Flushes a pending debounce, if one is armed. */
  const flush = useCallback(() => {
    if (!timer.current) return;
    clearTimeout(timer.current);
    timer.current = null;
    void saveRef.current();
  }, []);

  // The safety net v1 had no equivalent of. It only fires when work is
  // genuinely unsaved, so it never nags on a clean note.
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent): void => {
      if (!hasUnsavedWork(latest.current)) return;
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload);
    };
  }, []);

  /*
   * App-switching is not a page unload, so `beforeunload` never sees it. On
   * mobile "hidden" is routinely the last event a document gets before it is
   * frozen or discarded, which makes it the right place to flush.
   */
  useEffect(() => {
    const onHidden = (): void => {
      if (document.visibilityState === 'hidden') flush();
    };
    document.addEventListener('visibilitychange', onHidden);
    return () => {
      document.removeEventListener('visibilitychange', onHidden);
    };
  }, [flush]);

  /*
   * The debounce is FLUSHED on unmount, not dropped.
   *
   * Dropping it silently discarded every edit made in the 1.2 seconds before
   * the user left the screen — and the system Back gesture, the primary
   * one-handed way to leave, is a client-side navigation, so `beforeunload`
   * never fired either. The indicator said "Unsaved changes" and then the
   * screen was gone. The in-app back arrow only worked by accident: tapping it
   * blurs the textarea and `onBlur` calls `saveNow()`; removing an element from
   * the DOM fires no blur.
   *
   * `save` reads the draft from the `latest` mirror and the request outlives
   * the component, so a flush started here still completes.
   */
  useEffect(() => () => {
    flush();
  }, [flush]);

  return {
    model,
    edit,
    saveNow,
    takeTheirs: useCallback(() => {
      dispatch({ type: 'takeTheirs' });
    }, []),
    keepMine: useCallback(() => {
      dispatch({ type: 'keepMine' });
    }, []),
  };
}
