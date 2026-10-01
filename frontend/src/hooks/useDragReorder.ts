import {
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';

import { GESTURE_SLOP_PX } from './gesture.ts';
import { useSwallowNextClick } from './swallowNextClick.ts';

/**
 * A drag that reorders the rows of one list, held as a draft until the
 * pointer lifts, then handed to the caller as one new order.
 *
 * The gesture is delegated to the list rather than owned by each row. One
 * pointer capture for the whole list is less to get wrong than one per row,
 * and capture on the list is what keeps the moves arriving once the dragged
 * row has moved out from under the pointer: the row that started the drag
 * is by then somewhere else on the screen. The dragged row takes the slot
 * whose midpoint the pointer crosses, so the list re-sorts under the pointer
 * as it moves; nothing is committed until release, and Escape, a
 * `pointercancel` or the browser taking the capture away drops the row where
 * it was.
 *
 * The caller decides what a lift means. It calls `start` from whatever
 * pointerdown it deems a lift — a grip, or a hold on the row on a phone —
 * and gives the list `listHandlers` for everything after. The rows it renders
 * carry `data-drag-id` with the id the caller uses, in the order `draft`
 * says while one is lifted. `onCommit` gets the whole new order and the id
 * that moved: a caller that stores a permutation needs the first, one that
 * rewrites a text body needs to know which line moved to where.
 *
 * A caller whose rows have levels — the checklist — passes `onShift` and the
 * pointer's origin to `start`, and the drag then has two axes. The first
 * `GESTURE_SLOP_PX` px of travel decide which: more sideways than up and down and
 * the row keeps its slot while every indent step right or left (`levelPx`:
 * `--space-6` in the page's own pixels) is one level in or out
 * (`draftShift`, for the caller to preview); otherwise it is the reorder
 * above. The levels are not clamped here: the hook knows no depths, so the
 * caller clamps them to where its row may go, and one release can move
 * several levels. Release on the sideways axis calls `onShift` with the
 * levels, or nothing when the pointer came back under a step. Locked once decided, so a vertical drag that drifts sideways
 * never changes the level and a sideways one never re-sorts; and a drag past
 * the slop on either axis is no longer a tap, so a wobble on the handle does
 * not open its menu. Without `onShift` or an origin — the pinned group —
 * every move is vertical and there is no slop, as before.
 *
 * Two things a finger needs. `touch-action` cannot change mid-gesture, so a
 * native non-passive `touchmove` listener cancels the page's scroll only
 * while a row is lifted — before that, a finger that moves is scrolling. And
 * the click the browser fires when the pointer lifts after a lift, moved or
 * not, would activate whatever is under it — open the note, tick the box —
 * so the list swallows exactly that one (`useSwallowNextClick`). A lift that never moved is a tap
 * on the handle, and the caller hears of it as `onTap` (the grip's menu);
 * it cannot use the browser's click for that, because once the list holds
 * the capture that click is targeted at the list, not at the handle.
 */

/**
 * Sideways travel per level: the indent step `--space-6` (checklist.css
 * draws it) in CSS pixels, read when the row is lifted. The token is in rem,
 * so under the browser's text-size setting the real indent is wider than 24
 * device pixels and the preview must move by the same amount (DB6-32). The
 * token's own value when the sheet cannot be read (tests).
 */
function levelPx(): number {
  const root = getComputedStyle(document.documentElement);
  const value = root.getPropertyValue('--space-6').trim();
  const n = Number.parseFloat(value);
  if (!Number.isFinite(n) || n <= 0) return 24;
  return value.endsWith('rem') ? n * (Number.parseFloat(root.fontSize) || 16) : n;
}

interface Drag<T> {
  pointerId: number;
  id: T;
  moved: boolean;
  /** Where the pointer went down, when the caller said; the axis is decided from here. */
  origin: { x: number; y: number } | null;
  axis: 'undecided' | 'x' | 'y';
  levels: number;
  /** One level's worth of sideways travel, as the sheet had it at the lift. */
  levelPx: number;
  /** The order when the row was lifted; a release that leaves it commits nothing. */
  order: T[];
}

export interface DragReorderHandlers {
  onPointerDownCapture: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
  onLostPointerCapture: (event: ReactPointerEvent<HTMLElement>) => void;
  onClickCapture: (event: ReactMouseEvent<HTMLElement>) => void;
  onContextMenu: (event: ReactMouseEvent<HTMLElement>) => void;
}

export interface DragReorder<T extends string> {
  /** The order while a row is lifted; null when none is. */
  draft: readonly T[] | null;
  /** The level change a sideways drag would make on release; null when none is on. */
  draftShift: { id: T; levels: number } | null;
  draggingId: T | null;
  /**
   * Lifts the row `id` under this pointer. A second lift while one is on is
   * ignored. `origin` is where the pointer went down; with `onShift`, it is
   * what makes the drag two-axis.
   */
  start: (pointerId: number, id: T, origin?: { x: number; y: number }) => void;
  listHandlers: DragReorderHandlers;
  /** One slot up (-1) or down (+1) for the row, committed at once: the arrow keys, a menu item. */
  step: (id: T, by: -1 | 1) => void;
  /** Drops a lifted row where it was, committing nothing. */
  cancel: () => void;
}

export function useDragReorder<T extends string>({
  listRef,
  ids,
  onCommit,
  onTap,
  onShift,
}: {
  listRef: RefObject<HTMLElement | null>;
  /** The ids in the order shown, one per row with `data-drag-id`. */
  ids: readonly T[];
  onCommit: (next: T[], moved: T) => void;
  /** A lift that ended where it began: the pointer tapped the handle of row `id`. */
  onTap?: (id: T) => void;
  /**
   * A sideways drag released `levels` indent steps right (positive) or left
   * (negative) of where it began.
   */
  onShift?: (id: T, levels: number) => void;
}): DragReorder<T> {
  const [draft, setDraft] = useState<T[] | null>(null);
  const [draftShift, setDraftShift] = useState<{ id: T; levels: number } | null>(null);
  const [draggingId, setDraggingId] = useState<T | null>(null);
  const drag = useRef<Drag<T> | null>(null);
  const live = useRef<T[]>([]);
  const swallow = useSwallowNextClick();

  const start = (pointerId: number, id: T, origin?: { x: number; y: number }): void => {
    if (drag.current) return;
    drag.current = {
      pointerId,
      id,
      moved: false,
      origin: origin ?? null,
      axis: onShift && origin ? 'undecided' : 'y',
      levels: 0,
      levelPx: levelPx(),
      order: [...ids],
    };
    live.current = [...ids];
    setDraft([...ids]);
    setDraggingId(id);
    try {
      listRef.current?.setPointerCapture(pointerId);
    } catch {
      /* jsdom, or a pointer the browser has already released; the drag still works. */
    }
  };

  const finish = (pointerId: number, cancelled: boolean): void => {
    const current = drag.current;
    if (!current || current.pointerId !== pointerId) return;
    drag.current = null;
    setDraggingId(null);
    setDraft(null);
    setDraftShift(null);
    try {
      listRef.current?.releasePointerCapture(pointerId);
    } catch {
      /* Already released. */
    }
    // Told before the flag below is set: what the caller does with a tap —
    // click its own handle to open the row's menu — is a click through this
    // list too, and must not be the one swallowed.
    if (!cancelled && !current.moved) onTap?.(current.id);
    // Whether it moved or not, the pointer lifting after a lifted row is not
    // a tap on what is under it.
    swallow.arm();
    if (cancelled || !current.moved) return;
    if (current.axis === 'x') {
      if (current.levels !== 0) onShift?.(current.id, current.levels);
      return;
    }
    // Past the slop is moved, so a tap it is not; but a vertical drag that
    // crossed no midpoint left the order as it was, and the caller is not
    // asked to write what it already has.
    if (live.current.every((id, index) => id === current.order[index])) return;
    onCommit(live.current, current.id);
  };

  const cancel = (): void => {
    if (drag.current) finish(drag.current.pointerId, true);
  };

  const step = (id: T, by: -1 | 1): void => {
    const order = draft ?? ids;
    const from = order.indexOf(id);
    const to = from + by;
    if (from === -1 || to < 0 || to >= order.length) return;
    const next = order.filter((other) => other !== id);
    next.splice(to, 0, id);
    onCommit(next, id);
  };

  const listHandlers: DragReorderHandlers = {
    // A new press clears a swallow no click ever consumed (a cancelled drag).
    onPointerDownCapture: swallow.reset,
    onPointerMove: (event) => {
      const current = drag.current;
      if (!current || current.pointerId !== event.pointerId) return;
      const dx = current.origin ? event.clientX - current.origin.x : 0;
      const dy = current.origin ? event.clientY - current.origin.y : 0;
      if (current.axis === 'undecided') {
        if (Math.hypot(dx, dy) <= GESTURE_SLOP_PX) return;
        current.axis = Math.abs(dx) > Math.abs(dy) ? 'x' : 'y';
        // Decided is moved: past the slop on either axis, the lift is no tap.
        current.moved = true;
      }
      if (current.axis === 'x') {
        // `|| 0` folds the -0 a small leftward trunc gives into plain 0.
        const levels = Math.trunc(dx / current.levelPx) || 0;
        if (levels === current.levels) return;
        current.levels = levels;
        setDraftShift({ id: current.id, levels });
        return;
      }
      const rows = Array.from(
        listRef.current?.querySelectorAll<HTMLElement>('[data-drag-id]') ?? [],
      );
      const from = rows.findIndex((row) => row.dataset['dragId'] === current.id);
      if (from === -1) return;
      // The slot whose midpoint the pointer has crossed: the nearest above
      // when moving up, the farthest below when moving down.
      let to = from;
      rows.forEach((row, index) => {
        const box = row.getBoundingClientRect();
        const middle = box.top + box.height / 2;
        if (index < from && to === from && event.clientY < middle) to = index;
        if (index > from && event.clientY > middle) to = index;
      });
      if (to === from) return;
      current.moved = true;
      const next = live.current.filter((id) => id !== current.id);
      next.splice(to, 0, current.id);
      live.current = next;
      setDraft(next);
    },
    onPointerUp: (event) => {
      finish(event.pointerId, false);
    },
    onPointerCancel: (event) => {
      finish(event.pointerId, true);
    },
    onLostPointerCapture: (event) => {
      // Only the list losing its own capture is the browser taking the drag
      // away; a row's implicit capture handing over is not.
      if (event.target === event.currentTarget) finish(event.pointerId, true);
    },
    onClickCapture: (event) => {
      if (!swallow.take(event)) return;
      event.preventDefault();
      event.stopPropagation();
    },
    onContextMenu: (event) => {
      // Android raises its menu for the same press; it is also where text
      // selection begins.
      if (drag.current) event.preventDefault();
    },
  };

  // The scroll a finger would start is cancelled only while a row is lifted.
  useEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const block = (event: TouchEvent): void => {
      if (drag.current && event.cancelable) event.preventDefault();
    };
    list.addEventListener('touchmove', block, { passive: false });
    return () => {
      list.removeEventListener('touchmove', block);
    };
  }, [listRef]);

  // Escape drops the row where it was.
  useEffect(() => {
    if (draggingId === null) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape' && drag.current) finish(drag.current.pointerId, true);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
    };
  });

  return { draft, draftShift, draggingId, start, listHandlers, step, cancel };
}
