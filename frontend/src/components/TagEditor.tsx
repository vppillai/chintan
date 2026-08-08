import { useId, useState } from 'react';

/**
 * A list of short strings — tags, or a note's alternative names.
 *
 * Each existing value is a real button that removes itself, so the list is
 * keyboard-operable without a pointer, and the accessible name says what the
 * button does rather than just naming the tag.
 */

export interface TagEditorProps {
  label: string;
  values: readonly string[];
  placeholder: string;
  /** Contract caps: 32 tags of 40 runes, 32 aliases of 120. */
  maxItems?: number;
  maxLength?: number;
  onChange: (values: string[]) => void;
  /** Called when the user finishes, so the caller can flush an autosave. */
  onCommit?: () => void;
}

export function TagEditor({
  label,
  values,
  placeholder,
  maxItems = 32,
  maxLength = 40,
  onChange,
  onCommit,
}: TagEditorProps) {
  const [entry, setEntry] = useState('');
  const inputId = useId();
  const listId = useId();

  const add = (): void => {
    const value = entry.trim().slice(0, maxLength);
    if (!value) return;
    // Silently ignoring a duplicate is right: the user's intent is "this tag
    // should be on this note", which is already true.
    if (values.includes(value) || values.length >= maxItems) {
      setEntry('');
      return;
    }
    onChange([...values, value]);
    setEntry('');
    onCommit?.();
  };

  return (
    <section className="tag-editor">
      <h2 className="tag-editor__label" id={listId}>
        {label}
      </h2>

      <ul className="tag-list" role="list" aria-labelledby={listId}>
        {values.map((value) => (
          <li key={value}>
            <button
              type="button"
              className="tag tag--removable"
              onClick={() => {
                onChange(values.filter((item) => item !== value));
                onCommit?.();
              }}
            >
              <span>{value}</span>
              <span aria-hidden="true">×</span>
              <span className="visually-hidden">Remove {value}</span>
            </button>
          </li>
        ))}
      </ul>

      <label className="visually-hidden" htmlFor={inputId}>
        {placeholder}
      </label>
      <input
        id={inputId}
        className="tag-editor__input"
        value={entry}
        placeholder={placeholder}
        maxLength={maxLength}
        disabled={values.length >= maxItems}
        onChange={(event) => {
          setEntry(event.target.value);
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ',') {
            // Enter inside a form would submit it; this input is a list
            // builder, not a form field.
            event.preventDefault();
            add();
          }
          if (event.key === 'Backspace' && entry === '' && values.length > 0) {
            onChange(values.slice(0, -1));
          }
        }}
        onBlur={add}
      />
    </section>
  );
}
