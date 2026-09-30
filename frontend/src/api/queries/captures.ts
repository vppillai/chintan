/**
 * TanStack Query bindings for captures: the filing row's poll and its cadence,
 * and every mutation that removes, re-files or retries a recording.
 */

import {
  useMutation,
  useQueries,
  type Query,
  useQuery,
  useQueryClient,
  type QueryClient,
  type UseMutationResult,
} from '@tanstack/react-query';

import { useApi } from '../ApiProvider.tsx';
import { ApiError } from '../problem.ts';
import type { ChintanApi } from '../endpoints.ts';
import { STUCK_AFTER_MS, isTerminalStatus } from '../schema.ts';
import type { CaptureMoveWire, CaptureWire, NoteDetailWire } from '../schema.ts';

import { SEARCH_CORPUS_KEY, invalidateNoteLists, queryKeys } from './keys.ts';

/**
 * How long a recording that produced nothing keeps saying so. A `no_content`
 * row has nothing to open and nothing to retry, so it is the one receipt that
 * is allowed to expire on its own; every other stopped capture stays until
 * the user acts on it (see `isFilingRelevant`).
 */
const RECENTLY_SETTLED_MS = 10 * 60 * 1000;

/**
 * How long a `Filed` receipt is offered before it stops being a filing row.
 * A day covers the recording made on the walk home and read at the desk; after
 * that the recording is in its note, where the Recordings tab shows it, and a
 * receipt on the library screen is noise — the owner met three of them, weeks
 * old, above an empty library.
 */
const FILED_RECEIPT_MS = 24 * 60 * 60 * 1000;

/** Newest-first, and twenty is more than one person records before the first has filed. */
const CAPTURE_LIST_LIMIT = 20;

/**
 * How often to ask while something is still moving through the pipeline.
 *
 * A ladder, keyed on how long ago anything moving last made progress. A
 * capture's first half-minute is when it is most likely to flip — on prod the
 * pipeline finishes in p50 1.9 s, p90 4.0 s (n=189 over seven days,
 * 2026-09-26) — and a fixed 4 s poll added a median 2 s of pure waiting on
 * top of that, which is what the owner felt as "even tiny recordings take a
 * while". So a young capture is asked after every 1.5 s. Past that the poll
 * relaxes to 4 s: a capture that old is waiting on a provider. Two minutes
 * after the last progress it backs off to 15 s, and once the capture counts
 * as stuck (`STUCK_AFTER_MS`) to once a minute — it used to stay at 4 s for
 * ever, so one capture stuck for hours kept an open Home at nine hundred
 * requests an hour. It never stops while anything is non-terminal: the row's
 * Retry appears at fifteen minutes (`retryAccepted`, read at render), and the
 * poll's re-render is what makes it appear on time.
 */
export const CAPTURE_POLL_FAST_MS = 1_500;
export const CAPTURE_POLL_FAST_WINDOW_MS = 30_000;
export const CAPTURE_POLL_INTERVAL_MS = 4_000;
export const CAPTURE_POLL_SLOW_MS = 15_000;
export const CAPTURE_POLL_STUCK_MS = 60_000;
/** Quiet for this long since the last progress, and the poll relaxes to `CAPTURE_POLL_SLOW_MS`. */
const CAPTURE_POLL_QUIET_MS = 2 * 60 * 1000;

/**
 * The next poll delay for a set of captures, or `false` when nothing is
 * moving. Pure, so the cadence is testable without a query client.
 */
export function capturePollInterval(
  items: readonly CaptureWire[],
  now: number = Date.now(),
): number | false {
  const moving = items.filter((capture) => !isTerminalStatus(capture.status));
  if (moving.length === 0) return false;
  const young = moving.some((capture) => within(capture.created_at, CAPTURE_POLL_FAST_WINDOW_MS, now));
  if (young) return CAPTURE_POLL_FAST_MS;
  // The youngest progress decides: one capture still moving keeps the poll
  // brisk however long another has been stuck beside it.
  const since = Math.max(
    ...moving.map((capture) => Date.parse(capture.last_progress_at ?? capture.created_at)),
  );
  const quiet = now - since;
  if (!Number.isFinite(quiet) || quiet < CAPTURE_POLL_QUIET_MS) return CAPTURE_POLL_INTERVAL_MS;
  return quiet < STUCK_AFTER_MS ? CAPTURE_POLL_SLOW_MS : CAPTURE_POLL_STUCK_MS;
}

function within(iso: string | null | undefined, windowMs: number, now: number): boolean {
  if (!iso) return false;
  const at = Date.parse(iso);
  return Number.isFinite(at) && now - at < windowMs;
}

/**
 * Whether the library's filing row has anything to say about a capture.
 *
 * Anything still moving, obviously. Of the stopped ones: `failed`,
 * `spend_capped` and `needs_target` always, because each has an action the
 * user must take and a capture waiting on the user must not vanish silently.
 * `appended` for a day (`FILED_RECEIPT_MS`): the row is the one place that
 * says "your recording is in this note, here it is", and within the day it
 * stays until the user opens the note or dismisses it (`FilingRow` remembers
 * which, per device). It used to fade after ten minutes, which meant a
 * recording made on the walk home had no receipt by the time the user sat
 * down to read it; then it never faded, which meant a device that had
 * dismissed nothing showed receipts from weeks ago. `no_content` alone
 * expires quickly: there is nothing to open and nothing to do.
 */
export function isFilingRelevant(capture: CaptureWire, now: number = Date.now()): boolean {
  switch (capture.status) {
    case 'failed':
    case 'spend_capped':
    case 'needs_target':
      return true;
    case 'appended':
      // The append's own time when the API sends it; the capture's otherwise.
      return within(capture.appended_at ?? capture.created_at, FILED_RECEIPT_MS, now);
    case 'no_content':
      return within(capture.created_at, RECENTLY_SETTLED_MS, now);
    default:
      return !isTerminalStatus(capture.status);
  }
}

/**
 * Every capture the library's filing row has something to say about.
 *
 * This is what makes the row survive a reload: the set is server state, not a
 * JavaScript variable. An in-flight capture id held in a module-level field is
 * lost on refresh, stranding the audio with no UI able to find it again.
 *
 * ONE request per poll. Firing `pending`, `failed`, `needs_target` and `all`
 * in parallel every four seconds, plus all four again on every window focus,
 * comes to ~120 invocations for a two-minute pipeline, while the user is most
 * likely driving on cellular. The newest twenty captures contain
 * everything those filters would have returned that is worth showing (see
 * `isFilingRelevant`), so the filtering happens here. Always stale: the
 * library is remounted every time the user comes back to it — including
 * from the capture screen, six hundred milliseconds after a Send — and that
 * mount is one moment a fresh answer is owed. Coming to the foreground is the
 * other, whatever the interval is doing (see the option below).
 */
export function usePendingCaptures(enabled = true) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: queryKeys.pendingCaptures(),
    queryFn: async () => {
      const page = await api.listCaptures({ status: 'all', limit: CAPTURE_LIST_LIMIT });
      const items = page.items.filter((capture) => isFilingRelevant(capture));
      // Read from the cache rather than closed over: the library remounts on
      // every visit and the comparison has to be with the last poll, not the
      // last render.
      const previous = queryClient.getQueryData<{ items: CaptureWire[] }>(
        queryKeys.pendingCaptures(),
      );
      for (const noteId of newlyAppendedNoteIds(previous?.items, items)) {
        refreshAppendedNote(queryClient, noteId);
      }
      return { items };
    },
    enabled,
    refetchInterval: (query) => capturePollInterval(query.state.data?.items ?? []),
    /*
     * Focus is the one moment a capture a device made while the app was in
     * the background is owed a look. The interval covers the foreground case
     * for captures this client started, and stops when nothing is moving —
     * which is exactly the state a Home left in a pocket is in while a ring
     * files three recordings into three notes. This was `false`, from before
     * the inbox, on the reasoning that a focus had nothing new to learn; the
     * owner saw those filings only "once you refresh, pull to refresh".
     * `'always'` rather than `true` so a `staleTime` set elsewhere can never
     * quietly turn it off again. TanStack pauses the interval while the
     * document is hidden, so the background costs nothing.
     */
    refetchOnWindowFocus: 'always',
    staleTime: 0,
  });
}

/**
 * Notes whose text just changed under the app: captures that were not
 * `appended` on the previous poll and are now.
 *
 * Nothing else tells the note screen. The append is written by the worker,
 * not by this client, so no mutation here ever invalidated `['note', id]` —
 * and a note the user had open while recording into it (or opened from the
 * filing row's "Open the note") kept showing the body from before the
 * recording until a second visit. Restricted to transitions: a capture that
 * was already `appended` on the last poll has nothing new to say, and the
 * first poll after a cold start — with no previous answer — invalidates
 * nothing, because there is no cache yet to be stale.
 */
export function newlyAppendedNoteIds(
  previous: readonly CaptureWire[] | undefined,
  current: readonly CaptureWire[],
): string[] {
  if (!previous) return [];
  const before = new Map(previous.map((capture) => [capture.id, capture.status]));
  const noteIds = new Set<string>();
  for (const capture of current) {
    if (capture.status !== 'appended' || !capture.note_id) continue;
    if (before.get(capture.id) === 'appended') continue;
    noteIds.add(capture.note_id);
  }
  return Array.from(noteIds);
}

/**
 * A note the pipeline has just written to is stale everywhere the app holds
 * it: the detail query, every list under `['notes']` (snippet, updated_at,
 * ordering) and the device's copy, which lives under the same prefix and is
 * rewritten as a side effect of the detail refetch.
 */
export function refreshAppendedNote(queryClient: QueryClient, noteId: string): void {
  void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
  void queryClient.invalidateQueries({ queryKey: ['notes'] });
  // The body just grew by a transcript; the corpus on the device should know
  // the words in it before the user goes looking for them.
  void queryClient.invalidateQueries({ queryKey: SEARCH_CORPUS_KEY });
}

/**
 * The open note's recordings that are still moving, each asked after on its
 * own — `GET /v1/captures/{id}`, a few hundred bytes — instead of the whole
 * note, body and every capture, over and over.
 *
 * The note's detail query used to carry the filing cadence itself, which
 * meant the full note every 1.5 s, then every 4 s, for as long as anything
 * filed. A moving stage is written into the cached note, so the banner's
 * segments move exactly as they did. A capture that stops moving is not: the
 * note is read again instead, so the status and the body it changed arrive
 * together — an `appended` shown over the old body would announce a paragraph
 * that is not there yet. An append also refreshes the lists and the corpus,
 * decided the way the library's poll decides it (`newlyAppendedNoteIds`).
 * A capture the server no longer has (404) stops being asked after and the
 * note is read again, which drops its row.
 *
 * Called by the note screen alone. `useNote` has other readers — the tab bar
 * asks whether the open note is archived — and a second caller would mean a
 * second set of pollers.
 *
 * The cadence is read from the note's copy, not the capture query's own data:
 * a regeneration sends an `appended` capture back to `transcribed`, and a
 * capture query left holding `appended` from an earlier visit would otherwise
 * never ask again. `noteReadAt` is when that copy was read; a capture answer
 * newer than it that already says terminal waits for the note's refetch.
 */
export function useInFlightCaptures(
  noteId: string | undefined,
  captures: readonly CaptureWire[] | undefined,
  noteReadAt: number,
): void {
  const api = useApi();
  const queryClient = useQueryClient();
  const moving =
    noteId === undefined ? [] : (captures ?? []).filter((capture) => !isTerminalStatus(capture.status));
  useQueries({
    queries: moving.map((capture) => ({
      queryKey: queryKeys.capture(capture.id),
      queryFn: async () => {
        const noteKey = queryKeys.note(noteId as string);
        let fresh: CaptureWire;
        try {
          fresh = await api.getCapture(capture.id);
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) {
            void queryClient.invalidateQueries({ queryKey: noteKey });
          }
          throw error;
        }
        if (!isTerminalStatus(fresh.status)) {
          queryClient.setQueryData<NoteDetailWire>(noteKey, (current) =>
            current?.captures
              ? {
                  ...current,
                  captures: current.captures.map((held) =>
                    held.id === fresh.id ? { ...held, ...fresh } : held,
                  ),
                }
              : current,
          );
          return fresh;
        }
        const appendedTo = newlyAppendedNoteIds([capture], [fresh]);
        for (const id of appendedTo) refreshAppendedNote(queryClient, id);
        // Once: a second invalidation would cancel the first read and start another.
        if (!appendedTo.includes(noteId as string)) {
          void queryClient.invalidateQueries({ queryKey: noteKey });
        }
        return fresh;
      },
      // The note that listed it was just read, so the first ask waits a tick.
      initialData: capture,
      initialDataUpdatedAt: noteReadAt,
      refetchInterval: (query: Query<CaptureWire>) => {
        const { data, dataUpdatedAt, error } = query.state;
        if (error instanceof ApiError && error.status === 404) return false;
        if (data && isTerminalStatus(data.status) && dataUpdatedAt > noteReadAt) return false;
        return capturePollInterval([capture]);
      },
    })),
  });
}

/* ---------------------------------------------------------------------------
   Removing or re-filing a recording

   Both operations rewrite the note body on the server — the capture's
   paragraph is cut out, and for a move spliced into the target — so what the
   detail query holds is stale the moment either succeeds. The row is dropped
   from the cache at once so the screen answers the tap, and the note is then
   refetched for the body, the snippet and the version the editor adopts.

   Bulk by construction: one recording is a list of one. The endpoints take a
   single capture, so several are several calls run together; `allSettled`
   so a 409 on one still-filing recording does not stop the rest, and the
   caller is told which ones did not go.
   --------------------------------------------------------------------------- */

export interface CaptureBatchResult {
  done: string[];
  failed: { captureId: string; error: unknown }[];
}

async function settleEach(
  captureIds: readonly string[],
  run: (captureId: string) => Promise<unknown>,
): Promise<CaptureBatchResult> {
  const outcomes = await Promise.allSettled(captureIds.map((id) => run(id)));
  const result: CaptureBatchResult = { done: [], failed: [] };
  outcomes.forEach((outcome, index) => {
    const captureId = captureIds[index] as string;
    if (outcome.status === 'fulfilled') result.done.push(captureId);
    else result.failed.push({ captureId, error: outcome.reason });
  });
  return result;
}

/** The rows that are gone leave the cached note now; the refetch brings the body. */
function dropCapturesFromNote(
  queryClient: QueryClient,
  noteId: string,
  captureIds: readonly string[],
): void {
  if (captureIds.length === 0) return;
  const gone = new Set(captureIds);
  queryClient.setQueryData<NoteDetailWire>(queryKeys.note(noteId), (current) =>
    current?.captures
      ? { ...current, captures: current.captures.filter((capture) => !gone.has(capture.id)) }
      : current,
  );
}

export function useDeleteCaptures() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ captureIds }: { noteId: string; captureIds: string[] }) =>
      settleEach(captureIds, (id) => api.deleteCapture(id)),
    onSuccess: (result, { noteId }) => {
      dropCapturesFromNote(queryClient, noteId, result.done);
      if (result.done.length > 0) {
        refreshAppendedNote(queryClient, noteId);
        void queryClient.invalidateQueries({ queryKey: ['captures'] });
      }
    },
  });
}

/** The batch outcome plus the note the recordings went to — the id asked for, or the one the server made. */
export interface CaptureMoveResult extends CaptureBatchResult {
  targetId: string;
}

export function useMoveCaptures() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      captureIds,
      target,
    }: {
      noteId: string;
      target: CaptureMoveWire;
      captureIds: string[];
    }): Promise<CaptureMoveResult> => {
      if ('note_id' in target) {
        const result = await settleEach(captureIds, (id) => api.moveCapture(id, target));
        return { ...result, targetId: target.note_id };
      }
      /*
       * Into a note that does not exist yet: the first move makes it and the
       * rest follow by id, so three recordings asked into one new note land
       * in one note rather than in three named alike. A refusal of the first
       * is the whole batch's — nothing has moved yet — and reaches the caller
       * as the mutation's error, with the server's sentence.
       */
      const [first, ...rest] = captureIds;
      if (first === undefined) throw new Error('nothing to move');
      const targetId = (await api.moveCapture(first, target))?.note_id;
      if (!targetId) throw new Error('the answer named no note');
      const result = await settleEach(rest, (id) => api.moveCapture(id, { note_id: targetId }));
      return { ...result, done: [first, ...result.done], targetId };
    },
    onSuccess: (result, { noteId }) => {
      dropCapturesFromNote(queryClient, noteId, result.done);
      if (result.done.length > 0) {
        // Both bodies changed: the source lost paragraphs, the target gained
        // them in chronological position. A target made just now is also a
        // new row for the lists, which `refreshAppendedNote` invalidates.
        refreshAppendedNote(queryClient, noteId);
        refreshAppendedNote(queryClient, result.targetId);
        void queryClient.invalidateQueries({ queryKey: ['captures'] });
      }
    },
  });
}

export function useRetryCapture(): UseMutationResult<CaptureWire, Error, string> {
  const api: ChintanApi = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (captureId: string) => api.retryCapture(captureId),
    onSuccess: (capture) => {
      queryClient.setQueryData(queryKeys.capture(capture.id), capture);
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
    },
  });
}

export function useSetCaptureTarget() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      captureId,
      target,
    }: {
      captureId: string;
      target: { note_id: string } | { new_note_title: string };
    }) => api.setCaptureTarget(captureId, target),
    onSuccess: (capture) => {
      queryClient.setQueryData(queryKeys.capture(capture.id), capture);
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
      invalidateNoteLists(queryClient);
    },
  });
}
