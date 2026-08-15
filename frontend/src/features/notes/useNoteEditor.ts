import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useReducer, useRef } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { enqueueReplacing } from '@/offline/queue.ts';
import {
  clearQueuedEdit,
  queuedEditFor,
  queuedEditId,
  queuedEditKey,
  type QueuedEdit,
} from '@/offline/queuedEdits.ts';
import { OFFLINE_QUEUE_KEY } from '@/offline/useOfflineQueue.ts';

import {
  applyEdit,
  AUTOSAVE_DELAY_MS,
  editorReducer,
  hasUnsavedWork,
  initialEditor,
  reconcileQueued,
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
  const queryClient = useQueryClient();
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
    const body = {
      version: current.version,
      title: attempted.title,
      body: attempted.body,
      aliases: attempted.aliases,
      tags: attempted.tags,
    };
    dispatch({ type: 'saveStart' });

    try {
      await api.updateNote(note.id, body);
      /*
       * A direct save supersedes anything the queue was still holding for this
       * note. Leaving a dead entry behind would let `reconcileQueued` flip the
       * screen from "Saved" back to the old failure the moment it re-read the
       * queue.
       */
      await clearQueuedEdit(note.id).catch(() => {});
      queryClient.setQueryData(queuedEditKey(note.id), null);
      // The PATCH response carries the note, but re-reading the version from
      // it is enough: the text we sent is the text now stored.
      dispatch({
        type: 'saveSuccess',
        version: current.version + 1,
        draft: attempted,
      });
    } catch (error) {
      /*
       * Offline is not a failure to report — it is a write to make somewhere
       * else. The edit goes into the IndexedDB queue that `useOfflineQueue`
       * drains on reconnect, keyed by the note so that a paragraph typed in
       * three bursts is one queued PATCH rather than three racing ones.
       *
       * This is the caller `offline/queue.ts` never had. Until it existed, the
       * client told the user "your work is saved on this device and will sync"
       * on every offline failure, and nothing was saved and nothing synced.
       */
      if (error instanceof ApiError && error.isOffline) {
        try {
          await enqueueReplacing({
            id: queuedEditId(note.id),
            kind: 'updateNote',
            payload: { noteId: note.id, body },
          });
          // Seeded rather than invalidated: between the write and a refetch the
          // query would still answer "nothing queued", and the screen would
          // read "Saved" for an edit the server has never seen.
          const outstanding: QueuedEdit = { pending: true, dead: false, error: null };
          queryClient.setQueryData(queuedEditKey(note.id), outstanding);
          // The banner counts what is waiting, and nothing else tells it the
          // depth changed: its query polls slowly and only reacts to reconnect
          // and focus, so without this the user is told "nothing is saved on
          // this device" over an edit that is.
          void queryClient.invalidateQueries({ queryKey: OFFLINE_QUEUE_KEY });
          dispatch({ type: 'saveQueued', draft: attempted });
          return;
        } catch {
          // Storage refused — private mode, quota, a blocked-storage policy.
          // Falling through to `saveError` is the honest answer: the edit is
          // genuinely only in this tab, and the user should be told so.
        }
      }

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
  }, [api, note, queryClient]);

  const edit = useCallback(
    (patch: Partial<NoteDraft>) => {
      // Updated synchronously, not just via the effect that mirrors `model`
      // into `latest` after the next render. A caller whose `onCommit` calls
      // `saveNow()` in the same tick as `onChange` — `TagEditor`'s add/remove
      // — would otherwise have `save()` read the pre-edit draft, find nothing
      // dirty, and return without saving or leaving anything scheduled.
      latest.current = applyEdit(latest.current, patch);
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

  /*
   * What the device still owes the server for this note.
   *
   * `networkMode: 'always'` because it is a local read — the whole question is
   * what is on this device — and `staleTime: 0` because the flush changes the
   * answer from outside this component and invalidates this key when it does.
   */
  const queued = useQuery({
    queryKey: queuedEditKey(note?.id ?? ''),
    queryFn: (): Promise<QueuedEdit | null> => queuedEditFor(note?.id ?? ''),
    enabled: Boolean(note),
    networkMode: 'always',
    staleTime: 0,
    retry: false,
  });

  return {
    // Derived at the point of use, never copied into the reducer. A second copy
    // of "is anything outstanding?" is exactly what went stale before.
    model: reconcileQueued(model, note ? queued.data : undefined),
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
