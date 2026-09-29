import { useId, useRef } from 'react';

import { useModalFocus } from './useModalFocus.ts';

/**
 * The app's only modal, and it is a real one: `role="dialog"`,
 * `aria-modal`, a focus trap, Escape to dismiss, an inert background, and
 * focus restored to whatever opened it.
 *
 * Hand-rolled rather than `<dialog showModal()>` deliberately. The native
 * element gives the trap and Escape for free but its top-layer backdrop
 * escapes the theme tokens, and `showModal` is unimplemented in jsdom — which
 * would make the gate on the app's destructive actions the one component that
 * cannot be unit-tested. The trap itself lives in `useModalFocus`, shared with
 * the note screen's "Move to…" sheet.
 *
 * Focus lands on Cancel: the safe option should be under the Enter key of
 * someone who opened this by accident. There is no typed word (owner,
 * 2026-09-26: "I have to type delete. I don't like that UX"); the sentence
 * says what goes.
 */

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

/**
 * The open/closed switch, and nothing else.
 *
 * The panel is a separate component so that everything inside it — the focus
 * trap, the key listener — exists only while the dialog is on screen, and a
 * dialog that was cancelled unmounts rather than having to reset itself.
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
  onConfirm,
  onCancel,
}: Omit<ConfirmDialogProps, 'open'>) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const bodyId = useId();

  useModalFocus(panelRef, onCancel);

  const confirmClass = `dialog__action ${
    destructive ? 'dialog__action--destructive' : 'dialog__action--primary'
  }`;

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
          <button type="button" className={confirmClass} onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
