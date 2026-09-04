import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import type { StoredCapture } from '@/offline/db.ts';

import { discardCapture, unconfirmedCaptures } from './buffer.ts';
import { isCaptureBusy } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { uploadCapture } from './uploader.ts';

export const UNSENT_CAPTURES_KEY = ['captures', 'unsent'] as const;

/**
 * Offers back a recording that never reached the server.
 *
 * The audio has always been written to IndexedDB as it was produced and pruned
 * only on confirmation, so a tab killed mid-upload — routine on a phone during
 * a long recording — leaves the bytes on disk. Until this existed, nothing read
 * them back, which made the durability work invisible: the recording survived
 * and the user was never told.
 *
 * Oldest first, because that is the one most at risk of being forgotten.
 */
export function ResumePrompt() {
  const api = useApi();
  const queryClient = useQueryClient();
  const active = useCaptureStore((state) => state.model);
  const [busy, setBusy] = useState<string | null>(null);
  const [pendingDiscard, setPendingDiscard] = useState<StoredCapture | null>(null);
  const [error, setError] = useState<string | null>(null);

  const { data } = useQuery({
    queryKey: UNSENT_CAPTURES_KEY,
    queryFn: () => unconfirmedCaptures(),
    // Read once on boot. Nothing else creates entries behind the app's back:
    // a live capture drives this state itself.
    staleTime: Infinity,
    retry: false,
  });

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: UNSENT_CAPTURES_KEY });
    void queryClient.invalidateQueries({ queryKey: ['captures'] });
  }, [queryClient]);

  /*
   * The recording the store is sending right now is on disk with no
   * confirmation yet — exactly the shape this prompt looks for. Since Send
   * hands off to the library at once, this list is often read mid-upload, so
   * that recording has to be excluded by its id or the library would offer to
   * resend the very upload it is showing progress for one row below. The same
   * goes for a failed upload, which the filing row already offers to retry.
   * A recording left at review (Back from the capture screen) is not excluded:
   * this prompt is then the way back to it.
   */
  const owned =
    active.localId &&
    (isCaptureBusy(active) || active.state === 'uploaded' || active.state === 'failed')
      ? active.localId
      : null;
  const stranded = (data ?? []).filter((record) => record.localId !== owned);
  const oldest = stranded[0];
  if (!oldest) return null;

  const send = async (record: StoredCapture): Promise<void> => {
    setBusy(record.localId);
    setError(null);
    let failed: string | null = null;
    await uploadCapture(
      api,
      {
        localId: record.localId,
        contentType: record.contentType as 'audio/webm',
        durationMs: record.durationMs,
        noteId: record.noteId,
        peaks: record.peaks ?? [],
        // What makes this a resume rather than a first attempt: the server
        // already minted a capture for these bytes, so its answer to another
        // create is a stored one, credential included.
        serverCaptureId: record.serverCaptureId,
      },
      (event) => {
        if (event.type === 'uploadFailed') failed = event.message;
        if (event.type === 'spendCapped') failed = event.message;
      },
    );
    setBusy(null);
    setError(failed);
    refresh();
  };

  return (
    <>
      <section className="resume-prompt" aria-label="Unsent recording">
        <div className="resume-prompt__body">
          <p className="resume-prompt__title">
            You have an unsent recording from {relativeTime(oldest.createdAt)}.
          </p>
          <p className="resume-prompt__detail">
            <span className="numeric">{formatDuration(oldest.durationMs)}</span>
            {stranded.length > 1 && (
              <>
                {' · '}
                <span className="numeric">{stranded.length - 1}</span> more waiting
              </>
            )}
          </p>
          {error && (
            <p className="resume-prompt__error" role="alert">
              {error}
            </p>
          )}
        </div>

        <div className="resume-prompt__actions">
          <button
            type="button"
            className="resume-prompt__action"
            onClick={() => {
              setPendingDiscard(oldest);
            }}
            disabled={busy !== null}
          >
            Discard
          </button>
          <button
            type="button"
            className="resume-prompt__action resume-prompt__action--primary"
            onClick={() => void send(oldest)}
            disabled={busy !== null}
          >
            {busy === oldest.localId ? 'Sending…' : 'Send'}
          </button>
        </div>
      </section>

      {/*
        Discard is confirmed because this is the one artifact the product cannot
        get back. Everything else in the app is recoverable from the server;
        these bytes exist in exactly one place.
      */}
      <ConfirmDialog
        open={pendingDiscard !== null}
        title="Discard this recording?"
        body="It has not been sent, and it is not saved anywhere else. This cannot be undone."
        confirmLabel="Discard recording"
        destructive
        onCancel={() => {
          setPendingDiscard(null);
        }}
        onConfirm={() => {
          const target = pendingDiscard;
          setPendingDiscard(null);
          if (!target) return;
          void discardCapture(target.localId).then(refresh);
        }}
      />
    </>
  );
}

function formatDuration(ms: number): string {
  const total = Math.round(ms / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, '0')}`;
}

export function relativeTime(epochMs: number, now: number = Date.now()): string {
  const seconds = Math.max(0, Math.round((now - epochMs) / 1000));
  if (seconds < 90) return 'a moment ago';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} minutes ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return hours === 1 ? 'an hour ago' : `${hours} hours ago`;
  const days = Math.round(hours / 24);
  return days === 1 ? 'yesterday' : `${days} days ago`;
}
