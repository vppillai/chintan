import { useCallback, useRef, type CSSProperties, type ReactNode } from 'react';

import { useMediaQuery } from '@/hooks/useMediaQuery.ts';
import { useSwipeActions } from '@/hooks/useSwipeActions.ts';

import { Icon, type IconName } from './Icon.tsx';

/**
 * A row that slides aside to reveal a tray of actions — the phone's way of
 * getting at "delete" without a menu (backlog N8).
 *
 * The tray sits behind the row's trailing edge and is exactly as wide as the
 * row has been dragged, clipping its own buttons, so the nearest action
 * appears first and the content above it never has to be clipped — which
 * matters, because clipping the content would cut off the focus ring of the
 * button inside it. The gesture itself is `useSwipeActions`.
 *
 * Not for a pointer that can hover and point precisely: the desktop already
 * has the hover checkbox and the overflow menu, and a mouse drag on a row
 * would fight text selection and the scrollbar. Off, too, while bulk-select is
 * on — the row is a checkbox then, and one gesture per row is enough.
 *
 * The tray's buttons are real buttons, so a screen reader that lands on them
 * finds controls, but they are `aria-hidden` and `inert` while the tray is
 * closed: a control that is invisible and half a screen off to the right
 * must not be in the tab order. Every action the tray offers is reachable
 * another way — the long press, the overflow menu, the note's own action bar
 * — so nothing is only a swipe away.
 */

export interface SwipeAction {
  id: string;
  label: string;
  icon: IconName;
  destructive?: boolean;
  onSelect: () => void;
}

/** Each tray button's width when the tray cannot be measured (jsdom). */
export const SWIPE_ACTION_FALLBACK_PX = 72;

/** A mouse or trackpad: the swipe is not offered, the hover controls are. */
export const FINE_POINTER_QUERY = '(hover: hover) and (pointer: fine)';

export function SwipeRow({
  actions,
  disabled = false,
  label,
  className,
  contentClassName,
  children,
}: {
  actions: readonly SwipeAction[];
  disabled?: boolean;
  /** The tray's accessible name: "Actions for Roof repair". */
  label: string;
  className?: string;
  contentClassName?: string;
  children: ReactNode;
}) {
  const finePointer = useMediaQuery(FINE_POINTER_QUERY);
  const enabled = !disabled && !finePointer && actions.length > 0;
  const rootRef = useRef<HTMLDivElement>(null);
  const actionsRef = useRef<HTMLDivElement>(null);

  const measureWidth = useCallback(
    () => actionsRef.current?.offsetWidth || actions.length * SWIPE_ACTION_FALLBACK_PX,
    [actions.length],
  );
  const swipe = useSwipeActions({ enabled, measureWidth, containerRef: rootRef });

  if (!enabled) {
    return (
      <div ref={rootRef} className={join('swipe', className)}>
        <div className={join('swipe__content', contentClassName)}>{children}</div>
      </div>
    );
  }

  return (
    <div
      ref={rootRef}
      className={join('swipe', className)}
      data-open={swipe.open || undefined}
      data-dragging={swipe.dragging || undefined}
      data-shifted={swipe.offset !== 0 || undefined}
      style={{ '--swipe-x': `${String(swipe.offset)}px` } as CSSProperties}
      {...swipe.handlers}
    >
      <div
        className="swipe__tray"
        role="group"
        aria-label={label}
        aria-hidden={!swipe.open}
        inert={!swipe.open}
      >
        <div ref={actionsRef} className="swipe__actions">
          {actions.map((action) => (
            <button
              key={action.id}
              type="button"
              className="swipe__action"
              data-destructive={action.destructive || undefined}
              onClick={() => {
                // Closed first: the action may open a dialog, and a row that is
                // still uncovered behind a dialog is a row with two states.
                swipe.close();
                action.onSelect();
              }}
            >
              <Icon name={action.icon} size={20} />
              <span className="swipe__action-label">{action.label}</span>
            </button>
          ))}
        </div>
      </div>
      <div
        className={join('swipe__content', contentClassName)}
        onClickCapture={swipe.onContentClickCapture}
      >
        {children}
      </div>
    </div>
  );
}

function join(...names: (string | undefined)[]): string {
  return names.filter(Boolean).join(' ');
}
