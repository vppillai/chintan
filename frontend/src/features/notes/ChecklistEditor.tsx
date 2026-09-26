import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type Ref,
  type TextareaHTMLAttributes,
} from 'react';

import { ICON_STROKE_WIDTH, Icon, PATHS } from '@/components/Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { showToast } from '@/components/Toast.tsx';
import { useAutoGrow } from '@/hooks/useAutoGrow.ts';
import { useDragReorder } from '@/hooks/useDragReorder.ts';

import {
  insertItemAfter,
  moveItem,
  parseChecklist,
  removeDone,
  removeItem,
  setItemText,
  toggleItem,
  uncheckAll,
  type ChecklistItem,
} from './checklist.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The Items tab: a checklist note's body as rows to tick off.
 *
 * Open items first, in body order, each a grip, a real checkbox and a text
 * field that wraps and grows with its words; an "Add an item" row under
 * them; then the done items under a Done heading, greyed and struck through,
 * each with its checkbox to reopen it and a × to delete it. Ticking an item
 * moves it down to Done, as Google Keep does — the body keeps its order and
 * only the item's own line changes (`toggleItem`), so a recording the worker
 * appends meanwhile lands where it would have anyway.
 *
 * The grip is the one control for the order (2026-09-26, CL-3). A drag on
 * it lifts the row and the list re-sorts under the pointer (`useDragReorder`,
 * the pinned group's gesture); nothing is written until release, then the
 * body is rewritten once (`moveItem`) and saved, as a tick is. The arrow
 * keys on it move the row one slot. A tap on it — a lift that never moved —
 * opens the row's menu: Move up, Move down, Move to top, Move to bottom,
 * Delete; the path that needs no drag at all (WCAG 2.5.7), and a menu on
 * the grip rather than a ⋮ per row because a phone's width has no room for
 * both beside a dictated sentence. A body that changes under a lifted row —
 * a recording landing by refetch — drops the row, since the slots it was
 * moving between are gone. Done rows have no grip: their order is the
 * body's, and nothing shows it.
 *
 * Done is a disclosure, remembered per note for the session
 * (`sessionStorage`, open by default, the NoteTabs pattern), with Uncheck
 * all and Delete done beside it; Delete done offers Undo in the shell's
 * toast for six seconds rather than asking first (OF-DEL: no typed word,
 * no dialog for what can be undone).
 *
 * Every change is `editor.edit({ body })` and rides the note's own autosave,
 * conflict prompt and offline queue; a tick, a move and a delete save at
 * once, as a discrete act does, and typing saves on blur. Nothing here knows
 * about the server.
 *
 * Keyboard: Enter in an item starts a new one under it; Backspace in an
 * emptied item removes it and steps back to the one above; Enter in the add
 * row adds and stays there for the next.
 */
export function ChecklistEditor({ editor, noteId }: { editor: NoteEditor; noteId: string }) {
  const body = editor.model.draft.body;
  const items = useMemo(() => parseChecklist(body), [body]);
  const doneId = useId();
  const doneListId = useId();
  const hintId = useId();
  const [announcement, setAnnouncement] = useState('');
  const [adding, setAdding] = useState('');
  const [doneOpen, setDoneOpen] = useState(() => readDoneOpen(noteId));

  /*
   * The row just ticked stays where it is for one beat, so the tick draws
   * under the finger before the row moves down to Done (or back up from
   * it): a row that moved at once was mounted afresh in the other list
   * already ticked, and a transition does not play on mount. The body is
   * written at once — only the grouping waits — and any other write
   * regroups at once, because a line inserted or removed above the held
   * row would move its index onto another item.
   */
  const [held, setHeld] = useState<number | null>(null);
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
  useEffect(() => {
    const grip = focusGripAfterWrite.current;
    if (grip !== null) {
      focusGripAfterWrite.current = null;
      grips.current.get(grip)?.focus();
      return;
    }
    const target = focusAfterWrite.current;
    if (target === null) return;
    focusAfterWrite.current = null;
    const field = target === ADD_ROW ? addRef.current : inputs.current.get(target);
    if (!field) return;
    field.focus();
    // At the end of its words, where a Backspace goes on editing them: a
    // textarea focused by script starts with the caret before the first one.
    field.setSelectionRange(field.value.length, field.value.length);
  });

  const write = (next: string, focus?: number, hold: number | null = null): void => {
    editor.edit({ body: next });
    if (focus !== undefined) focusAfterWrite.current = focus;
    setHeld(hold);
  };
  const save = (): void => void editor.saveNow();

  const toggle = (index: number, item: ChecklistItem): void => {
    write(toggleItem(body, index), undefined, index);
    setAnnouncement(item.done ? 'Reopened' : 'Marked done');
    save();
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

  // The held row is grouped by what it was, not by what it now is.
  const entries = items.map((item, index) => ({ item, index }));
  const open = entries.filter(({ item, index }) => (index === held ? item.done : !item.done));
  const done = entries.filter(({ item, index }) => (index === held ? !item.done : item.done));

  /**
   * Puts the open item at body index `index` in the slot of the open item at
   * `position`: one body write, saved at once. `focusGrip` keeps the keyboard
   * on the moved row after the render.
   */
  const moveOpen = (index: number, position: number, focusGrip: boolean): void => {
    const target = open[position];
    if (!target || target.index === index) return;
    write(moveItem(body, index, target.index));
    if (focusGrip) focusGripAfterWrite.current = position;
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
      moveOpen(Number(moved), next.indexOf(moved), false);
    },
    // A tap on the grip opens the row's menu: the grip is the menu's own
    // trigger, so clicking it is the same as any press on it.
    onTap: (id) => {
      grips.current.get(openIds.indexOf(id))?.click();
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

  const onItemKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>, index: number): void => {
    if (event.key === 'Enter') {
      event.preventDefault();
      // The new item sits right under this one, in the body and on screen.
      write(insertItemAfter(body, index), index + 1);
      return;
    }
    if (event.key === 'Backspace' && event.currentTarget.value === '') {
      event.preventDefault();
      // Back to the open item above, which keeps its index; or the add row
      // when this was the last open item.
      const position = open.findIndex((entry) => entry.index === index);
      const previous = open[position - 1];
      remove(index, previous ? previous.index : ADD_ROW);
    }
  };

  const onGripKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number, position: number): void => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault();
    moveOpen(index, position + (event.key === 'ArrowUp' ? -1 : 1), true);
  };

  /** The grip's menu: the no-drag path to every place a row can go, then the row's delete. */
  const gripMenu = (index: number, position: number): OverflowMenuItem[] => {
    const first = position === 0;
    const last = position === open.length - 1;
    return [
      { label: 'Move up', disabled: first, onSelect: () => moveOpen(index, position - 1, true) },
      { label: 'Move down', disabled: last, onSelect: () => moveOpen(index, position + 1, true) },
      { label: 'Move to top', disabled: first, onSelect: () => moveOpen(index, 0, true) },
      { label: 'Move to bottom', disabled: last, onSelect: () => moveOpen(index, open.length - 1, true) },
      // Phase 2 (owner decision CL-D1): "Make a sub-item" / "Move out" go here.
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

  const toggleDone = (): void => {
    const next = !doneOpen;
    setDoneOpen(next);
    rememberDoneOpen(noteId, next);
  };

  const reopenAll = (): void => {
    write(uncheckAll(body));
    setAnnouncement('All items reopened');
    save();
  };

  const deleteDone = (): void => {
    const previous = body;
    const count = done.length;
    write(removeDone(body));
    save();
    const message = `${String(count)} done item${count === 1 ? '' : 's'} deleted`;
    setAnnouncement(message);
    showToast({
      message,
      action: {
        label: 'Undo',
        onSelect: () => {
          write(previous);
          save();
        },
      },
    });
  };

  return (
    <div className="checklist-editor">
      {open.length > 0 && (
        <p id={hintId} className="visually-hidden">
          To reorder, drag a handle, or focus it and press the up and down arrow keys; tap it for
          more.
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
        {shownOpen.map(({ item, index }, position) => {
          const id = String(index);
          return (
            <li
              key={index}
              className={rowClass(item)}
              data-drag-id={id}
              data-dragging={drag.draggingId === id || undefined}
            >
              <OverflowMenu
                label={`Move ${item.text || 'item'}`}
                describedBy={hintId}
                items={gripMenu(index, position)}
                trigger={(props) => (
                  <button
                    {...props}
                    ref={(element) => {
                      if (element) grips.current.set(position, element);
                      else grips.current.delete(position);
                    }}
                    className="checklist__grip"
                    onPointerDown={(event) => {
                      if (event.button === 0) drag.start(event.pointerId, id);
                    }}
                    onKeyDown={(event) => {
                      onGripKeyDown(event, index, position);
                    }}
                  >
                    <Icon name="grip" size={18} />
                  </button>
                )}
              />
              <Check
                checked={item.done}
                name={item.text || `Item ${String(position + 1)}`}
                onChange={() => {
                  toggle(index, item);
                }}
              />
              <ItemField
                ref={(element) => {
                  if (element) inputs.current.set(index, element);
                  else inputs.current.delete(index);
                }}
                value={item.text}
                aria-label={`Item ${String(position + 1)}`}
                enterKeyHint="next"
                onChange={(event) => {
                  write(setItemText(body, index, event.target.value));
                }}
                onKeyDown={(event) => {
                  onItemKeyDown(event, index);
                }}
                onBlur={save}
              />
            </li>
          );
        })}
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
        <section className="checklist__done" aria-labelledby={doneId}>
          <div className="checklist__done-head">
            <h2 id={doneId} className="checklist__done-title">
              <button
                type="button"
                className="checklist__disclosure"
                aria-expanded={doneOpen}
                aria-controls={doneListId}
                onClick={toggleDone}
              >
                <Icon name="chevron-down" size={16} className="checklist__chevron" />
                <span className="eyebrow">
                  Done <span className="numeric">({done.length})</span>
                </span>
              </button>
            </h2>
            <div className="checklist__done-actions">
              <button type="button" className="checklist__done-action" onClick={reopenAll}>
                Uncheck all
              </button>
              <span aria-hidden="true">·</span>
              <button type="button" className="checklist__done-action" onClick={deleteDone}>
                Delete done
              </button>
            </div>
          </div>
          <ul id={doneListId} className="checklist" role="list" hidden={!doneOpen}>
            {done.map(({ item, index }) => (
              <li key={index} className={rowClass(item)}>
                <span className="checklist__grip-space" aria-hidden="true" />
                <Check
                  checked={item.done}
                  name={item.text || 'Item'}
                  onChange={() => {
                    toggle(index, item);
                  }}
                />
                <span className="checklist__text">{item.text}</span>
                <DeleteItem
                  text={item.text}
                  onClick={() => {
                    remove(index);
                  }}
                />
              </li>
            ))}
          </ul>
        </section>
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

/** Where a note's Done section remembers whether it is open, for the session. */
export function doneStorageKey(noteId: string): string {
  return `chintan.checklist-done.${noteId}`;
}

function readDoneOpen(noteId: string): boolean {
  try {
    return sessionStorage.getItem(doneStorageKey(noteId)) !== 'collapsed';
  } catch {
    // Storage denied: the section simply opens, as it does the first time.
    return true;
  }
}

function rememberDoneOpen(noteId: string, open: boolean): void {
  try {
    sessionStorage.setItem(doneStorageKey(noteId), open ? 'open' : 'collapsed');
  } catch {
    /* Storage denied. */
  }
}

/** The add row, as a focus target. Never an item index. */
const ADD_ROW = -1;

function rowClass(item: ChecklistItem): string {
  return item.done ? 'checklist__row checklist__row--done' : 'checklist__row';
}

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

/**
 * The box: a real checkbox, kept for what only it gives — the role, the
 * name, the keyboard, the state — and stretched invisibly over its 44 px
 * label, so the tap lands on the control itself; beside it the box a finger
 * sees, drawn here in the icon set's own pen (`ICON_STROKE_WIDTH`, not
 * scaling, round caps) so it reads as the same hand as every glyph. The tick
 * is `PATHS.check` with a `pathLength` of 1, which lets the stylesheet hide
 * it with one dash and draw it as a stroke when the box is ticked. No
 * browser's native box appears anywhere in the app.
 */
function Check({
  checked,
  name,
  disabled = false,
  onChange,
}: {
  checked: boolean;
  name: string;
  disabled?: boolean;
  onChange: () => void;
}) {
  return (
    <label className="checklist__check">
      <input
        type="checkbox"
        className="checklist__box"
        checked={checked}
        disabled={disabled}
        onChange={onChange}
      />
      <CheckMark />
      <span className="visually-hidden">{name}</span>
    </label>
  );
}

/**
 * The box a finger sees, on its own: the `.checklist__box` input before it
 * in the same label is what the stylesheet reads the state from, so any
 * label built that way — the Items rows, the Split up rows, the switches in
 * the Cleaned tab and Details — shows the one drawn box.
 */
export function CheckMark() {
  return (
    <span className="checklist__mark" aria-hidden="true">
      <svg
        width={22}
        height={22}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={ICON_STROKE_WIDTH}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <rect x={1} y={1} width={22} height={22} rx={5.5} vectorEffect="non-scaling-stroke" />
        <path d={PATHS.check} pathLength={1} vectorEffect="non-scaling-stroke" />
      </svg>
    </span>
  );
}

/** The × on a done row: gone for good, not reopened. */
function DeleteItem({
  text,
  disabled = false,
  onClick,
}: {
  text: string;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="checklist__delete"
      aria-label={`Delete ${text || 'item'}`}
      disabled={disabled}
      onClick={onClick}
    >
      <Icon name="close" size={16} />
    </button>
  );
}

/**
 * An item's words: a textarea that wraps and grows with them. A recording
 * becomes one item, so a whole dictated sentence is the normal case, and a
 * single-line input clipped it on a phone (smoke 2026-09-21, finding 2). It
 * is still one line of the body — the caller takes Enter for "new item", and
 * `setItemText`/`insertItemAfter` turn a pasted line break into a space — so
 * the field only ever wraps, never holds a newline. `field-sizing: content`
 * sizes it where understood; `useAutoGrow` measures elsewhere.
 */
function ItemField({
  ref,
  value,
  ...rest
}: { ref: Ref<HTMLTextAreaElement>; value: string } & TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const own = useRef<HTMLTextAreaElement | null>(null);
  useAutoGrow(own, value);
  return (
    <textarea
      ref={(element) => {
        own.current = element;
        if (typeof ref === 'function') ref(element);
        else if (ref) ref.current = element;
      }}
      rows={1}
      className="checklist__text"
      value={value}
      autoComplete="off"
      {...rest}
    />
  );
}

/**
 * The Split up tab's list: the same rows, ticking and deleting through the
 * caller, which decides what body they change — `CleanedPanel` makes the
 * first act adopt the split list as the note's body. Items stay in body
 * order and a done one fills in where it stands rather than moving down,
 * because this list is a reading of a proposal, not the editor; a done item
 * still has its × as it does under Done. `disabled` holds every row while a
 * regeneration is on its way, as the buttons are held: a tick then would
 * adopt a proposal about to be replaced. A row is keyed by its words as
 * well as its place, so a delete above it remounts it (no transition) rather
 * than sliding the tick of the row that stood there.
 */
export function ChecklistPreview({
  body,
  label,
  disabled = false,
  onToggle,
  onDelete,
}: {
  body: string;
  label: string;
  disabled?: boolean;
  onToggle: (index: number) => void;
  onDelete: (index: number) => void;
}) {
  const items = parseChecklist(body);
  return (
    <ul className="checklist checklist--preview" role="list" aria-label={label}>
      {items.map((item, index) => (
        <li key={`${String(index)}:${item.text}`} className={rowClass(item)}>
          <Check
            checked={item.done}
            name={item.text || `Item ${String(index + 1)}`}
            disabled={disabled}
            onChange={() => {
              onToggle(index);
            }}
          />
          <span className="checklist__text">{item.text}</span>
          {item.done && (
            <DeleteItem
              text={item.text}
              disabled={disabled}
              onClick={() => {
                onDelete(index);
              }}
            />
          )}
        </li>
      ))}
    </ul>
  );
}
