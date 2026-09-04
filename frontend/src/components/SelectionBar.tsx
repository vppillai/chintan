import { useEffect, type ReactNode } from 'react';

/**
 * The bar that appears while things are selected: how many, select all, the
 * actions, Cancel — and one line beneath for what the last action did.
 *
 * Sticky to the foot of the scrolling region, like the note screen's action
 * bar, so it sits directly above the tab bar wherever the list is scrolled
 * to. It used to be a card at the *end* of the list, which on a phone with
 * forty notes was six thousand pixels below the row that had just been
 * selected (QA Q6). Sticky rather than fixed for the reason every other shell
 * row gives: the tab bar owns the viewport's bottom row, and a fixed bar here
 * would sit on it.
 *
 * Escape cancels, from anywhere on the screen: selection is a mode, and a
 * mode needs a way out that does not require finding the button.
 */
export function SelectionBar({
  label,
  count,
  allSelected,
  onSelectAll,
  onCancel,
  status,
  children,
}: {
  /** The toolbar's accessible name: "Bulk actions", "Recording actions". */
  label: string;
  count: number;
  allSelected: boolean;
  onSelectAll: () => void;
  onCancel: () => void;
  /** What the last action did, or where it is up to ("3 of 12…"). */
  status?: ReactNode;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      // A dialog above the bar owns Escape while it is open.
      if (document.querySelector('[role="dialog"]')) return;
      onCancel();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [onCancel]);

  return (
    <div className="selection-bar">
      <div className="selection-bar__row" role="toolbar" aria-label={label}>
        <p className="selection-bar__count" role="status">
          <span className="numeric">{count}</span> selected
        </p>
        <button type="button" className="selection-bar__action" onClick={onSelectAll}>
          {allSelected ? 'Deselect all' : 'Select all'}
        </button>
        {children}
        <button type="button" className="selection-bar__action" onClick={onCancel}>
          Cancel
        </button>
      </div>
      {status && (
        <p className="selection-bar__status" role="status" aria-live="polite">
          {status}
        </p>
      )}
    </div>
  );
}
