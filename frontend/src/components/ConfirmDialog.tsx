import {
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
} from 'react';

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
 * says what goes, and the one action large enough to want a second gesture —
 * emptying an archive of more than ten notes — asks for a press held for
 * `holdMs` instead.
 */

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  cancelLabel?: string;
  /** Styles the confirm control as the dangerous option. */
  destructive?: boolean;
  /**
   * Press and hold for this long to confirm, instead of a tap. For a bulk
   * delete forever of many notes: a tap can be a slip, a second held down
   * cannot. A finger, a mouse button, or Space or Enter held on the keyboard;
   * the fill across the button shows how far along the hold is, and jumps to
   * full under reduced motion.
   */
  holdMs?: number | undefined;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * The open/closed switch, and nothing else.
 *
 * The panel is a separate component so that everything inside it — the focus
 * trap, the key listener, a hold in progress — exists only while the dialog
 * is on screen, and a dialog that was cancelled unmounts rather than having
 * to reset itself.
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
  holdMs,
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
          {holdMs ? (
            <HoldButton className={confirmClass} ms={holdMs} onHeld={onConfirm}>
              {confirmLabel}
            </HoldButton>
          ) : (
            <button type="button" className={confirmClass} onClick={onConfirm}>
              {confirmLabel}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * A button that fires only once it has been held down for `ms`.
 *
 * Its own timer rather than `useLongPress`: that hook is a gesture on a row —
 * pointer only, cancelled by travel, with a click to swallow afterwards — and
 * this is a control, which a keyboard must be able to hold too. Space and
 * Enter arm it on the way down and disarm on the way up, and their default
 * click is suppressed so a tap of either cannot confirm on its own; the
 * pointer's click after a completed hold lands on a dialog that has closed.
 * Leaving the button, losing focus or the pointer being cancelled all disarm
 * it, so a hold is a hold and nothing else.
 */
function HoldButton({
  className,
  ms,
  onHeld,
  children,
}: {
  className: string;
  ms: number;
  onHeld: () => void;
  children: string;
}) {
  const [holding, setHolding] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const release = (): void => {
    if (!timer.current) return;
    clearTimeout(timer.current);
    timer.current = null;
    setHolding(false);
  };
  const press = (): void => {
    if (timer.current) return;
    setHolding(true);
    timer.current = setTimeout(() => {
      timer.current = null;
      setHolding(false);
      onHeld();
    }, ms);
  };
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const isHoldKey = (event: KeyboardEvent<HTMLButtonElement>): boolean =>
    event.key === ' ' || event.key === 'Enter';

  return (
    <button
      type="button"
      className={`${className} dialog__action--hold`}
      data-holding={holding || undefined}
      style={{ '--hold-ms': `${String(ms)}ms` } as CSSProperties}
      onPointerDown={(event: PointerEvent<HTMLButtonElement>) => {
        if (event.button === 0) press();
      }}
      onPointerUp={release}
      onPointerCancel={release}
      onPointerLeave={release}
      onBlur={release}
      onKeyDown={(event) => {
        if (!isHoldKey(event)) return;
        event.preventDefault();
        if (!event.repeat) press();
      }}
      onKeyUp={(event) => {
        if (!isHoldKey(event)) return;
        event.preventDefault();
        release();
      }}
      onClick={(event) => {
        event.preventDefault();
      }}
      onContextMenu={(event) => {
        event.preventDefault();
      }}
    >
      <span className="dialog__hold-fill" aria-hidden="true" />
      {children}
    </button>
  );
}
