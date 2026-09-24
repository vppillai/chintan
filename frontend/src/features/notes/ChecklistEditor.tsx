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
import { useAutoGrow } from '@/hooks/useAutoGrow.ts';

import {
  insertItemAfter,
  parseChecklist,
  removeItem,
  setItemText,
  toggleItem,
  type ChecklistItem,
} from './checklist.ts';
import type { NoteEditor } from './useNoteEditor.ts';

/**
 * The Items tab: a checklist note's body as rows to tick off.
 *
 * Open items first, in body order, each a real checkbox and a text field
 * that wraps and grows with its words; an "Add an item" row under them; then
 * the done items, greyed and
 * struck through, each with its checkbox to reopen it and a × to delete it.
 * Ticking an item moves it down to Done, as Google Keep does — the body keeps
 * its order and only the item's own line changes (`toggleItem`), so a
 * recording the worker appends meanwhile lands where it would have anyway.
 *
 * Every change is `editor.edit({ body })` and rides the note's own autosave,
 * conflict prompt and offline queue; a tick and a delete save at once, as a
 * discrete act does, and typing saves on blur. Nothing here knows about the
 * server.
 *
 * Keyboard: Enter in an item starts a new one under it; Backspace in an
 * emptied item removes it and steps back to the one above; Enter in the add
 * row adds and stays there for the next.
 */
export function ChecklistEditor({ editor }: { editor: NoteEditor }) {
  const body = editor.model.draft.body;
  const items = useMemo(() => parseChecklist(body), [body]);
  const doneId = useId();
  const [announcement, setAnnouncement] = useState('');
  const [adding, setAdding] = useState('');

  /*
   * Where the caret goes after a write that moved it: the index of the item
   * to focus, or `ADD_ROW` for the add row. Set with the edit and consumed by
   * the effect after the render that drew the new rows — the new item's input
   * does not exist until then.
   */
  const inputs = useRef(new Map<number, HTMLTextAreaElement>());
  const addRef = useRef<HTMLTextAreaElement>(null);
  const focusAfterWrite = useRef<number | null>(null);
  useEffect(() => {
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

  const write = (next: string, focus?: number): void => {
    editor.edit({ body: next });
    if (focus !== undefined) focusAfterWrite.current = focus;
  };
  const save = (): void => void editor.saveNow();

  const toggle = (index: number, item: ChecklistItem): void => {
    write(toggleItem(body, index));
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

  const open = items.map((item, index) => ({ item, index })).filter(({ item }) => !item.done);
  const done = items.map((item, index) => ({ item, index })).filter(({ item }) => item.done);

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

  return (
    <div className="checklist-editor">
      <ul className="checklist" role="list" aria-label="Items">
        {open.map(({ item, index }, position) => (
          <li key={index} className="checklist__row">
            <Check
              checked={false}
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
        ))}
        <li className="checklist__row checklist__row--add">
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
          <h2 id={doneId} className="eyebrow">
            Done <span className="numeric">({done.length})</span>
          </h2>
          <ul className="checklist" role="list">
            {done.map(({ item, index }) => (
              <li key={index} className="checklist__row checklist__row--done">
                <Check
                  checked
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

/** The add row, as a focus target. Never an item index. */
const ADD_ROW = -1;

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
  onChange,
}: {
  checked: boolean;
  name: string;
  onChange: () => void;
}) {
  return (
    <label className="checklist__check">
      <input type="checkbox" className="checklist__box" checked={checked} onChange={onChange} />
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
      <span className="visually-hidden">{name}</span>
    </label>
  );
}

/** The × on a done row: gone for good, not reopened. */
function DeleteItem({ text, onClick }: { text: string; onClick: () => void }) {
  return (
    <button
      type="button"
      className="checklist__delete"
      aria-label={`Delete ${text || 'item'}`}
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
 * still has its × as it does under Done.
 */
export function ChecklistPreview({
  body,
  label,
  onToggle,
  onDelete,
}: {
  body: string;
  label: string;
  onToggle: (index: number) => void;
  onDelete: (index: number) => void;
}) {
  const items = parseChecklist(body);
  return (
    <ul className="checklist checklist--preview" role="list" aria-label={label}>
      {items.map((item, index) => (
        <li
          key={index}
          className={item.done ? 'checklist__row checklist__row--done' : 'checklist__row'}
        >
          <Check
            checked={item.done}
            name={item.text || `Item ${String(index + 1)}`}
            onChange={() => {
              onToggle(index);
            }}
          />
          <span className="checklist__text">{item.text}</span>
          {item.done && (
            <DeleteItem
              text={item.text}
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
