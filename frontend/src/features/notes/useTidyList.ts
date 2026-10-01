import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useSyncExternalStore } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { queryKeys, usePollNote } from '@/api/queries.ts';
import type { CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { showToast } from '@/components/Toast.tsx';

import { UNDO_STALE } from './ChecklistEditor.tsx';
import { parseChecklist } from './checklist.ts';
import { CLEAN_POLL_TIMEOUT_MS, cleanPollInterval, cleanSettled } from './cleaned.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * Tidy up list (R8-F8): the checklist's ⋮ action that splits sentences into
 * items and groups them, written straight into the body with a 6 s Undo.
 *
 * It replaced the Split up tab, a second editable copy of the list whose
 * first keystroke silently became the real one. The model call is the same
 * — `POST /v1/notes/{id}/clean`, where the server picks `tasks` for a
 * checklist and `SplitOutput` refuses an answer that drops or invents an
 * item or a tick — but nothing is shown before it lands; the Undo is the
 * preview (the OF-DEL pattern). Converting a prose note to a checklist starts
 * one too (`NoteDrawer`), since a dictated paragraph is otherwise one long
 * item.
 *
 * The request answers 202 and the result arrives on the note's `cleaned`, so
 * the note is polled until `cleanSettled`. The result is written only while
 * the list is exactly what it was made from — the view not stale, and the
 * editor's body equal to the server's — so an item ticked, typed or
 * dictated while the model ran is never overwritten; the toast then offers
 * Tidy again instead.
 */

interface Pending {
  /** The view as it was when the request went out; settled when it differs. */
  before: CleanedWire | null;
  since: number;
  /** Started by the prose → checklist switch, which words its toast differently. */
  converted: boolean;
}

/*
 * Module state, not a component's: the request outlives a tab switch and
 * leaving the note within the session, and whichever screen for the note is
 * mounted next picks the result up. A reload forgets it; the result then
 * sits unused in `cleaned_body`, which is harmless.
 */
const pending = new Map<string, Pending>();
/*
 * Each open note's `saveNow`, registered by `useApplyTidy`: a tidy saves the
 * person's unsaved typing first, since the model cleans the server's body
 * and an answer for a body the draft has moved past is never applied.
 */
const savers = new Map<string, () => Promise<void>>();
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function settle(noteId: string): void {
  pending.delete(noteId);
  emit();
}

/** The tidy under way for this note, if any. */
function usePendingTidy(noteId: string): Pending | undefined {
  return useSyncExternalStore(subscribe, () => pending.get(noteId));
}

/** Whether a tidy is under way for this note: the menu's "Tidying…" and the editor's status line. */
export function useTidying(noteId: string): boolean {
  return usePendingTidy(noteId) !== undefined;
}

const TIDY_FAILED = 'Couldn’t tidy the list.';

function startTidy(
  api: ReturnType<typeof useApi>,
  queryClient: QueryClient,
  noteId: string,
  converted: boolean,
): void {
  if (pending.has(noteId)) return;
  // The view as the cache holds it now — after the conversion's PATCH, when
  // there was one — so the answer is told apart from what was there.
  const before = queryClient.getQueryData<NoteDetailWire>(queryKeys.note(noteId))?.cleaned;
  pending.set(noteId, { before: before ?? null, since: Date.now(), converted });
  emit();
  const save = savers.get(noteId)?.() ?? Promise.resolve();
  save.then(() => api.cleanNote(noteId)).catch(() => {
    settle(noteId);
    showToast({
      message: TIDY_FAILED,
      action: { label: 'Try again', onSelect: () => startTidy(api, queryClient, noteId, false) },
    });
  });
}

/** Starts a tidy of the note's list as the server has it. */
export function useStartTidy(): (noteId: string, converted?: boolean) => void {
  const api = useApi();
  const queryClient = useQueryClient();
  return useCallback(
    (noteId: string, converted = false) => {
      startTidy(api, queryClient, noteId, converted);
    },
    [api, queryClient],
  );
}

function count(n: number, noun: string): string {
  return `${String(n)} ${noun}${n === 1 ? '' : 's'}`;
}

/**
 * Applies a finished tidy to the list. Mounted once per open note, by
 * something that lives as long as the note screen does (`NoteDrawer`), so
 * the result lands whichever tab is showing.
 */
export function useApplyTidy(note: NoteDetailWire, editor: NoteEditor): void {
  const entry = usePendingTidy(note.id);
  const start = useStartTidy();
  const queryClient = useQueryClient();
  const cleaned = note.cleaned ?? null;
  const { saveNow } = editor;

  useEffect(() => {
    savers.set(note.id, saveNow);
    return () => {
      if (savers.get(note.id) === saveNow) savers.delete(note.id);
    };
  }, [note.id, saveNow]);

  usePollNote(note.id, entry?.since ?? null, cleanPollInterval);

  useEffect(() => {
    // Once only: an effect run twice (StrictMode, a re-render) sees the entry gone.
    if (!entry || pending.get(note.id) !== entry || !cleanSettled(entry.before, cleaned)) return;
    settle(note.id);

    const retry = { onSelect: () => start(note.id) };
    if (cleaned?.error) {
      showToast({ message: TIDY_FAILED, action: { label: 'Try again', ...retry } });
      return;
    }
    // Turned back into prose meanwhile: a list written over it now would be
    // the conversion undone by surprise, so the answer is simply dropped.
    if ((editor.current().kind ?? note.kind) !== 'checklist') return;
    const previous = editor.current().body;
    if (!cleaned || cleaned.stale || cleaned.mode !== 'tasks' || previous !== note.body) {
      showToast({
        message: 'The list changed while tidying — nothing replaced.',
        action: { label: 'Tidy again', ...retry },
      });
      return;
    }
    if (cleaned.body.trim() === previous.trim()) {
      showToast({ message: 'Already tidy.' });
      return;
    }

    const written = cleaned.body;
    editor.edit({ body: written });
    void editor.saveNow();
    const before = parseChecklist(previous).length;
    const after = count(parseChecklist(written).length, 'item');
    showToast({
      message: entry.converted
        ? `Made a checklist: ${count(before, 'paragraph')} → ${after}.`
        : `List tidied: ${count(before, 'line')} → ${after}.`,
      action: {
        label: 'Undo',
        onSelect: () => {
          // Only while the body is still the tidied one: a recording filed in
          // by refetch, or a tick since, would otherwise leave with the Undo.
          if (editor.current().body !== written) {
            showToast({ message: UNDO_STALE });
            return;
          }
          editor.edit({ body: previous });
          void editor.saveNow();
        },
      },
    });
  }, [entry, cleaned, note.id, note.body, note.kind, editor, start]);

  /*
   * A worker that never answers must not leave "Tidying…" up for good. The
   * note is read once more first: a screen mounted again after the minute
   * (the person came back to the note) has not polled, and the answer may
   * be there; then the apply effect above takes it on the re-render.
   */
  useEffect(() => {
    if (!entry) return;
    const giveUp = setTimeout(
      () => {
        void queryClient
          .refetchQueries({ queryKey: queryKeys.note(note.id), exact: true })
          .then(() => {
            if (pending.get(note.id) !== entry) return;
            const now = queryClient.getQueryData<NoteDetailWire>(queryKeys.note(note.id))?.cleaned;
            if (cleanSettled(entry.before, now)) return;
            settle(note.id);
            showToast({ message: TIDY_FAILED, action: { label: 'Try again', onSelect: () => start(note.id) } });
          });
      },
      Math.max(0, CLEAN_POLL_TIMEOUT_MS - (Date.now() - entry.since)),
    );
    return () => {
      clearTimeout(giveUp);
    };
  }, [entry, note.id, queryClient, start]);
}

/** For tests: forgets every tidy under way. */
export function resetTidies(): void {
  pending.clear();
  savers.clear();
  emit();
}
