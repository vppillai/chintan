import { useCallback, useEffect, useId, useRef } from 'react';

/**
 * The app's only modal, and it is a real one (spec §5.7): `role="dialog"`,
 * `aria-modal`, a focus trap, Escape to dismiss, an inert background, and
 * focus restored to whatever opened it.
 *
 * Hand-rolled rather than `<dialog showModal()>` deliberately. The native
 * element gives the trap and Escape for free but its top-layer backdrop
 * escapes the theme tokens, and `showModal` is unimplemented in jsdom — which
 * would make the gate on the app's destructive actions the one component that
 * cannot be unit-tested. Sixty lines is a fair price for testability here.
 */

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  cancelLabel?: string;
  /** Styles the confirm control as the dangerous option. */
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  cancelLabel = 'Cancel',
  destructive = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  const titleId = useId();
  const bodyId = useId();

  const focusables = useCallback((): HTMLElement[] => {
    const panel = panelRef.current;
    if (!panel) return [];
    return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
  }, []);

  // Remember what had focus, move into the dialog, and put it back on close.
  // Without the restore, dismissing a dialog drops a keyboard user at the top
  // of the document.
  useEffect(() => {
    if (!open) return;
    restoreRef.current = document.activeElement as HTMLElement | null;
    // Cancel first, not confirm: the safe option should be under the thumb
    // and under the Enter key of someone who opened this by accident.
    const [first] = focusables();
    first?.focus();

    return () => {
      restoreRef.current?.focus?.();
    };
  }, [open, focusables]);

  useEffect(() => {
    if (!open) return;

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
  }, [open, onCancel, focusables]);

  if (!open) return null;

  return (
    <div className="dialog-layer">
      {/*
        The scrim is not a button: a click target that dismisses a destructive
        confirmation is how people delete things by accident. Escape and the
        explicit Cancel are the two ways out.
      */}
      <div className="dialog-scrim" aria-hidden="true" />

      <div
        ref={panelRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
      >
        <h2 id={titleId} className="dialog__title">
          {title}
        </h2>
        <p id={bodyId} className="dialog__body">
          {body}
        </p>
        <div className="dialog__actions">
          <button type="button" className="dialog__action" onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            type="button"
            className={`dialog__action ${
              destructive ? 'dialog__action--destructive' : 'dialog__action--primary'
            }`}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
