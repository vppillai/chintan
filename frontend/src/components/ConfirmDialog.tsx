import { useCallback, useEffect, useId, useRef, useState } from 'react';

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
  /**
   * Text the user must type before the confirm control unlocks.
   *
   * For the one action in the app that cannot be undone. A dialog whose confirm
   * button sits where "OK" usually sits is dismissed by muscle memory; typing
   * the note's own title is the smallest thing that makes deleting it a
   * deliberate act. Matched case-insensitively and trimmed — this is a speed
   * bump, not a password.
   */
  requireText?: string;
  /** Labels the typing field. Ignored unless `requireText` is set. */
  requireLabel?: string;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * The open/closed switch, and nothing else.
 *
 * The panel is a separate component so that everything inside it — the focus
 * trap, the key listener, and the typed-confirmation field — exists only while
 * the dialog is on screen. That is what makes the typing field reset itself: a
 * dialog that was cancelled half-typed unmounts, so reopening it cannot arrive
 * already unlocked, and no effect has to reach in and clear anything.
 */
export function ConfirmDialog({ open, ...rest }: ConfirmDialogProps) {
  if (!open) return null;
  return <DialogPanel {...rest} />;
}

function DialogPanel({
  title,
  body,
  confirmLabel,
  cancelLabel = 'Cancel',
  destructive = false,
  requireText,
  requireLabel,
  onConfirm,
  onCancel,
}: Omit<ConfirmDialogProps, 'open'>) {
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  const titleId = useId();
  const bodyId = useId();
  const confirmTextId = useId();
  const [typed, setTyped] = useState('');

  const unlocked =
    !requireText || typed.trim().toLowerCase() === requireText.trim().toLowerCase();

  const focusables = useCallback((): HTMLElement[] => {
    const panel = panelRef.current;
    if (!panel) return [];
    return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
  }, []);

  // Remember what had focus, move into the dialog, and put it back on close.
  // Without the restore, dismissing a dialog drops a keyboard user at the top
  // of the document.
  useEffect(() => {
    restoreRef.current = document.activeElement as HTMLElement | null;
    // The first focusable, which is Cancel on a plain dialog and the typing
    // field on a gated one. Never the confirm control: the safe option should
    // be under the thumb and under the Enter key of someone who opened this by
    // accident, and on a gated dialog the confirm is disabled anyway.
    const [first] = focusables();
    first?.focus();

    return () => {
      restoreRef.current?.focus?.();
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

        {requireText && (
          <div className="dialog__gate">
            <label className="dialog__gate-label" htmlFor={confirmTextId}>
              {requireLabel ?? `Type ${requireText} to confirm`}
            </label>
            <input
              id={confirmTextId}
              className="dialog__gate-input"
              type="text"
              value={typed}
              autoComplete="off"
              autoCorrect="off"
              spellCheck={false}
              onChange={(event) => {
                setTyped(event.target.value);
              }}
            />
          </div>
        )}

        <div className="dialog__actions">
          <button type="button" className="dialog__action" onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            type="button"
            className={`dialog__action ${
              destructive ? 'dialog__action--destructive' : 'dialog__action--primary'
            }`}
            disabled={!unlocked}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
