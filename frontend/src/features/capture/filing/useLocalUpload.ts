import { useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

import { queryKeys } from '@/api/queries.ts';
import type { CaptureWire } from '@/api/schema.ts';

import { UNSENT_CAPTURES_KEY } from '../ResumePrompt.tsx';
import type { CaptureModel } from '../machine.ts';
import { useCaptureStore } from '../store.ts';

/**
 * How long a landed upload's local row waits for the server's row to replace
 * it before giving up its place. The poll is asked for at once, so normally
 * this is a few hundred milliseconds; the bound is for a connection that died
 * between the PUT landing and the poll, where the row would otherwise sit at
 * "Uploaded" for ever.
 */
export const HANDOFF_GRACE_MS = 10_000;

/**
 * The upload in progress, read from the capture store rather than the server.
 *
 * Send hands off at once, so for the seconds between the tap and
 * `POST /v1/captures` returning there is no server row to show — and the
 * server never knows about the PUT at all until the object lands. This row
 * covers that gap: "Uploading… 40%" from the store's own progress, then
 * "Uploaded" until the server's row arrives, which replaces it and releases
 * the machine. A failed upload stays here with Retry and Discard, because the
 * bytes are still on this device and only this device can act.
 *
 * Two readers. A note's Recordings tab passes its own id and sees only an
 * upload aimed at it, with the note's own captures as the server rows that
 * take over. The library's filing row passes `homeOnly` and sees only an
 * upload aimed at nothing — one aimed at a note is already on that note's
 * Recordings tab, which is where Send went — while still doing the hand-over
 * for either, since whichever screen is mounted is the one that can. They are
 * never on screen together.
 */
export function useLocalUpload(
  serverItems: readonly CaptureWire[],
  noteId?: string,
  options: { homeOnly?: boolean } = {},
): CaptureModel | null {
  const model = useCaptureStore((state) => state.model);
  const reset = useCaptureStore((state) => state.reset);
  const queryClient = useQueryClient();

  const uploading = model.state === 'uploading';
  const landed = model.state === 'uploaded';
  const failed =
    model.state === 'failed' &&
    (model.failure?.kind === 'upload-failed' || model.failure?.kind === 'spend-capped');
  const serverHasIt =
    landed &&
    model.serverCaptureId !== null &&
    serverItems.some((capture) => capture.id === model.serverCaptureId);
  const target = model.noteId;

  useEffect(() => {
    if (!landed) return;
    // The server has the audio: ask for its row now rather than at the poll's
    // next tick, and the device's list of unsent recordings is one shorter.
    void queryClient.invalidateQueries({ queryKey: queryKeys.pendingCaptures() });
    void queryClient.invalidateQueries({ queryKey: UNSENT_CAPTURES_KEY });
    // The note it went into has a new recording; its own poll takes over from
    // there, since the new row is non-terminal.
    if (target) void queryClient.invalidateQueries({ queryKey: queryKeys.note(target) });
  }, [landed, target, queryClient]);

  useEffect(() => {
    if (!landed) return;
    if (serverHasIt) {
      // The server's row is on screen; the machine has nothing left to say.
      reset();
      return;
    }
    const timer = setTimeout(reset, HANDOFF_GRACE_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [landed, serverHasIt, reset]);

  if (noteId !== undefined && target !== noteId) return null;
  /*
   * A failed upload is shown on Home whatever it was aimed at. The note's
   * Recordings tab is where a moving one belongs, but nobody is on that tab
   * after a spend cap or an expired link has bounced them, and ResumePrompt
   * leaves the machine's own recording out — so a recording that exists on
   * this device alone had no handle anywhere until a reload.
   */
  if (options.homeOnly && target !== null && !failed) return null;
  if (uploading || failed || (landed && !serverHasIt)) return model;
  return null;
}
