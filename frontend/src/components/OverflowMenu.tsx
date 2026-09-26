import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

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
 *
 * The trigger is the ⋮ unless the owner renders its own (`trigger`): the
 * checklist's grip is a drag handle that is also the row's menu button, and
 * it spreads the same props, so the menu's wiring — the name, the popup
 * semantics, the toggle — is one thing whichever control wears it.
 */

export interface OverflowMenuItem {
  label: string;
  onSelect: () => void;
  destructive?: boolean;
  disabled?: boolean;
}

/**
 * What the menu's trigger must carry, whichever button it is. No ref: the
 * menu finds its own trigger by the popup attribute when Escape hands focus
 * back, so an owner's render function is handed nothing it could read during
 * render.
 */
export interface OverflowMenuTriggerProps {
  type: 'button';
  'aria-label': string;
  'aria-describedby': string | undefined;
  'aria-haspopup': 'menu';
  'aria-expanded': boolean;
  'aria-controls': string | undefined;
  onClick: () => void;
}

export function OverflowMenu({
  label,
  describedBy,
  items,
  triggerRef: ownerRef,
  trigger,
}: {
  /** The trigger's accessible name, naming the row: "More for recording from Today 14:02". */
  label: string;
  /**
   * The id of what says which row this is, when the name does not: a note
   * row's ⋮ is "More", described by the title beside it, so that the title
   * belongs to the row's own name alone and nothing else answers to it.
   */
  describedBy?: string;
  items: readonly OverflowMenuItem[];
  /**
   * The owner's handle on the trigger, when what an item opens should hand
   * focus back to it on closing. Selecting an item unmounts the focused
   * menuitem, so without this the owner has nothing to return to.
   */
  triggerRef?: RefObject<HTMLButtonElement | null>;
  /** The owner's own control to open the menu from, in place of the ⋮; it spreads the props it is given. */
  trigger?: (props: OverflowMenuTriggerProps) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const ownRef = useRef<HTMLButtonElement>(null);
  const triggerRef = ownerRef ?? ownRef;

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
        // The ⋮ is in the ref; an owner's own trigger is found by what makes it one.
        (triggerRef.current ?? root?.querySelector<HTMLElement>('[aria-haspopup="menu"]'))?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('keydown', onKeyDown, true);
    };
  }, [open, triggerRef]);

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

  const triggerProps: OverflowMenuTriggerProps = {
    type: 'button',
    'aria-label': label,
    'aria-describedby': describedBy,
    'aria-haspopup': 'menu',
    'aria-expanded': open,
    'aria-controls': open ? menuId : undefined,
    onClick: () => {
      setOpen((current) => !current);
    },
  };

  return (
    <div ref={rootRef} className="overflow">
      {trigger ? (
        trigger(triggerProps)
      ) : (
        <button ref={triggerRef} className="overflow__trigger" {...triggerProps}>
          <Icon name="more" size={20} />
        </button>
      )}

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
