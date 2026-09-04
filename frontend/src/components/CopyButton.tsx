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
 * gesture, and more readily on Safari than anywhere else. So there are two
 * routes — the async clipboard API, then a selection copy through a scratch
 * textarea when the API is missing or refuses — and both outcomes are stated:
 * the button itself reads "Copied" for a moment, and the failure says what to
 * do instead rather than doing nothing.
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

/**
 * The pre-`navigator.clipboard` route: select the text in an off-screen
 * textarea and ask the document to copy the selection.
 *
 * Still needed. Older WebKit has no `navigator.clipboard`, an http origin has
 * none anywhere, and Safari will reject `writeText` when the call is not
 * inside the gesture that started it. `execCommand('copy')` is deprecated but
 * implemented everywhere, and a deprecated copy beats no copy.
 *
 * Synchronous on purpose — it has to run while the tap still counts as a user
 * gesture. Focus is handed back to whatever had it, or the tap would leave the
 * user's keyboard focus on nothing.
 */
function copyViaSelection(value: string): boolean {
  if (typeof document.execCommand !== 'function') return false;
  const previous = document.activeElement;
  const scratch = document.createElement('textarea');
  scratch.value = value;
  scratch.readOnly = true;
  scratch.setAttribute('aria-hidden', 'true');
  scratch.tabIndex = -1;
  scratch.className = 'copy__scratch';
  document.body.appendChild(scratch);

  let copied = false;
  try {
    scratch.focus({ preventScroll: true });
    scratch.select();
    // iOS ignores `select()` on a readonly control; the range call is what
    // actually selects there.
    scratch.setSelectionRange(0, value.length);
    copied = document.execCommand('copy');
  } catch {
    copied = false;
  } finally {
    scratch.remove();
    if (previous instanceof HTMLElement) previous.focus({ preventScroll: true });
  }
  return copied;
}

/** Clipboard API first, selection copy second. Resolves to whether anything worked. */
async function copyText(value: string): Promise<boolean> {
  // Absent entirely on an insecure origin, so this is not merely a rejected
  // promise — it is a missing API, and the fallback runs inside the gesture.
  if (!navigator.clipboard?.writeText) return copyViaSelection(value);
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    return copyViaSelection(value);
  }
}

export function CopyButton({ text, label, className }: CopyButtonProps) {
  /*
   * The outcome is stamped with the label it belongs to. A control renamed
   * under a stale "Copied" — the transcript toggle switching from raw to
   * cleaned — would otherwise claim the new thing had been copied. It has
   * not, so an outcome for another label reads as idle.
   */
  const [outcome, setOutcome] = useState<{ label: string; state: CopyState } | null>(null);
  const state: CopyState = outcome && outcome.label === label ? outcome.state : 'idle';
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // A settle timer outliving the screen would set state on an unmounted tree.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const settle = useCallback(
    (next: CopyState) => {
      setOutcome({ label, state: next });
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        setOutcome(null);
      }, SETTLE_MS);
    },
    [label],
  );

  const onClick = useCallback(() => {
    const value = text();
    void copyText(value).then((copied) => {
      settle(copied ? 'copied' : 'failed');
    });
  }, [text, settle]);

  return (
    <div className="copy">
      {/*
        The button is the confirmation: it reads "Copied" for a moment, where
        the thumb already is. `aria-live` on the button would announce every
        re-render, so the announcement is the status line below instead.
      */}
      <button
        type="button"
        className={className ?? 'screen__action'}
        data-state={state}
        onClick={onClick}
      >
        {state === 'copied' ? 'Copied' : label}
      </button>

      {/*
        Polite, and only visible when it has something the button does not
        already say. Sighted feedback lives inline, so the failure
        renders where the user is looking; the success is already on the
        button and is announced from here without being shown twice.
      */}
      {state !== 'idle' && (
        <p
          className={state === 'copied' ? 'copy__result visually-hidden' : 'copy__result'}
          data-state={state}
          role="status"
          aria-live="polite"
        >
          {state === 'copied'
            ? 'Copied'
            : 'Could not copy — this browser would not allow it. Select the text and copy it by hand.'}
        </p>
      )}
    </div>
  );
}
