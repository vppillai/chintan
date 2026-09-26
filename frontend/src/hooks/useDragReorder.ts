import {
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';

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
 * Two things a finger needs. `touch-action` cannot change mid-gesture, so a
 * native non-passive `touchmove` listener cancels the page's scroll only
 * while a row is lifted — before that, a finger that moves is scrolling. And
 * the click the browser fires when the pointer lifts after a drag would
 * activate whatever is under it — open the note, tick the box — so the list
 * swallows exactly that one. Whether a lift that never moved swallows its
 * click too is the caller's (`swallowTap`): a row lifted by a hold must not
 * open on release, but a grip whose tap opens the row's menu needs the click.
 */

interface Drag<T> {
  pointerId: number;
  id: T;
  moved: boolean;
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
  draggingId: T | null;
  /** Lifts the row `id` under this pointer. A second lift while one is on is ignored. */
  start: (pointerId: number, id: T) => void;
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
  swallowTap = true,
}: {
  listRef: RefObject<HTMLElement | null>;
  /** The ids in the order shown, one per row with `data-drag-id`. */
  ids: readonly T[];
  onCommit: (next: T[], moved: T) => void;
  /** Whether the click after a lift that never moved is swallowed as well as the one after a drag. */
  swallowTap?: boolean;
}): DragReorder<T> {
  const [draft, setDraft] = useState<T[] | null>(null);
  const [draggingId, setDraggingId] = useState<T | null>(null);
  const drag = useRef<Drag<T> | null>(null);
  const live = useRef<T[]>([]);
  const swallowClick = useRef(false);

  const start = (pointerId: number, id: T): void => {
    if (drag.current) return;
    drag.current = { pointerId, id, moved: false };
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
    try {
      listRef.current?.releasePointerCapture(pointerId);
    } catch {
      /* Already released. */
    }
    // The pointer lifting after a drag is not a tap on what is under it.
    swallowClick.current = current.moved || swallowTap;
    if (cancelled || !current.moved) return;
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
    onPointerDownCapture: () => {
      swallowClick.current = false;
    },
    onPointerMove: (event) => {
      const current = drag.current;
      if (!current || current.pointerId !== event.pointerId) return;
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
      if (!swallowClick.current) return;
      swallowClick.current = false;
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

  return { draft, draggingId, start, listHandlers, step, cancel };
}
