import { useCallback, useEffect, useRef, useState } from 'react';

import { copyText, SETTLE_MS } from './CopyButton.tsx';

/**
 * The note into another app through the system share sheet
 * (`navigator.share`), which is where a phone keeps WhatsApp, Messages and
 * mail; rendered only where the API exists (`canShare`), since on a desktop
 * browser without it the copy button already is the way out. Cancelling the
 * sheet is not an outcome — the browser rejects with `AbortError` and the
 * button says nothing. Any other refusal falls back to the clipboard and says
 * so, because a tap that did nothing is the one thing this must not be.
 */
export function canShare(): boolean {
  return typeof navigator !== 'undefined' && typeof navigator.share === 'function';
}

type ShareState = 'idle' | 'copied' | 'failed';

export function ShareButton({
  title,
  text,
  html,
  className,
}: {
  /** The sheet's subject line, produced on click like the text. */
  title: () => string;
  text: () => string;
  /** The HTML twin for the clipboard fallback, as `CopyButton` takes it; the sheet itself takes text. */
  html?: () => string;
  className?: string;
}) {
  const [state, setState] = useState<ShareState>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  const settle = useCallback((next: ShareState) => {
    setState(next);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      setState('idle');
    }, SETTLE_MS);
  }, []);

  const onClick = useCallback(() => {
    const plain = text();
    void navigator.share({ title: title(), text: plain }).catch((error: unknown) => {
      if (error instanceof DOMException && error.name === 'AbortError') return;
      return copyText(plain, html?.()).then((copied) => {
        settle(copied ? 'copied' : 'failed');
      });
    });
  }, [text, html, title, settle]);

  return (
    <div className="copy">
      <button type="button" className={className ?? 'screen__action'} data-state={state} onClick={onClick}>
        Share…
      </button>
      {state !== 'idle' && (
        <p className="copy__result" data-state={state} role="status" aria-live="polite">
          {state === 'copied'
            ? 'The share sheet would not open, so the note is copied instead.'
            : 'Could not share or copy — this browser would not allow it. Select the text and copy it by hand.'}
        </p>
      )}
    </div>
  );
}
