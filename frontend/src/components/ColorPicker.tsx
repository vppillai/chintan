import { useId, useRef, useState } from 'react';

import { NOTE_COLORS, type NoteColor } from '@/api/schema.ts';

import { colorName } from './colorName.ts';
import { useModalFocus } from './useModalFocus.ts';

/**
 * A note's tint: a grid of swatches, "No colour" first, each a dot of the
 * colour and its name. Native radios, so the group is one Tab stop, the
 * arrows move through it and a screen reader says "Rose, radio button, 2 of
 * 9, checked" — each radio stretched invisibly over its 44 px swatch (its
 * aria-label is the visible name, so the name is said once), the checked
 * one ringed in ink (`home.css`). Used by the
 * Details drawer and by Home's dialog below.
 */
export function ColorPicker({
  value,
  onChange,
}: {
  value: NoteColor;
  onChange: (color: NoteColor) => void;
}) {
  const name = useId();
  const legendId = useId();
  return (
    <fieldset className="color-picker" aria-labelledby={legendId}>
      <legend id={legendId} className="tag-editor__label">
        Colour
      </legend>
      <div className="color-picker__swatches">
        {NOTE_COLORS.map((color) => (
          <label key={color} className="color-picker__swatch" data-color={color}>
            <input
              type="radio"
              className="color-picker__input"
              name={name}
              value={color}
              checked={value === color}
              aria-label={colorName(color)}
              onChange={() => {
                onChange(color);
              }}
            />
            <span aria-hidden="true">{colorName(color)}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/**
 * Home's way to the picker, from a row's ⋮: a small dialog, since a menu
 * holds items rather than a row of swatches. A choice applies at once, so
 * the one button is Done; Escape closes it too (`useModalFocus`).
 */
export function ColorDialog({
  title,
  value,
  onChange,
  onClose,
}: {
  title: string;
  value: NoteColor;
  onChange: (color: NoteColor) => void;
  onClose: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  // The choice shows at once: the row's cache is patched only after the
  // mutation's async onMutate, and a controlled radio would spring back
  // until then.
  const [current, setCurrent] = useState(value);
  useModalFocus(panelRef, onClose);
  return (
    <div className="dialog-layer">
      <div className="dialog-scrim" aria-hidden="true" />
      <div ref={panelRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <h2 id={titleId} className="dialog__title">
          Colour for “{title}”
        </h2>
        <ColorPicker
          value={current}
          onChange={(color) => {
            setCurrent(color);
            onChange(color);
          }}
        />
        <div className="dialog__actions">
          <button type="button" className="dialog__action dialog__action--primary" onClick={onClose}>
            Done
          </button>
        </div>
      </div>
    </div>
  );
}
