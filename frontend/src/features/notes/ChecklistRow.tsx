import {
  useRef,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type Ref,
  type TextareaHTMLAttributes,
} from 'react';

import { Check } from '@/components/CheckMark.tsx';
import { Icon } from '@/components/Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from '@/components/OverflowMenu.tsx';
import { useAutoGrow } from '@/hooks/useAutoGrow.ts';

import type { ChecklistItem } from './checklist.ts';

/**
 * One open row of the Items tab: the grip, the box and the item's field.
 *
 * The grip is the one control for the order and the level (2026-09-26,
 * CL-3; 2026-09-29, R6-CL-2). A press on it lifts the row (`onLift`, the
 * editor's drag hook, which reads up and down as a move and sideways as a
 * level change); the up and down arrows on it move the row one slot
 * (`onStep`) and the right and left arrows change its level (`onNest`); a
 * tap on it — a lift that never moved — opens the row's menu, which the
 * editor builds (`menu`): the path that needs no drag at all (WCAG 2.5.7),
 * and a menu on the grip rather than a ⋮ per row because a phone's width has
 * no room for both beside a dictated sentence.
 *
 * The field carries Keep's keys: Tab takes the row one level in under the
 * one above it and Shift+Tab one level out (`onNest`, which says whether anything
 * changed — when nothing can, the key keeps its meaning and focus moves on,
 * so the list is never a keyboard trap); Enter starts a new item under this
 * one (`onEnter`); Backspace in an emptied item removes it (`onBackspaceEmpty`).
 * What each of those writes to the body is the editor's, which owns the
 * body; the row knows only the keys.
 */
export function ChecklistRow({
  item,
  index,
  position,
  dragging,
  previewDepth,
  flash = false,
  hintId,
  fieldHintId,
  menu,
  gripRef,
  fieldRef,
  onLift,
  onStep,
  onToggle,
  onText,
  onNest,
  onEnter,
  onBackspaceEmpty,
  onBlur,
}: {
  item: ChecklistItem;
  /** The item's index in the body: the drag id and the field's key. */
  index: number;
  /** The row's place among the open rows shown, for its name and the grip map. */
  position: number;
  dragging: boolean;
  /** The depth a sideways drag would give this row on release — the lifted row or one it carries — drawn while it is in the air. */
  previewDepth: number | undefined;
  /** Just added by a recording: marked for a moment after the banner's Show. */
  flash?: boolean;
  hintId: string;
  fieldHintId: string;
  menu: OverflowMenuItem[];
  gripRef: (element: HTMLButtonElement | null) => void;
  fieldRef: (element: HTMLTextAreaElement | null) => void;
  onLift: (event: ReactPointerEvent<HTMLButtonElement>) => void;
  onStep: (by: -1 | 1) => void;
  onToggle: () => void;
  onText: (text: string) => void;
  /** Nests (1) or un-nests (-1) the row, keeping focus where it is; false when nothing could change. */
  onNest: (by: 1 | -1, focus: 'field' | 'grip') => boolean;
  onEnter: () => void;
  onBackspaceEmpty: () => void;
  onBlur: () => void;
}) {
  const id = String(index);

  const onFieldKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>): void => {
    if (event.key === 'Tab') {
      // Keep's keys: Tab nests, Shift+Tab un-nests. When neither can change
      // anything the key keeps its meaning and focus moves on.
      if (onNest(event.shiftKey ? -1 : 1, 'field')) event.preventDefault();
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      onEnter();
      return;
    }
    if (event.key === 'Backspace' && event.currentTarget.value === '') {
      event.preventDefault();
      onBackspaceEmpty();
    }
  };

  const onGripKeyDown = (event: KeyboardEvent<HTMLButtonElement>): void => {
    // Up and down move the row a slot; right and left change its level, as
    // the sideways drag on the same handle does. Taken whether or not the
    // level could change: a grip is a button, and the arrows mean nothing
    // else on one.
    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      event.preventDefault();
      onStep(event.key === 'ArrowUp' ? -1 : 1);
    } else if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
      event.preventDefault();
      onNest(event.key === 'ArrowRight' ? 1 : -1, 'grip');
    }
  };

  return (
    <li
      className={rowClass(item)}
      data-depth={item.depth || undefined}
      data-drag-id={id}
      data-dragging={dragging || undefined}
      data-preview-depth={previewDepth}
      // ARIA 1.2 allows a level on a list item: the indent, for the ear.
      aria-level={item.depth + 1}
      data-flash={flash ? '' : undefined}
    >
      <OverflowMenu
        label={`Move ${item.text || 'item'}`}
        describedBy={hintId}
        items={menu}
        trigger={(props) => (
          <button
            {...props}
            ref={gripRef}
            className="checklist__grip"
            onPointerDown={onLift}
            onKeyDown={onGripKeyDown}
          >
            <Icon name="grip" size={18} />
          </button>
        )}
      />
      <Check checked={item.done} name={item.text || `Item ${String(position + 1)}`} onChange={onToggle} />
      <ItemField
        ref={fieldRef}
        value={item.text}
        aria-label={fieldName(item.depth, position)}
        aria-describedby={fieldHintId}
        enterKeyHint="next"
        onChange={(event) => {
          onText(event.target.value);
        }}
        onKeyDown={onFieldKeyDown}
        onBlur={onBlur}
      />
    </li>
  );
}

/** "Item 3", "Sub-item 3", "Sub-item 3, level 3": the row's place and, below the first sub-level, its level. */
function fieldName(depth: number, position: number): string {
  const name = `${depth > 0 ? 'Sub-item' : 'Item'} ${String(position + 1)}`;
  return depth > 1 ? `${name}, level ${String(depth + 1)}` : name;
}

export function rowClass(item: ChecklistItem): string {
  return item.done ? 'checklist__row checklist__row--done' : 'checklist__row';
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
export function ItemField({
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
