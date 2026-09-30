import { useEffect, useId, useMemo, useRef, useState } from 'react';

import { Icon } from '@/components/Icon.tsx';
import type { OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { showToast } from '@/components/Toast.tsx';
import { useDragReorder } from '@/hooks/useDragReorder.ts';

import { ChecklistDone } from './ChecklistDone.tsx';
import { ChecklistRow, ItemField } from './ChecklistRow.tsx';
import {
  blockOf,
  canNest,
  insertItemAfter,
  moveItem,
  nestUnder,
  parseChecklist,
  removeDone,
  removeItem,
  setItemText,
  toggleItem,
  uncheckAll,
  unnest,
  type ChecklistItem,
} from './checklist.ts';

/**
 * A checklist body as rows to tick off: the Items tab, and the Split up tab
 * over its proposal.
 *
 * Open items first, in body order, each a grip, a real checkbox and a text
 * field that wraps and grows with its words (`ChecklistRow`); an "Add an
 * item" row under them; then the done items under a Done heading, greyed
 * and struck through, each with its checkbox to reopen it and a × to delete
 * it (`ChecklistDone`). Ticking an item moves it down to Done, as Google
 * Keep does — the body keeps its order and only the item's own line changes
 * (`toggleItem`), so a recording the worker appends meanwhile lands where it
 * would have anyway.
 *
 * The grip is the one control for the order and the level (2026-09-26,
 * CL-3; 2026-09-29, R6-CL-2). A drag on it up or down lifts the row and the
 * list re-sorts under the pointer (`useDragReorder`, the pinned group's
 * gesture); nothing is written until release, then the body is rewritten
 * once (`moveItem`) and saved, as a tick is. A drag on it sideways — the
 * first ten pixels decide the axis, and it is locked from then on — keeps
 * the row in its slot and changes its level instead: one indent step
 * (24 px, `--space-6`) to the right makes it a sub-item of the open row
 * above, one to the left brings it up, one level either way, previewed on
 * the lifted row (`data-nest-preview`: the indent it would take and an
 * accent bar at its start) and written once on release through `nest`, the
 * same path as Tab. The up and down arrows on a focused grip move the row
 * one slot; the right and left arrows change its level. A tap on the grip
 * — a lift that never moved — opens the row's menu: Move up, Move down,
 * Move to top, Move to bottom, Make a sub-item, Move up a level, Delete;
 * the path that needs no drag at all (WCAG 2.5.7), and a menu on the grip
 * rather than a ⋮ per row because a phone's width has no room for both
 * beside a dictated sentence. A body that changes under a lifted row — a
 * recording landing by refetch — drops the row, since the slots it was
 * moving between are gone. Done rows have no grip: their order is the
 * body's, and nothing shows it.
 *
 * One level of sub-items (2026-09-27, CL-D1): a row at depth 1 is set in by
 * one spacing step (`data-depth`, checklist.css) with the same drawn box.
 * Tab in a row's field makes it a sub-item of the open row shown above it
 * (`nestUnder` — the row a person sees, not the body's previous line, which
 * may be a done one sitting in Done) and Shift+Tab brings it up a level
 * (`unnest`), as Keep does; the grip's menu carries the same two as "Make a
 * sub-item" / "Move up a level" for the finger and for anyone who does not
 * know the keys. Tab that can change nothing — the first row, a row already
 * a sub-item — is left to the browser, so the list is never a keyboard
 * trap. Ticking a parent ticks its sub-items and the whole block
 * is held for the beat and moves to Done together; reopening a sub-item
 * reopens its parent with it (`toggleItem`). A parent's block moves as one
 * from the grip: Move down steps past its own children, and a block that
 * ends the list cannot move down. A row dropped on a sub-item's slot
 * becomes a sub-item, one dropped on a top-level slot comes out
 * (`moveItem`). Done rows keep their depth, so a finished parent reads as
 * a block there too.
 *
 * Done is a disclosure, remembered per note for the session
 * (`sessionStorage`, open by default, the NoteTabs pattern), with Uncheck
 * all and Delete done beside it; Delete done offers Undo in the shell's
 * toast for six seconds rather than asking first (OF-DEL: no typed word,
 * no dialog for what can be undone). Undo writes the body it captured back
 * only while the body is still what this editor last wrote: a recording
 * filed into the list by refetch inside those six seconds would otherwise
 * leave with it, silently (`UNDO_STALE`). The body as it stands is asked of
 * the caller (`currentBody`), because the toast outlives this component — a
 * tab switch unmounts it while Undo still shows — and the `body` prop stops
 * following the note then. The person's own acts since all pass through
 * `write`, so they never block it.
 *
 * Every change is one `onChange(body)`, and a tick, a move and a delete
 * call `onSave` at once, as a discrete act does, while typing saves on blur.
 * The Items tab hands those to the note editor, so a change rides the note's
 * own autosave, conflict prompt and offline queue; the Split up tab hands
 * them to `adopt`, which makes the first of them replace the body with the
 * proposal. Nothing here knows about the server, or whose body this is.
 *
 * Keyboard: Enter in an item starts a new one under it — a parent's first
 * sub-item when it has any (`insertItemAfter`); Backspace in an emptied item
 * removes it and steps back to the one above, and a removed parent's
 * sub-items come up a level (`removeItem`); Enter in the add row adds and
 * stays there for the next. A move that lands a row on a slot of another
 * level changes its level (`moveItem`), and the status line says so, since
 * neither the menu nor the arrow keys offered a choice.
 */
export function ChecklistEditor({
  noteId,
  body,
  currentBody,
  flash = null,
  onChange,
  onSave,
}: {
  /** For the Done disclosure, remembered per note. */
  noteId: string;
  body: string;
  /**
   * The body as it stands this instant, wherever it lives: read by an Undo
   * that may fire after this component has gone, when `body` is stale.
   */
  currentBody: () => string;
  /** The words of rows a recording just added, marked for a moment (R7-6b). */
  flash?: ReadonlySet<string> | null;
  onChange: (body: string) => void;
  /** A discrete act — a tick, a move, a delete, leaving a field — is done; save now. */
  onSave: () => void;
}) {
  const items = useMemo(() => parseChecklist(body), [body]);
  const hintId = useId();
  const fieldHintId = useId();
  const [announcement, setAnnouncement] = useState('');
  const [adding, setAdding] = useState('');

  /*
   * The row just ticked stays where it is for one beat, so the tick draws
   * under the finger before the row moves down to Done (or back up from
   * it): a row that moved at once was mounted afresh in the other list
   * already ticked, and a transition does not play on mount. The body is
   * written at once — only the grouping waits — and any other write
   * regroups at once, because a line inserted or removed above the held
   * row would move its index onto another item.
   */
  const [held, setHeld] = useState<readonly number[] | null>(null);
  useEffect(() => {
    if (held === null) return;
    const timer = setTimeout(() => {
      setHeld(null);
    }, motionBaseMs());
    return () => {
      clearTimeout(timer);
    };
  }, [held]);

  /*
   * Where focus goes after a write that moved it: the index of the item
   * whose field to focus, or `ADD_ROW` for the add row; or the open-list
   * position of the grip to focus, after a move made from the keyboard, so
   * a second arrow press finds the same row. Set with the edit and consumed
   * by the effect after the render that drew the new rows — the new item's
   * input does not exist until then, and the moved row's grip is a fresh
   * element, keyed by its new index.
   */
  const inputs = useRef(new Map<number, HTMLTextAreaElement>());
  const grips = useRef(new Map<number, HTMLButtonElement>());
  const addRef = useRef<HTMLTextAreaElement>(null);
  const focusAfterWrite = useRef<number | null>(null);
  const focusGripAfterWrite = useRef<number | null>(null);
  const caretAfterWrite = useRef<[number, number] | null>(null);
  useEffect(() => {
    // Every target is consumed by the render it was set for, taken or not:
    // one left armed would fire on the next render — the save settling —
    // and pull focus off the grip a menu item had just put it on.
    const grip = focusGripAfterWrite.current;
    const target = focusAfterWrite.current;
    const caret = caretAfterWrite.current;
    focusGripAfterWrite.current = null;
    focusAfterWrite.current = null;
    caretAfterWrite.current = null;
    if (grip !== null) {
      grips.current.get(grip)?.focus();
      return;
    }
    if (target === null) return;
    const field = target === ADD_ROW ? addRef.current : inputs.current.get(target);
    if (!field) return;
    field.focus();
    // Where the caret was when the row's field was remade, or else at the
    // end of its words, where a Backspace goes on editing them: a textarea
    // focused by script starts with the caret before the first one.
    const [start, end] = caret ?? [field.value.length, field.value.length];
    field.setSelectionRange(start, end);
  });

  // What this editor last wrote: Undo compares it with `currentBody()`,
  // since a change from elsewhere arrives as a new body and nothing else
  // tells the closure holding the captured one.
  const lastWritten = useRef<string | null>(null);

  const write = (next: string, focus?: number, hold: readonly number[] | null = null): void => {
    lastWritten.current = next;
    onChange(next);
    if (focus !== undefined) focusAfterWrite.current = focus;
    setHeld(hold);
  };
  const save = onSave;

  const toggle = (index: number, item: ChecklistItem): void => {
    const previous = body;
    const next = toggleItem(body, index);
    // Every row the tick flipped is held with the one tapped — a parent's
    // sub-items, a reopened sub-item's parent — so a block moves to Done, or
    // back, together. Indices match line for line: a tick adds no line.
    const after = parseChecklist(next);
    const flipped = items.flatMap((was, i) => (was.done === after[i]?.done ? [] : [i]));
    /*
     * A tick moves the row out of sight into Done, so a mistaken one gets an
     * Undo, as Delete done does and with the same guard (R7-13). Shorter
     * than that toast: a tick is small, and the next tick replaces it. The
     * toast goes before the write for the same reason as Delete done's: in
     * Split up the write is the adoption, whose own toast must land last.
     * Undo writes the whole body back, so the row returns to its place.
     */
    if (!item.done) {
      showToast({
        message: `${item.text.trim() || 'Item'} done`,
        ms: TICK_TOAST_MS,
        action: {
          label: 'Undo',
          onSelect: () => {
            if (currentBody() !== lastWritten.current) {
              showToast({ message: UNDO_STALE });
              return;
            }
            write(previous);
            setAnnouncement('Reopened');
            save();
          },
        },
      });
    }
    write(next, undefined, flipped);
    setAnnouncement(item.done ? 'Reopened' : 'Marked done');
    save();
  };

  /**
   * Nests the open row at `position` under the one above it (`by` 1) or
   * brings it up a level (-1): one write, saved at once, or false when
   * there is nowhere to go. `focus` is what stays focused: the row's field
   * (Tab) or its grip (the menu), never both. Nesting may move the row's
   * lines past done ones in the body, which remakes its field, so that is
   * focused again by its new index, caret where it was; its open position
   * never changes.
   */
  const nest = (position: number, by: 1 | -1, focus: 'field' | 'grip'): boolean => {
    const entry = open[position];
    const above = open[position - 1];
    if (!entry) return false;
    // A refusal is said from the grip, where the key otherwise does nothing
    // a screen reader can tell (DB6-22); from the field the key keeps its
    // meaning and the browser's focus move is the answer.
    const refuse = (why: string): false => {
      if (focus === 'grip') setAnnouncement(why);
      return false;
    };
    let next: string;
    if (by > 0) {
      if (!above) return refuse('Nothing above to nest under');
      // A top-level row `canNest` refuses has a done row above it: the one
      // just ticked, held for the beat, or a parent under a done grandparent.
      if (!canNest(items, entry.index, above.index)) {
        return refuse(entry.item.depth > 0 ? 'Already a sub-item' : 'Cannot nest under a done item');
      }
      next = nestUnder(body, entry.index, above.index);
    } else {
      if (entry.item.depth === 0) return refuse('Already a top-level item');
      next = unnest(body, entry.index);
    }
    if (focus === 'grip') {
      write(next);
      focusGripAfterWrite.current = position;
    } else {
      // The write ends any hold, so the rows shown after it are the open
      // ones by `done` alone; the row's place among those, before and after,
      // is the same — `position` is held-aware and may not be.
      const shown = (item: ChecklistItem, i: number): number[] => (item.done ? [] : [i]);
      const at = items.flatMap(shown).indexOf(entry.index);
      const after = parseChecklist(next).flatMap(shown)[at];
      if (after !== undefined && after !== entry.index) {
        const field = inputs.current.get(entry.index);
        caretAfterWrite.current = field ? [field.selectionStart, field.selectionEnd] : null;
        write(next, after);
      } else {
        write(next);
      }
    }
    setAnnouncement(by > 0 ? 'Made a sub-item' : 'Moved up a level');
    save();
    return true;
  };

  const remove = (index: number, focus?: number): void => {
    write(removeItem(body, index), focus);
    save();
  };

  const addFromRow = (): void => {
    const text = adding.trim();
    if (!text) return;
    write(insertItemAfter(body, null, text));
    setAdding('');
  };

  // The held rows are grouped by what they were, not by what they now are.
  const entries = items.map((item, index) => ({ item, index }));
  const isHeld = (index: number): boolean => held?.includes(index) ?? false;
  const open = entries.filter(({ item, index }) => (isHeld(index) ? item.done : !item.done));
  const done = entries.filter(({ item, index }) => (isHeld(index) ? !item.done : item.done));

  /** The open-list positions of the block at body index `index`: the row and its open sub-items. */
  const openBlock = (index: number): number[] => {
    const block = blockOf(items, index);
    return open.flatMap((entry, position) => (block.includes(entry.index) ? [position] : []));
  };

  /**
   * Puts the open item at body index `index` — with its sub-items — in the
   * slot of the open item at `position`: one body write, saved at once. A
   * step down from a parent would land on its own first child, so the
   * target moves past the block to the first row outside it. `focusGrip`
   * keeps the keyboard on the moved row after the render.
   */
  const moveOpen = (index: number, position: number, focusGrip: boolean): void => {
    const block = openBlock(index);
    const current = block[0] ?? -1;
    let at = position;
    while (at > current && block.includes(at)) at += 1;
    const target = open[at];
    if (!target || target.index === index) {
      // The arrow keys at either end: said, since nothing moved and nothing
      // else says why (the menu disables its items there).
      if (focusGrip) setAnnouncement(position < current ? 'Already at the top' : 'Already at the bottom');
      return;
    }
    const next = moveItem(body, index, target.index);
    write(next);
    // Moving down, the block's own rows have left the list above the slot.
    if (focusGrip) focusGripAfterWrite.current = at > current ? at - (block.length - 1) : at;
    // The slot's level is the row's now; said, because no path here asked.
    const landed = target.index > index ? target.index - (blockOf(items, index).length - 1) : target.index;
    const depth = parseChecklist(next)[landed]?.depth ?? 0;
    const was = items[index]?.depth ?? 0;
    if (depth !== was) setAnnouncement(depth > was ? 'Now a sub-item' : 'Now a top-level item');
    save();
  };

  // The drag's ids are body indices, as strings; the order shown while a
  // row is lifted is the hook's draft.
  const listRef = useRef<HTMLUListElement>(null);
  const openIds = open.map(({ index }) => String(index));
  const drag = useDragReorder({
    listRef,
    ids: openIds,
    onCommit: (next, moved) => {
      // A parent dragged one slot down stands on its own child's slot, which
      // the draft showed but nothing can mean; dropped there it goes back,
      // rather than past the next row too as the menu's Move down steps.
      const position = next.indexOf(moved);
      if (openBlock(Number(moved)).includes(position)) return;
      moveOpen(Number(moved), position, false);
    },
    // A tap on the grip opens the row's menu: the grip is the menu's own
    // trigger, so clicking it is the same as any press on it.
    onTap: (id) => {
      grips.current.get(openIds.indexOf(id))?.click();
    },
    // A sideways drag released a level over: the same write as Tab and the
    // menu, which refuse the first open row, a row already a sub-item and a
    // done neighbour, and say what they did.
    onShift: (id, levels) => {
      nest(openIds.indexOf(id), levels, 'grip');
    },
  });
  const dragging = drag.draggingId !== null;
  useEffect(() => {
    if (dragging) drag.cancel();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only a body change should drop the row; `cancel` is remade every render
  }, [body]);
  const byId = new Map(open.map((entry) => [String(entry.index), entry]));
  const shownOpen = drag.draft
    ? drag.draft.flatMap((id) => {
        const entry = byId.get(id);
        return entry ? [entry] : [];
      })
    : open;

  /**
   * The level the lifted row at `position` would take if the sideways drag
   * let go now, for the row to draw; nothing when the drag is not sideways,
   * is on another row, or asks for what `nest` would refuse.
   */
  const nestPreview = (position: number): 1 | -1 | undefined => {
    const shift = drag.draftShift;
    const entry = shownOpen[position];
    if (!shift || !entry || shift.id !== String(entry.index) || shift.levels === 0) return undefined;
    const above = shownOpen[position - 1];
    const allowed =
      shift.levels > 0 ? above !== undefined && canNest(items, entry.index, above.index) : entry.item.depth > 0;
    return allowed ? shift.levels : undefined;
  };

  /** The grip's menu: the no-drag path to every place a row can go, its level, then the row's delete. */
  const gripMenu = (index: number, position: number, item: ChecklistItem): OverflowMenuItem[] => {
    const first = position === 0;
    const above = open[position - 1];
    // Nothing below the block to move past: a parent whose sub-items end the list is last too.
    const last = open.slice(position + 1).every((entry) => blockOf(items, index).includes(entry.index));
    return [
      { label: 'Move up', disabled: first, onSelect: () => moveOpen(index, position - 1, true) },
      { label: 'Move down', disabled: last, onSelect: () => moveOpen(index, position + 1, true) },
      { label: 'Move to top', disabled: first, onSelect: () => moveOpen(index, 0, true) },
      { label: 'Move to bottom', disabled: last, onSelect: () => moveOpen(index, open.length - 1, true) },
      {
        label: 'Make a sub-item',
        disabled: !above || !canNest(items, index, above.index),
        onSelect: () => {
          nest(position, 1, 'grip');
        },
      },
      {
        label: 'Move up a level',
        disabled: item.depth === 0,
        onSelect: () => {
          nest(position, -1, 'grip');
        },
      },
      {
        label: 'Delete',
        destructive: true,
        onSelect: () => {
          // Focus lands on the next open row, which moves up one index, or on the add row.
          const following = open[position + 1];
          remove(index, following ? following.index - 1 : ADD_ROW);
        },
      },
    ];
  };

  const reopenAll = (): void => {
    write(uncheckAll(body));
    setAnnouncement('All items reopened');
    save();
  };

  const deleteDone = (): void => {
    const previous = body;
    const count = done.length;
    const message = `${String(count)} done item${count === 1 ? '' : 's'} deleted`;
    // The toast before the write. In Split up the write is the adoption,
    // whose own toast then lands last and is the one left standing: its
    // Undo restores the list as it stood before the proposal, which this
    // one cannot, and `showToast` replaces whatever was showing, so shown
    // after it this one would have hidden it (DB6-2). In the Items tab the
    // order is invisible.
    showToast({
      message,
      action: {
        label: 'Undo',
        onSelect: () => {
          if (currentBody() !== lastWritten.current) {
            showToast({ message: UNDO_STALE });
            return;
          }
          write(previous);
          save();
        },
      },
    });
    setAnnouncement(message);
    write(removeDone(body));
    save();
  };

  return (
    <div className="checklist-editor">
      {open.length > 0 && (
        <>
          <p id={hintId} className="visually-hidden">
            To reorder, drag a handle, or focus it and press the up and down arrow keys; drag it
            sideways, or press the right and left arrows, to change its level; tap it for more.
          </p>
          {/* Described on the field, where the keys act: the grip's hint is never read there. */}
          <p id={fieldHintId} className="visually-hidden">
            Tab makes this item a sub-item of the one above; Shift+Tab moves it up a level.
          </p>
        </>
      )}
      <ul
        ref={listRef}
        className="checklist"
        role="list"
        aria-label="Items"
        data-dragging={dragging || undefined}
        {...drag.listHandlers}
      >
        {shownOpen.map(({ item, index }, position) => (
          <ChecklistRow
            key={index}
            item={item}
            index={index}
            position={position}
            dragging={drag.draggingId === String(index)}
            nestPreview={nestPreview(position)}
            flash={flash?.has(item.text.trim()) ?? false}
            hintId={hintId}
            fieldHintId={fieldHintId}
            menu={gripMenu(index, position, item)}
            gripRef={(element) => {
              if (element) grips.current.set(position, element);
              else grips.current.delete(position);
            }}
            fieldRef={(element) => {
              if (element) inputs.current.set(index, element);
              else inputs.current.delete(index);
            }}
            onLift={(event) => {
              if (event.button === 0) {
                drag.start(event.pointerId, String(index), { x: event.clientX, y: event.clientY });
              }
            }}
            onStep={(by) => {
              moveOpen(index, position + by, true);
            }}
            onToggle={() => {
              toggle(index, item);
            }}
            onText={(text) => {
              write(setItemText(body, index, text));
            }}
            onNest={(by, focus) => nest(position, by, focus)}
            onEnter={() => {
              // The new item sits right under this one, in the body and on screen.
              write(insertItemAfter(body, index), index + 1);
            }}
            onBackspaceEmpty={() => {
              // Back to the open item above, which keeps its index; or the add row
              // when this was the last open item.
              const at = open.findIndex((entry) => entry.index === index);
              const previous = open[at - 1];
              remove(index, previous ? previous.index : ADD_ROW);
            }}
            onBlur={save}
          />
        ))}
        <li className="checklist__row checklist__row--add">
          <span className="checklist__grip-space" aria-hidden="true" />
          <span className="checklist__check checklist__add-mark" aria-hidden="true">
            <Icon name="plus" size={18} />
          </span>
          <ItemField
            ref={addRef}
            value={adding}
            placeholder="Add an item"
            aria-label="Add an item"
            enterKeyHint="done"
            onChange={(event) => {
              setAdding(event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key !== 'Enter') return;
              event.preventDefault();
              addFromRow();
            }}
            onBlur={() => {
              addFromRow();
              save();
            }}
          />
        </li>
      </ul>

      {done.length > 0 && (
        <ChecklistDone
          noteId={noteId}
          done={done}
          onToggle={toggle}
          onRemove={(index) => {
            remove(index);
          }}
          onReopenAll={reopenAll}
          onDeleteDone={deleteDone}
        />
      )}

      {/*
        Said, not only drawn: the row the finger tapped has just left the
        list it was in, and a screen reader has nothing else to tell it where
        it went.
      */}
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
    </div>
  );
}

/** The add row, as a focus target. Never an item index. */
const ADD_ROW = -1;

/** How long a tick's Undo stands: long enough to read a short line and reach the button. */
const TICK_TOAST_MS = 4000;

/** What an Undo says instead of writing over a list that changed under it. */
export const UNDO_STALE = 'The list changed since — nothing undone.';

/**
 * `--motion-duration-base` in milliseconds, read from the sheet so the hold
 * lasts exactly as long as the tick takes to draw — one millisecond under
 * reduced motion, where the tokens collapse. The token's own value when the
 * sheet cannot be read (tests).
 */
function motionBaseMs(): number {
  return parseMotionMs(getComputedStyle(document.documentElement).getPropertyValue('--motion-duration-base'));
}

/**
 * A CSS time in milliseconds. The source sheet says `220ms`, but the built
 * one is minified to `.22s`, and reading that as 0.22 ms made the hold
 * invisible on the deployed app while every test, run against the source
 * token, passed. Honours both units; the token's own value for anything else.
 */
export function parseMotionMs(raw: string): number {
  const value = raw.trim();
  const n = Number.parseFloat(value);
  if (!Number.isFinite(n)) return 220;
  if (value.endsWith('ms')) return n;
  if (value.endsWith('s')) return n * 1000;
  return n;
}
