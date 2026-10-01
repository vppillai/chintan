import { useEffect, useId, useMemo, useRef, useState } from 'react';

import { Icon } from '@/components/Icon.tsx';
import type { OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { announce } from '@/components/StatusRegion.tsx';
import { showToast } from '@/components/Toast.tsx';
import { useDragReorder } from '@/hooks/useDragReorder.ts';

import { ChecklistDone } from './ChecklistDone.tsx';
import { ChecklistRow, ItemField } from './ChecklistRow.tsx';
import {
  blockOf,
  canNest,
  clampLevels,
  insertItemAfter,
  openBlock,
  parentOf,
  parseChecklist,
  planLevel,
  planMove,
  previewDepths,
  removeDone,
  removeItem,
  setItemText,
  shiftLevel,
  toggleItem,
  uncheckAll,
  type ChecklistItem,
  type Entry,
} from './checklist.ts';
import { useTidying } from './useTidyList.ts';

/**
 * A checklist body as rows to tick off: the Items tab. While a Tidy up list
 * runs (`useTidyList`) a status line above the rows says so.
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
 * the row in its slot and changes its level instead: every indent step
 * (24 px, `--space-6`) to the right is one level in, to the left one out,
 * clamped to where the row may go (`clampLevels`), so one release can move
 * it several levels. It is previewed on the lifted row (`data-preview-depth`:
 * the depth it would take, drawn as that indent with an accent bar at its
 * start) and written once on release through `shift`, the same path as Tab. The up and down arrows on a focused grip move the row
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
 * Three levels (round 8, F1; `MAX_DEPTH`): a row is set in by one spacing
 * step per level (`data-depth`, checklist.css) with the same drawn box, and
 * carries `aria-level`. Tab in a row's field takes it one level in under the
 * open row shown above it — the row a person sees, not the body's previous
 * line, which may be a done one sitting in Done — and Shift+Tab one level
 * out, in place, as Keep and Workflowy do (`shiftLevel`). A row goes at most
 * one level under the row above it, so under a sub-item one Tab makes a
 * top-level row its sibling and a second its child; the first row never goes
 * in. The grip's menu carries the same two as "Make a sub-item" / "Move up a
 * level" for the finger and for anyone who does not know the keys. Tab that
 * can change nothing — the first row, a row as deep as the row above allows,
 * a row whose own sub-items would go past the third level — is left to the
 * browser, so the list is never a keyboard trap; from the grip the same
 * refusal is said (`planLevel`). Ticking a parent ticks its sub-items and the whole block
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
 * own autosave, conflict prompt and offline queue. Nothing here knows about
 * the server, or whose body this is.
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
  const tidying = useTidying(noteId);
  // Said, not only drawn, through the shell's one region (`announce`): a
  // Tidy under way, and below, where a row went after a tick, a move or a
  // key, since a screen reader has nothing else to tell it.
  useEffect(() => {
    if (tidying) announce('Tidying the list…');
  }, [tidying]);
  const hintId = useId();
  const fieldHintId = useId();
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

  // The held rows are grouped by what they were, not by what they now are.
  // Memoised so the drag's preview is remade only when the rows are.
  const { open, done } = useMemo(() => {
    const isHeld = (index: number): boolean => held?.includes(index) ?? false;
    const entries: Entry[] = items.map((item, index) => ({ item, index }));
    return {
      open: entries.filter(({ item, index }) => (isHeld(index) ? item.done : !item.done)),
      done: entries.filter(({ item, index }) => (isHeld(index) ? !item.done : item.done)),
    };
  }, [items, held]);

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
     * Undo (R7-13). Shorter than Delete done's toast: a tick is small, and
     * the next tick replaces it, so only the last tick is undoable. Weak: it
     * never takes the place of a standing Delete done or Tidy Undo, which
     * would leave those items with no way back. Undo writes the whole
     * body back, so the row returns to its place — and only while the body
     * is still exactly the tick's: after any later act, the person's own
     * included, it refuses rather than undo that act too.
     */
    write(next, undefined, flipped);
    if (item.done) announce('Reopened');
    else {
      // Said once: by the toast, which is a live region too, or — when a
      // standing Delete done or Tidy Undo keeps the weak toast away — by the
      // status region (review 2026-10-01, FE-4; R7-13 had both say it).
      const shown = showToast({
        message: `${shortName(item.text)} done`,
        ms: TICK_TOAST_MS,
        weak: true,
        action: {
          label: 'Undo',
          onSelect: () => {
            if (currentBody() !== next) {
              showToast({ message: UNDO_STALE });
              return;
            }
            write(previous);
            announce('Reopened');
            save();
          },
        },
      });
      if (!shown) announce('Marked done');
    }
    save();
  };

  /**
   * Moves the open row at `position` `by` levels — one from Tab, the arrows
   * and the menu, any number from a drag, which the caller has clamped to
   * where the row may go: one write, saved at once, or false when there is
   * nowhere to go. `focus` is what stays focused: the row's field (Tab) or
   * its grip (the menu, the arrows, a drag), never both. Going in may move
   * the row's lines past done ones in the body, which remakes its field, so
   * that is focused again by its new index, caret where it was; its open
   * position never changes. The block under it moves with it (`shiftLevel`).
   */
  const shift = (position: number, by: number, focus: 'field' | 'grip'): boolean => {
    const entry = open[position];
    if (!entry) return false;
    const target = planLevel(items, open, position, by);
    if ('refusal' in target) {
      // A refusal is said from the grip, where the key otherwise does
      // nothing a screen reader can tell (DB6-22); from the field the key
      // keeps its meaning and the browser's focus move is the answer.
      if (focus === 'grip') announce(target.refusal);
      return false;
    }
    const next = shiftLevel(body, entry.index, by, open[position - 1]?.index ?? entry.index);
    // The write ends any hold, so the rows shown after it are the open ones
    // by `done` alone; the row's place among those, before and after, is the
    // same — `position` is held-aware and may not be.
    const shown = (item: ChecklistItem, i: number): number[] => (item.done ? [] : [i]);
    const after = parseChecklist(next);
    const landed = after.flatMap(shown)[items.flatMap(shown).indexOf(entry.index)] ?? entry.index;
    if (focus === 'grip') {
      write(next);
      focusGripAfterWrite.current = position;
    } else if (landed !== entry.index) {
      const field = inputs.current.get(entry.index);
      caretAfterWrite.current = field ? [field.selectionStart, field.selectionEnd] : null;
      write(next, landed);
    } else {
      write(next);
    }
    // Where it went, once, however many levels one release moved it.
    const parent = parentOf(after, landed);
    const under = parent === null ? null : `“${shortName(after[parent]?.text ?? '')}”`;
    if (under === null) announce('Now a top-level item');
    else announce(by > 0 ? `Made a sub-item of ${under}` : `Moved up a level, under ${under}`);
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

  /**
   * Puts the open item at body index `index` — with its sub-items — in the
   * slot of the open item at `position` (`planMove`): one body write, saved
   * at once. `focusGrip` keeps the keyboard on the moved row after the render.
   */
  const moveOpen = (index: number, position: number, focusGrip: boolean): void => {
    const move = planMove(body, items, open, index, position);
    if ('refusal' in move) {
      // The arrow keys at either end: said, since nothing moved and nothing
      // else says why (the menu disables its items there).
      if (focusGrip) announce(move.refusal);
      return;
    }
    write(move.next);
    if (focusGrip) focusGripAfterWrite.current = move.gripAt;
    // The slot's level is the row's now; said, because no path here asked.
    if (move.said !== null) announce(move.said);
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
      if (openBlock(items, open, Number(moved)).includes(position)) return;
      moveOpen(Number(moved), position, false);
    },
    // A tap on the grip opens the row's menu: the grip is the menu's own
    // trigger, so clicking it is the same as any press on it.
    onTap: (id) => {
      grips.current.get(openIds.indexOf(id))?.click();
    },
    // A sideways drag released some levels over: the same write as Tab and
    // the menu, which refuse what cannot be and say what they did.
    onShift: (id, levels) => {
      const position = openIds.indexOf(id);
      shift(position, clampLevels(items, open, position, levels), 'grip');
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

  // The depth each row would take if the sideways drag let go now, remade
  // only when the drag or the rows change, not on every move of the finger.
  const previews = useMemo(
    () => previewDepths(body, items, open, drag.draftShift),
    [body, items, open, drag.draftShift],
  );

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
          shift(position, 1, 'grip');
        },
      },
      {
        label: 'Move up a level',
        disabled: item.depth === 0,
        onSelect: () => {
          shift(position, -1, 'grip');
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
    announce('All items reopened');
    save();
  };

  const deleteDone = (): void => {
    const previous = body;
    const count = done.length;
    const message = `${String(count)} done item${count === 1 ? '' : 's'} deleted`;
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
    announce(message);
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
      {/* Seen above the rows; said through the shell's region (the effect above). */}
      {tidying && (
        <p className="checklist-editor__status" aria-hidden="true">
          Tidying the list…
        </p>
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
            previewDepth={previews.get(index)}
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
            onNest={(by, focus) => shift(position, by, focus)}
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
    </div>
  );
}

/** The add row, as a focus target. Never an item index. */
const ADD_ROW = -1;

/** How long a tick's Undo stands: long enough to read a short line and reach the button. */
const TICK_TOAST_MS = 4000;

/** The most of an item's words a toast line carries. */
const TOAST_NAME_MAX = 40;

/** An item's words for a one-line toast: cut at a word with an ellipsis when long. */
export function shortName(text: string): string {
  const words = text.trim().replace(/\s+/g, ' ');
  if (words === '') return 'Item';
  if (words.length <= TOAST_NAME_MAX) return words;
  const cut = words.slice(0, TOAST_NAME_MAX);
  const space = cut.lastIndexOf(' ');
  return `${(space > TOAST_NAME_MAX / 2 ? cut.slice(0, space) : cut).trimEnd()}…`;
}

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
