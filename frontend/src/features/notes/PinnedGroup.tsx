import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';

import { useReorderPins } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';
import { NoteRow, type SelectOptions } from '@/components/NoteRow.tsx';
import { FINE_POINTER_QUERY } from '@/components/SwipeRow.tsx';
import { useDragReorder } from '@/hooks/useDragReorder.ts';
import { LONG_PRESS_MS, LONG_PRESS_TOLERANCE_PX } from '@/hooks/useLongPress.ts';
import { useMediaQuery } from '@/hooks/useMediaQuery.ts';
import { useOnline } from '@/hooks/useOnline.ts';

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
 * keys, one step per press, for a keyboard; and every pinned row's ⋮ offers
 * Move up and Move down, the one path that needs no drag at all (WCAG 2.5.7)
 * and the only visible sign on a phone that the order can change.
 *
 * Under a tag or Checklists chip the group is a subset of the pinned notes,
 * and the server ranks exactly the ids it is sent, from 0: moving one row
 * inside the filter hoisted the visible rows above every pinned note the
 * filter hid (review 2026-09-24, R4-2). So the screen says whether the whole
 * group is here (`reorderable`), and where it is not, nothing lifts and no
 * grip is drawn. Offline the reorder would pause and fire later against a
 * cache that has moved on, so it waits for the network too (R4-11).
 *
 * The drag itself — capture on the list, the slot by midpoint, the draft
 * until release, Escape, the swallowed click — is `useDragReorder`, shared
 * with the Items tab. What is this group's own: the hold that lifts a row on
 * a phone (one timer for the whole list; before it fires, a finger that moves
 * is scrolling, and the hold lets go), the grip-versus-row routing of a
 * press, the gating, and the one request. The hook swallows the click that
 * follows any lift, moved or not, so releasing a finger that held a row
 * does not open the note it had lifted.
 */

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
  reorderable = true,
  selectedIds,
  onToggleSelect,
}: {
  /** The pinned notes, already in `pin_rank` order (`splitPinned`). */
  notes: readonly NoteWire[];
  selectable?: boolean;
  /** Whether `notes` is every pinned note, so an order made here is the whole order. */
  reorderable?: boolean;
  selectedIds: ReadonlySet<string>;
  onToggleSelect: (noteId: string, options: SelectOptions) => void;
}) {
  const reorder = useReorderPins();
  const finePointer = useMediaQuery(FINE_POINTER_QUERY);
  const online = useOnline();
  const hintId = useId();
  const listRef = useRef<HTMLUListElement>(null);
  /** The order while its request is in the air, so the rows never spring back before the optimistic patch lands. */
  const [pending, setPending] = useState<string[] | null>(null);
  const hold = useRef<Hold | null>(null);

  const ids = notes.map((note) => note.id);
  const shown = pending ?? ids;
  const byId = new Map(notes.map((note) => [note.id, note]));
  const grips = finePointer && !selectable && reorderable;
  const movable = reorderable && !selectable;
  /** Whether a pointer can lift a row at all right now. */
  const lifts = movable && online;

  const commit = (next: string[]): void => {
    if (next.join('\n') === ids.join('\n')) {
      setPending(null);
      return;
    }
    setPending(next);
    // The optimistic patch has re-ranked the rows by the time this settles,
    // or the rollback has restored them; either way the props carry the
    // order to show, and the pending order steps aside.
    void reorder
      .mutateAsync(next)
      .catch(() => undefined)
      .finally(() => {
        setPending(null);
      });
  };

  const drag = useDragReorder({ listRef, ids: shown, onCommit: commit });
  const order = drag.draft ?? shown;
  const rows = order.flatMap((id) => {
    const note = byId.get(id);
    return note ? [note] : [];
  });

  const cancelHold = (): void => {
    if (hold.current) clearTimeout(hold.current.timer);
    hold.current = null;
  };

  const start = (pointerId: number, id: string): void => {
    cancelHold();
    drag.start(pointerId, id);
  };

  /** The hold's pointer lifting or leaving: the hold is over, whatever the drag does. */
  const releaseHold = (event: ReactPointerEvent<HTMLUListElement>): void => {
    if (hold.current?.pointerId === event.pointerId) cancelHold();
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLUListElement>): void => {
    if (!lifts || event.button !== 0 || drag.draggingId !== null) return;
    const target = event.target as HTMLElement;
    const id = target.closest<HTMLElement>('[data-drag-id]')?.dataset['dragId'];
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
    drag.listHandlers.onPointerMove(event);
  };

  const onGripKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, id: string): void => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault();
    drag.step(id, event.key === 'ArrowUp' ? -1 : 1);
  };

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
        data-dragging={drag.draggingId !== null || undefined}
        {...drag.listHandlers}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={(event) => {
          releaseHold(event);
          drag.listHandlers.onPointerUp(event);
        }}
        onPointerCancel={(event) => {
          releaseHold(event);
          drag.listHandlers.onPointerCancel(event);
        }}
        onLostPointerCapture={(event) => {
          if (event.target === event.currentTarget) releaseHold(event);
          drag.listHandlers.onLostPointerCapture(event);
        }}
        onContextMenu={(event) => {
          // Android raises its menu for the hold too.
          if (hold.current) event.preventDefault();
          drag.listHandlers.onContextMenu(event);
        }}
      >
        {rows.map((note, index) => (
          <li
            key={note.id}
            className="pin-row"
            data-drag-id={note.id}
            data-grip={grips || undefined}
            data-dragging={drag.draggingId === note.id || undefined}
          >
            {/*
              Before the row in the DOM, as it is on the screen: Tab then runs
              grip → row → ⋮ instead of row → ⋮ at the right edge → back to the
              grip at the left (WCAG 2.4.3; review 2026-09-24, R4-13). The grip
              is absolutely positioned, so nothing moves.
            */}
            {grips && (
              <button
                type="button"
                className="pin-grip"
                aria-label={`Move ${note.title}`}
                aria-describedby={hintId}
                disabled={!online}
                onKeyDown={(event) => {
                  onGripKeyDown(event, note.id);
                }}
              >
                <Icon name="grip" size={18} />
              </button>
            )}
            <NoteRow
              note={note}
              selectable={selectable}
              selected={selectedIds.has(note.id)}
              onToggleSelect={onToggleSelect}
              // Under a finger the hold lifts the row; with a mouse it selects,
              // and the grip is the handle. Where nothing lifts — a filter is
              // on, or the device is offline — the hold selects, as everywhere.
              holdToSelect={finePointer || !lifts}
              {...(movable && index > 0 ? { onMoveUp: () => drag.step(note.id, -1) } : {})}
              {...(movable && index < rows.length - 1
                ? { onMoveDown: () => drag.step(note.id, 1) }
                : {})}
            />
          </li>
        ))}
      </ul>
    </section>
  );
}
