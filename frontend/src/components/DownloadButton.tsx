import { useCallback, useEffect, useRef, useState } from 'react';

/**
 * Saving a file out of the app — the audio, or the note as markdown.
 *
 * Not a plain `<a href download>`: the audio lives behind a presigned,
 * cross-origin S3 URL, and the `download` attribute is silently ignored on a
 * cross-origin link with no `Content-Disposition: attachment` — clicking one
 * just plays or displays the resource instead of saving it — the same failure
 * mode `Player`'s own doc comment names for `window.open`. Fetching the bytes
 * and downloading a same-origin `blob:` URL is what actually works regardless
 * of origin.
 *
 * Mirrors `CopyButton`'s idle/settled state machine on purpose: this screen
 * already teaches "click, then read the one line below the button" for
 * copying, and a second, differently-shaped affordance for saving would be a
 * second thing to learn for the same kind of action.
 */

export type DownloadState = 'idle' | 'downloading' | 'done' | 'failed';

const SETTLE_MS = 2_500;

export interface DownloadButtonProps {
  /** Produced on click, not on render — the audio URL can expire between them. */
  blob: () => Promise<Blob>;
  /** Produced alongside the blob, so a markdown download can name itself after the current title. */
  filename: () => string;
  label: string;
  className?: string;
}

/**
 * Hands a file to the browser's save flow: a same-origin `blob:` URL on a
 * detached anchor with `download` set, clicked and revoked. The one save path
 * in the app — the button below, and the recording archive on the note
 * screen, both go through here, so the anchor-and-revoke discipline lives in
 * one place.
 */
export function saveBlob(file: Blob, filename: string): void {
  const url = URL.createObjectURL(file);
  try {
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    // Not attached to the DOM: Chrome and Firefox both fire a synthetic
    // click on a detached anchor, and attaching it would mean also
    // remembering to remove it on every path, including the ones that
    // throw.
    link.click();
  } finally {
    URL.revokeObjectURL(url);
  }
}

export function DownloadButton({ blob, filename, label, className }: DownloadButtonProps) {
  const [state, setState] = useState<DownloadState>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const settle = useCallback((next: DownloadState) => {
    setState(next);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      setState('idle');
    }, SETTLE_MS);
  }, []);

  const onClick = useCallback(() => {
    setState('downloading');
    void (async () => {
      try {
        saveBlob(await blob(), filename());
        settle('done');
      } catch {
        settle('failed');
      }
    })();
  }, [blob, filename, settle]);

  return (
    <div className="download">
      <button
        type="button"
        className={className ?? 'screen__action'}
        onClick={onClick}
        disabled={state === 'downloading'}
      >
        {state === 'downloading' ? 'Downloading…' : label}
      </button>

      {(state === 'done' || state === 'failed') && (
        <p className="download__result" data-state={state} role="status" aria-live="polite">
          {state === 'done' ? 'Downloaded' : 'Could not download — try again.'}
        </p>
      )}
    </div>
  );
}
