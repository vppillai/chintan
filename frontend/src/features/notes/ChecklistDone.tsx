import { useId, useState } from 'react';

import { Check } from '@/components/CheckMark.tsx';
import { Icon } from '@/components/Icon.tsx';

import { rowClass } from './ChecklistRow.tsx';
import type { ChecklistItem } from './checklist.ts';

/**
 * The done items, under a Done heading, greyed and struck through, each with
 * its checkbox to reopen it and a × to delete it. Done rows have no grip:
 * their order is the body's, and nothing shows it.
 *
 * Done is a disclosure, remembered per note for the session
 * (`sessionStorage`, open by default, the NoteTabs pattern), with Uncheck
 * all and Delete done beside it; what those write is the editor's
 * (`onReopenAll`, `onDeleteDone`), as every body change is.
 */
export function ChecklistDone({
  noteId,
  done,
  onToggle,
  onRemove,
  onReopenAll,
  onDeleteDone,
}: {
  noteId: string;
  /** The done items with their body indices, in body order. */
  done: readonly { item: ChecklistItem; index: number }[];
  onToggle: (index: number, item: ChecklistItem) => void;
  onRemove: (index: number) => void;
  onReopenAll: () => void;
  onDeleteDone: () => void;
}) {
  const doneId = useId();
  const doneListId = useId();
  const [doneOpen, setDoneOpen] = useState(() => readDoneOpen(noteId));

  const toggleDone = (): void => {
    const next = !doneOpen;
    setDoneOpen(next);
    rememberDoneOpen(noteId, next);
  };

  return (
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
          <button type="button" className="checklist__done-action" onClick={onReopenAll}>
            Uncheck all
          </button>
          <span aria-hidden="true">·</span>
          <button type="button" className="checklist__done-action" onClick={onDeleteDone}>
            Delete done
          </button>
        </div>
      </div>
      <ul id={doneListId} className="checklist" role="list" hidden={!doneOpen}>
        {done.map(({ item, index }) => (
          <li key={index} className={rowClass(item)} data-depth={item.depth || undefined}>
            <span className="checklist__grip-space" aria-hidden="true" />
            <Check
              checked={item.done}
              name={item.text || 'Item'}
              onChange={() => {
                onToggle(index, item);
              }}
            />
            <span className="checklist__text">{item.text}</span>
            <DeleteItem
              text={item.text}
              onClick={() => {
                onRemove(index);
              }}
            />
          </li>
        ))}
      </ul>
    </section>
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

/** The × on a done row: gone for good, not reopened. */
export function DeleteItem({
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
