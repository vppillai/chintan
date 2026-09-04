import { useCallback, useEffect, type RefObject } from 'react';

/**
 * What makes a panel a modal: focus moves in when it opens, Tab cannot leave
 * it, Escape dismisses it, and focus goes back where it came from on close.
 *
 * Extracted from `ConfirmDialog` when the note screen gained a second modal —
 * the "Move to…" sheet — so the two cannot drift apart on the one behaviour a
 * keyboard user depends on. Hand-rolled rather than `<dialog showModal()>` for
 * the reasons `ConfirmDialog` gives: the native top layer escapes the theme
 * tokens, and jsdom does not implement `showModal`, which would leave the
 * gate on every destructive action untestable.
 *
 * Mount the hook only while the panel is on screen — render the panel from a
 * component that exists only when open — so the effects run once per opening
 * and there is nothing to reset.
 */

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

export function useModalFocus(panelRef: RefObject<HTMLElement | null>, onCancel: () => void): void {
  const focusables = useCallback((): HTMLElement[] => {
    const panel = panelRef.current;
    if (!panel) return [];
    return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
  }, [panelRef]);

  // Remember what had focus, move into the panel, and put it back on close.
  // Without the restore, dismissing a dialog drops a keyboard user at the top
  // of the document.
  useEffect(() => {
    const restore = document.activeElement as HTMLElement | null;
    // The first focusable, which is Cancel on a plain dialog, the typing
    // field on a gated one and the search field on the move sheet. Never a
    // confirm control: the safe option should be under the thumb and under
    // the Enter key of someone who opened this by accident.
    const [first] = focusables();
    first?.focus();

    return () => {
      restore?.focus?.();
    };
  }, [focusables]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCancel();
        return;
      }
      if (event.key !== 'Tab') return;

      // The trap. Without it, Tab walks out of the dialog into a background
      // that is visually inert but still reachable.
      const elements = focusables();
      if (elements.length === 0) return;
      const first = elements[0];
      const last = elements[elements.length - 1];
      const active = document.activeElement;

      if (event.shiftKey && active === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first?.focus();
      }
    };

    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('keydown', onKeyDown, true);
    };
  }, [onCancel, focusables]);
}
