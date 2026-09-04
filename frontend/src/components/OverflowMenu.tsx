import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';

import { Icon } from './Icon.tsx';

/**
 * The "More" control: a vertical ellipsis that opens a short list of actions
 * for one row.
 *
 * A real menu — `aria-haspopup="menu"` on the trigger, `role="menu"` with
 * `menuitem`s beneath it, arrow keys between items, Escape and a tap outside
 * to close, focus back on the trigger afterwards — because a row of four
 * inline buttons is what it replaces, and four buttons per recording is a
 * wall of chrome for actions taken once a month.
 *
 * Positioned under its trigger by CSS (`.overflow-menu`), inside the row, so
 * the layout sweep's overlap and bleeding checks see it when it is open and
 * the scroll container clips it like anything else.
 */

export interface OverflowMenuItem {
  label: string;
  onSelect: () => void;
  destructive?: boolean;
  disabled?: boolean;
}

export function OverflowMenu({
  label,
  items,
}: {
  /** The trigger's accessible name, naming the row: "More for recording from Today 14:02". */
  label: string;
  items: readonly OverflowMenuItem[];
}) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const root = rootRef.current;
    root?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus();

    const onPointerDown = (event: PointerEvent): void => {
      if (root && !root.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('keydown', onKeyDown, true);
    };
  }, [open]);

  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    const nodes = Array.from(
      rootRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [],
    );
    if (nodes.length === 0) return;
    const index = nodes.indexOf(document.activeElement as HTMLElement);
    const step = event.key === 'ArrowDown' ? 1 : -1;
    nodes[(index + step + nodes.length) % nodes.length]?.focus();
  };

  return (
    <div ref={rootRef} className="overflow">
      <button
        ref={triggerRef}
        type="button"
        className="overflow__trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => {
          setOpen((current) => !current);
        }}
      >
        <Icon name="more" size={20} />
      </button>

      {open && (
        <div id={menuId} className="overflow-menu" role="menu" onKeyDown={onMenuKeyDown}>
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className="overflow-menu__item"
              data-destructive={item.destructive || undefined}
              disabled={item.disabled}
              onClick={() => {
                setOpen(false);
                item.onSelect();
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
