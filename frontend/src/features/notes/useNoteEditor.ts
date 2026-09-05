import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useReducer, useRef } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { recordSavedNote } from '@/api/queries.ts';
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
  type EditorEvent,
  type EditorModel,
  type NoteDraft,
} from './autosave.ts';

function draftFrom(note: NoteDetailWire): NoteDraft {
  return {
    title: note.title,
    body: note.body,
    aliases: note.aliases ?? [],
    tags: note.tags ?? [],
    // Absent on the wire means "inherits the default", which the contract
    // spells as the empty string on the way back up.
    language: note.language ?? '',
    auto_clean: note.auto_clean ?? false,
    // The mode is only known from a cleaned view that exists; with none, the
    // draft leaves it unset and nothing is sent until the user picks one.
    ...(note.cleaned?.mode ? { cleaned_mode: note.cleaned.mode } : {}),
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
 *
 * One PATCH in flight at a time. Two used to overlap: type, wait 1.2 s, save #1
 * leaves with `version N`; keep typing, 1.2 s later save #2 also leaves with
 * `version N`, because the reducer has not seen #1's response yet. #1 lands
 * and the server is at N+1; #2 is answered 409 — and the screen told the user
 * "a voice capture or another device saved this note while you were editing"
 * about their own keystrokes, offering to throw one burst away. On cellular a
 * three-second PATCH is normal, so this was not a corner case. Now a save that
 * finds one running marks "dirty again" and the running one re-saves when it
 * lands, carrying the version the *server* returned rather than a guess of
 * `version + 1`.
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

  /**
   * Applies an event to the mirror *and* to the reducer, in that order.
   *
   * The mirror is what `save` reads, and the re-save after an in-flight PATCH
   * runs synchronously from the first save's `finally` — before React has
   * rendered, so before the effect above has copied the new `version` across.
   * Read through the effect alone, the second save would carry the version
   * the first one just superseded and manufacture the very 409 this guards
   * against. `edit()` already does the same for the same reason.
   */
  /**
   * A save that settles after the screen has gone must not dispatch into a
   * component that no longer exists. React would only warn, but a test runner
   * tearing down its window mid-flight surfaced it as a crash, and the mirror
   * below is the only thing the late arrival could still usefully update.
   */
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const commit = useCallback((event: EditorEvent) => {
    latest.current = editorReducer(latest.current, event);
    if (mounted.current) dispatch(event);
  }, []);

  /** The PATCH currently on the wire, if any. */
  const inFlight = useRef<Promise<void> | null>(null);
  /** Set when a save was asked for while one was running. */
  const dirtyAgain = useRef(false);
  /**
   * A stable handle on the current `save`, so the unmount flush below can stay
   * a mount-only effect and the in-flight re-save can call the guarded entry
   * point without `save` closing over itself. Keying the unmount effect on
   * `save` would make it fire its cleanup on every note change, which is not
   * what "the screen went away" means. Filled in by the effect after `save` is
   * defined; the placeholder is never reachable before then.
   */
  const saveRef = useRef<() => Promise<void>>(async () => {});

  useEffect(() => {
    if (!note) return;
    if (loadedId.current !== note.id) {
      loadedId.current = note.id;
      commit({ type: 'reset', draft: draftFrom(note), version: note.version });
      return;
    }
    /*
     * The same note, newer on the server than what is on screen: a voice
     * capture the worker appended while this was open, arriving by refetch.
     *
     * Loading once per id was how the screen came to show the pre-recording
     * body until a second visit — the cache handed over the stale copy first,
     * the fresh one landed a moment later, and nothing looked at it. It is
     * adopted only when there is nothing of the user's to lose: no edit
     * pending, none on the wire, and nothing typed since the last save. A
     * dirty draft keeps its text, and the version check on its next PATCH
     * turns the divergence into the conflict prompt, as it always has.
     */
    const current = latest.current;
    const settled = current.state === 'clean' || current.state === 'saved';
    if (!settled || inFlight.current || timer.current) return;
    if (note.version <= current.version) return;
    commit({ type: 'reset', draft: draftFrom(note), version: note.version });
  }, [note, commit]);

  const performSave = useCallback(async () => {
    const current = latest.current;
    if (!note) return;
    if (current.state === 'conflict') return;
    if (current.state !== 'dirty' && current.state !== 'error') return;

    const attempted = current.draft;
    /*
     * The cleaned-view fields go out only when they changed. Every other
     * field is sent whole on every save; these two are the one part of the
     * contract the backend is still growing into, and a PATCH that names a
     * field the server does not yet accept is refused outright
     * (`DisallowUnknownFields`) — which would turn every keystroke's save
     * into "Couldn't save" until the backend caught up. Sending them only
     * when the user touched them keeps the ordinary save on the old contract.
     */
    const body = {
      version: current.version,
      title: attempted.title,
      body: attempted.body,
      aliases: attempted.aliases,
      tags: attempted.tags,
      ...(attempted.language !== undefined ? { language: attempted.language } : {}),
      ...(attempted.auto_clean !== undefined &&
      Boolean(attempted.auto_clean) !== Boolean(current.saved.auto_clean)
        ? { auto_clean: attempted.auto_clean }
        : {}),
      ...(attempted.cleaned_mode !== undefined &&
      attempted.cleaned_mode !== current.saved.cleaned_mode
        ? { cleaned_mode: attempted.cleaned_mode }
        : {}),
    };
    commit({ type: 'saveStart' });

    try {
      const stored = await api.updateNote(note.id, body);
      /*
       * A direct save supersedes anything the queue was still holding for this
       * note. Leaving a dead entry behind would let `reconcileQueued` flip the
       * screen from "Saved" back to the old failure the moment it re-read the
       * queue.
       */
      await clearQueuedEdit(note.id).catch(() => {});
      queryClient.setQueryData(queuedEditKey(note.id), null);
      /*
       * What the server now holds, written into the caches this screen reads
       * from next time. The response is a list row — no body — so the text is
       * the draft that was sent, which is by definition what was stored; the
       * version, timestamp and snippet are the server's. Without this the
       * note reopened within `staleTime` showed the pre-edit body and its
       * next save lost a version check to the user's own edit.
       */
      const { language: _previous, ...base } = { ...note, ...stored };
      recordSavedNote(queryClient, {
        ...base,
        title: attempted.title,
        body: attempted.body,
        aliases: attempted.aliases,
        tags: attempted.tags,
        // `''` means "inherits the default", which the wire spells as absence.
        ...(attempted.language ? { language: attempted.language } : {}),
        // The list row the PATCH answers with may not carry it; the toggle
        // must not flip back to what the cache held before the save.
        ...(attempted.auto_clean !== undefined ? { auto_clean: attempted.auto_clean } : {}),
        ...(note.captures ? { captures: note.captures } : {}),
      });
      // The version is the server's, not `current.version + 1`. The two agree
      // today, but the guess is exactly the kind of thing that stops being true
      // quietly — a server-side normalisation that bumps twice, a replayed
      // idempotent response — and the text we sent is the text now stored, so
      // the response's number is the only one worth carrying forward.
      commit({
        type: 'saveSuccess',
        version: stored.version,
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
          commit({ type: 'saveQueued', draft: attempted });
          return;
        } catch {
          // Storage refused — private mode, quota, a blocked-storage policy.
          // Falling through to `saveError` is the honest answer: the edit is
          // genuinely only in this tab, and the user should be told so.
        }
      }

      if (error instanceof ApiError && error.isConflict) {
        const theirs = await api.getNote(note.id).catch(() => null);
        commit({
          type: 'conflict',
          theirs: theirs ? draftFrom(theirs) : attempted,
          version: theirs?.version ?? error.currentVersion ?? current.version + 1,
          message: 'This note changed somewhere else while you were editing.',
        });
        return;
      }
      commit({
        type: 'saveError',
        message: error instanceof ApiError ? error.userMessage : 'Could not save.',
      });
    }
  }, [api, commit, note, queryClient]);

  /**
   * The serialised entry point: at most one `performSave` on the wire.
   *
   * A caller who arrives while one is running does not start a second — it
   * would carry the same `version` and lose — but the intent is not dropped
   * either: `dirtyAgain` is set and the running save re-runs this once it has
   * settled, reading the draft and version as they are *then*. The awaiting
   * caller is handed the in-flight promise, so `saveNow()` from a blur still
   * resolves when a request has actually finished.
   */
  const save = useCallback(async (): Promise<void> => {
    if (inFlight.current) {
      dirtyAgain.current = true;
      return inFlight.current;
    }
    const run = performSave().finally(() => {
      inFlight.current = null;
      if (!dirtyAgain.current) return;
      dirtyAgain.current = false;
      // `save`, not `performSave`: the re-save can itself be overtaken by a
      // third burst and needs the same guard.
      void saveRef.current();
    });
    inFlight.current = run;
    return run;
  }, [performSave]);

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

  // The safety net for a tab closed mid-edit. It only fires when work is
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
