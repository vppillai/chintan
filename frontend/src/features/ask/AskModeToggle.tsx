import { useId } from 'react';

export type FieldMode = 'search' | 'ask';

/**
 * Search | Ask, beside the library's field.
 *
 * Two real radio buttons drawn as one segmented control: arrow keys move
 * between them, a screen reader hears "Search, radio button, 1 of 2", and
 * the checked one is the mode. A pair of toggle buttons would have needed
 * `aria-pressed` on each and a rule that exactly one is pressed; radios say
 * that by construction.
 */
export function AskModeToggle({
  mode,
  onChange,
}: {
  mode: FieldMode;
  onChange: (mode: FieldMode) => void;
}) {
  const name = useId();
  return (
    <fieldset className="field-mode">
      <legend className="visually-hidden">Search or ask</legend>
      {(['search', 'ask'] as const).map((option) => (
        <label key={option} className="field-mode__option">
          <input
            type="radio"
            className="field-mode__input"
            name={name}
            value={option}
            checked={mode === option}
            onChange={() => {
              onChange(option);
            }}
          />
          <span className="field-mode__label">{option === 'ask' ? 'Ask' : 'Search'}</span>
        </label>
      ))}
    </fieldset>
  );
}
