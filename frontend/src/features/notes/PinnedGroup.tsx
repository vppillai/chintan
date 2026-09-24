import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';

import { useReorderPins } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';
import { NoteRow, type SelectOptions } from '@/components/NoteRow.tsx';
import { FINE_POINTER_QUERY } from '@/components/SwipeRow.tsx';
import { LONG_PRESS_MS, LONG_PRESS_TOLERANCE_PX } from '@/hooks/useLongPress.ts';
import { useMediaQuery } from '@/hooks/useMediaQuery.ts';

/**
 * The Pinned group at the top of Home (2026-09-24 contract, B): the notes the
 * person keeps there, in the order they put them, above the day groups.
 *
 * Reordering is a drag, and one request. Where the pointer is fine each row
 * wears a grip at its left edge and a mouse — or a finger on a touchscreen
 * laptop — drags it; on a phone there is no grip — the row's width is the
 * phone's — and the press-and-hold that selects a row everywhere else lifts a
 * pinned row instead, to be dragged (Select is still in the row's ⋮ menu).
 * The two holds never arm together: the row's selects exactly where
 * `holdToSelect` is on, and the list's lifts exactly where it is off, so a
 * finger on a hybrid device is not selected and lifted at once (review
 * 2026-09-24). The dragged row takes the slot whose midpoint the
 * pointer crosses, so the list re-sorts under the pointer as it moves; the
 * order is held here as a draft until the pointer lifts, then sent as one
 * `POST /v1/notes/pins`. The lists are patched optimistically by the
 * mutation, so the rows never spring back while the request is in the air,
 * and put back by it if the server refuses. The grip also takes the arrow
 * keys, one step per press, for a keyboard.
 *
 * The gesture is delegated to the list rather than owned by each row. One
 * hold timer and one pointer capture for the whole group is less to get wrong
 * than one per row, and capture on the list is what keeps the moves arriving
 * once the dragged row has moved out from under the pointer. A finger that
 * has lifted a row must not also scroll the page, and `touch-action` cannot
 * change mid-gesture, so a native non-passive `touchmove` listener cancels the
 * scroll only while a drag is on; before the hold fires, a finger that moves
 * is scrolling, and the hold lets go. The click the browser fires when the
 * pointer lifts after a drag, or after a hold that never moved, would open the
 * note under it; the list swallows exactly that one.
 */

interface Drag {
  pointerId: number;
  id: string;
  moved: boolean;
}

interface Hold {
  pointerId: number;
  id: string;
  x: number;
  y: number;
  timer: ReturnType<typeof setTimeout>;
}

export function PinnedGroup({
  notes,
  selectable = false,
  selectedIds,
  onToggleSelect,
}: {
  /** The pinned notes, already in `pin_rank` order (`splitPinned`). */
  notes: readonly NoteWire[];
  selectable?: boolean;
  selectedIds: ReadonlySet<string>;
  onToggleSelect: (noteId: string, options: SelectOptions) => void;
}) {
  const reorder = useReorderPins();
  const finePointer = useMediaQuery(FINE_POINTER_QUERY);
  const hintId = useId();
  const listRef = useRef<HTMLUListElement>(null);
  /** The order while a drag is on, or while its request is in the air. */
  const [draft, setDraft] = useState<string[] | null>(null);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const drag = useRef<Drag | null>(null);
  const hold = useRef<Hold | null>(null);
  const live = useRef<string[]>([]);
  const swallowClick = useRef(false);

  const ids = notes.map((note) => note.id);
  const order = draft ?? ids;
  const byId = new Map(notes.map((note) => [note.id, note]));
  const rows = order.flatMap((id) => {
    const note = byId.get(id);
    return note ? [note] : [];
  });
  const grips = finePointer && !selectable;

  const commit = (next: string[]): void => {
    if (next.join('\n') === ids.join('\n')) {
      setDraft(null);
      return;
    }
    setDraft(next);
    // The optimistic patch has re-ranked the rows by the time this settles,
    // or the rollback has restored them; either way the props carry the
    // order to show, and the draft steps aside.
    void reorder
      .mutateAsync(next)
      .catch(() => undefined)
      .finally(() => {
        setDraft(null);
      });
  };

  const cancelHold = (): void => {
    if (hold.current) clearTimeout(hold.current.timer);
    hold.current = null;
  };

  const start = (pointerId: number, id: string): void => {
    cancelHold();
    drag.current = { pointerId, id, moved: false };
    live.current = ids;
    setDraft(ids);
    setDraggingId(id);
    try {
      listRef.current?.setPointerCapture(pointerId);
    } catch {
      /* jsdom, or a pointer the browser has already released; the drag still works. */
    }
  };

  const finish = (pointerId: number, cancelled: boolean): void => {
    if (hold.current?.pointerId === pointerId) cancelHold();
    const current = drag.current;
    if (!current || current.pointerId !== pointerId) return;
    drag.current = null;
    setDraggingId(null);
    try {
      listRef.current?.releasePointerCapture(pointerId);
    } catch {
      /* Already released. */
    }
    // Whether it moved or not, the pointer lifting after a lifted row is not a tap.
    swallowClick.current = true;
    if (cancelled || !current.moved) {
      setDraft(null);
      return;
    }
    commit(live.current);
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLUListElement>): void => {
    swallowClick.current = false;
    if (selectable || event.button !== 0 || drag.current) return;
    const target = event.target as HTMLElement;
    const id = target.closest<HTMLElement>('[data-pin-id]')?.dataset['pinId'];
    if (!id) return;
    if (target.closest('.pin-grip')) {
      start(event.pointerId, id);
      return;
    }
    // The hold lifts the row itself, never its ⋮ or its swipe tray: a slow
    // press on the menu button must still open the menu when it lifts.
    if (!target.closest('.note-row')) return;
    // A mouse on the row is the row's: a hold there selects. So is a finger
    // wherever the pointer is fine (a touchscreen laptop): the row arms its
    // own hold there (`holdToSelect`), and the grip is the handle for both.
    if (event.pointerType === 'mouse' || finePointer) return;
    cancelHold();
    hold.current = {
      pointerId: event.pointerId,
      id,
      x: event.clientX,
      y: event.clientY,
      timer: setTimeout(() => {
        hold.current = null;
        if (typeof navigator !== 'undefined' && typeof navigator.vibrate === 'function') {
          navigator.vibrate(10);
        }
        start(event.pointerId, id);
      }, LONG_PRESS_MS),
    };
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLUListElement>): void => {
    const armed = hold.current;
    if (
      armed &&
      armed.pointerId === event.pointerId &&
      Math.hypot(event.clientX - armed.x, event.clientY - armed.y) > LONG_PRESS_TOLERANCE_PX
    ) {
      cancelHold();
    }
    const current = drag.current;
    if (!current || current.pointerId !== event.pointerId) return;
    const items = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>('[data-pin-id]') ?? [],
    );
    const from = items.findIndex((item) => item.dataset['pinId'] === current.id);
    if (from === -1) return;
    // The slot whose midpoint the pointer has crossed: the nearest above when
    // moving up, the farthest below when moving down.
    let to = from;
    items.forEach((item, index) => {
      const box = item.getBoundingClientRect();
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
  };

  const onGripKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, id: string): void => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault();
    const from = order.indexOf(id);
    const to = from + (event.key === 'ArrowUp' ? -1 : 1);
    if (from === -1 || to < 0 || to >= order.length) return;
    const next = order.filter((other) => other !== id);
    next.splice(to, 0, id);
    commit(next);
  };

  const onClickCapture = (event: ReactMouseEvent<HTMLUListElement>): void => {
    if (!swallowClick.current) return;
    swallowClick.current = false;
    event.preventDefault();
    event.stopPropagation();
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
  }, []);

  // Escape drops the row where it was.
  useEffect(() => {
    if (!draggingId) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape' && drag.current) finish(drag.current.pointerId, true);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
    };
  });

  useEffect(() => cancelHold, []);

  return (
    <section className="note-group note-group--pinned" aria-label="Pinned">
      <h2 className="note-group__label">Pinned</h2>
      {grips && (
        <p id={hintId} className="visually-hidden">
          To reorder, drag a handle, or focus it and press the up and down arrow keys.
        </p>
      )}
      <ul
        ref={listRef}
        className="note-list pin-list"
        role="list"
        data-dragging={draggingId !== null || undefined}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={(event) => {
          finish(event.pointerId, false);
        }}
        onPointerCancel={(event) => {
          finish(event.pointerId, true);
        }}
        onLostPointerCapture={(event) => {
          // Only the list losing its own capture is the browser taking the
          // drag away; the row's implicit capture handing over is not.
          if (event.target === event.currentTarget) finish(event.pointerId, true);
        }}
        onContextMenu={(event) => {
          // Android raises its menu for the same hold; it is also where text
          // selection begins.
          if (hold.current || drag.current) event.preventDefault();
        }}
        onClickCapture={onClickCapture}
      >
        {rows.map((note) => (
          <li
            key={note.id}
            className="pin-row"
            data-pin-id={note.id}
            data-grip={grips || undefined}
            data-dragging={draggingId === note.id || undefined}
          >
            <NoteRow
              note={note}
              selectable={selectable}
              selected={selectedIds.has(note.id)}
              onToggleSelect={onToggleSelect}
              // Under a finger the hold lifts the row; with a mouse it selects,
              // and the grip is the handle.
              holdToSelect={finePointer}
            />
            {grips && (
              <button
                type="button"
                className="pin-grip"
                aria-label={`Move ${note.title}`}
                aria-describedby={hintId}
                onKeyDown={(event) => {
                  onGripKeyDown(event, note.id);
                }}
              >
                <Icon name="grip" size={18} />
              </button>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
