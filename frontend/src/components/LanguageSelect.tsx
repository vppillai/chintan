import { AUTO_LANGUAGE, LANGUAGES, languageName } from '@/features/settings/languages.ts';

export interface LanguageSelectProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  /**
   * Offers an "inherit" entry, stored as the empty string, labelled with what
   * inheriting currently means — the note screen's control, where the empty
   * string means "use the You screen's default".
   */
  inherit?: { label: string };
  disabled?: boolean;
}

/**
 * One `<select>` for both language controls.
 *
 * A native select, not a custom list: it is a long-ish list chosen rarely, the
 * platform picker is the most usable thing on a phone for that, and it needs
 * no ARIA of its own. Auto-detect sits first because it is the one entry that
 * is not a language; a code the server holds that the curated list does not
 * name is appended so the control always shows the value it is in.
 */
export function LanguageSelect({ id, value, onChange, inherit, disabled }: LanguageSelectProps) {
  const listed =
    value === '' || value === AUTO_LANGUAGE || LANGUAGES.some((language) => language.code === value);

  return (
    <select
      id={id}
      className="settings-select"
      value={value}
      disabled={disabled}
      onChange={(event) => {
        onChange(event.target.value);
      }}
    >
      {inherit && <option value="">{inherit.label}</option>}
      <option value={AUTO_LANGUAGE}>{languageName(AUTO_LANGUAGE)}</option>
      {LANGUAGES.map((language) => (
        <option key={language.code} value={language.code}>
          {language.name}
        </option>
      ))}
      {!listed && <option value={value}>{languageName(value)}</option>}
    </select>
  );
}
