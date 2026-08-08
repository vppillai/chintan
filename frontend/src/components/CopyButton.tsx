import { useCallback, useEffect, useRef, useState } from 'react';

/**
 * Copying text out of the app.
 *
 * This is a voice-capture product: the whole point is getting a thought out of
 * your head and then using it somewhere else. Until now the only route was
 * select-all inside a textarea, which one-handed on a phone is miserable.
 *
 * Three things on the note screen could be meant by "copy" — the note, the raw
 * transcript, and the cleaned transcript — and one button cannot mean all
 * three. So each control names what it copies, and there is no unlabelled
 * "copy" anywhere.
 *
 * A copy with no confirmation is indistinguishable from one that failed, and
 * failure is real: `writeText` rejects outside a secure context, without a user
 * gesture, and more readily on Safari than anywhere else. Both outcomes are
 * stated, and the failure says what to do instead rather than doing nothing.
 */

export type CopyState = 'idle' | 'copied' | 'failed';

/** How long the confirmation stays before the control returns to rest. */
const SETTLE_MS = 2_500;

export interface CopyButtonProps {
  /** Produced on click, not on render: the note is still being edited. */
  text: () => string;
  /** Names what is being copied. "Copy" on its own is never enough here. */
  label: string;
  className?: string;
}

export function CopyButton({ text, label, className }: CopyButtonProps) {
  const [state, setState] = useState<CopyState>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // A settle timer outliving the screen would set state on an unmounted tree.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const settle = useCallback((next: CopyState) => {
    setState(next);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      setState('idle');
    }, SETTLE_MS);
  }, []);

  const onClick = useCallback(() => {
    const value = text();
    void (async () => {
      try {
        // Absent entirely on an insecure origin, so this is not merely a
        // rejected promise — it is a missing API, and reads as one.
        if (!navigator.clipboard?.writeText) throw new Error('no clipboard');
        await navigator.clipboard.writeText(value);
        settle('copied');
      } catch {
        settle('failed');
      }
    })();
  }, [text, settle]);

  return (
    <div className="copy">
      <button
        type="button"
        className={className ?? 'screen__action'}
        onClick={onClick}
      >
        {label}
      </button>

      {/*
        Inline and polite. The app's only live region is visually hidden and
        spec §5.7 puts sighted feedback inline, so this is both: it renders
        where the user is looking and announces itself once.
      */}
      {state !== 'idle' && (
        <p className="copy__result" data-state={state} role="status" aria-live="polite">
          {state === 'copied'
            ? 'Copied'
            : 'Could not copy — this browser would not allow it. Select the text and copy it by hand.'}
        </p>
      )}
    </div>
  );
}
