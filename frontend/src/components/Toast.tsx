import { useEffect, useSyncExternalStore } from 'react';

/**
 * The one transient notice, with the one action it can carry: "Deleted · kept
 * in Archive for 30 days — Undo".
 *
 * A shell row above the tab bar, not a floating card. The update prompt made
 * the case (`.update-prompt`): a `position: fixed` toast sat on the record
 * button, the one control the product exists to offer. In flow it can cover
 * nothing, and it survives a route change because the shell does — a note
 * deleted from its own screen shows its Undo on the library it lands on.
 *
 * The region is always mounted and empty when quiet, like `StatusRegion`: a
 * live region added to the DOM together with its text is frequently not
 * announced. Undo is a real button, so a keyboard reaches it and a screen
 * reader is told there is one; it hides itself after `TOAST_MS`, long enough
 * to read the sentence and find the control, short enough that a stale Undo
 * is not lying in wait under a thumb.
 *
 * A module store rather than a context (`useOnline` is the same shape): the
 * callers are a row that unmounts as its note leaves the list and a menu on a
 * screen about to navigate away, and neither can hold a provider's setter
 * through that.
 */

export const TOAST_MS = 6000;

export interface ToastNotice {
  message: string;
  action?: { label: string; onSelect: () => void };
}

let current: ToastNotice | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

/** Shows this notice, replacing whatever was showing. */
export function showToast(notice: ToastNotice): void {
  current = notice;
  emit();
}

export function dismissToast(): void {
  if (!current) return;
  current = null;
  emit();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function snapshot(): ToastNotice | null {
  return current;
}

export function Toast() {
  const notice = useSyncExternalStore(subscribe, snapshot, () => null);

  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(dismissToast, TOAST_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [notice]);

  return (
    <div className="toast" role="status" aria-live="polite" aria-atomic="true">
      {notice && (
        <>
          <span className="toast__text">{notice.message}</span>
          {notice.action && (
            <button
              type="button"
              className="toast__action"
              onClick={() => {
                dismissToast();
                notice.action?.onSelect();
              }}
            >
              {notice.action.label}
            </button>
          )}
        </>
      )}
    </div>
  );
}
