import { useId } from 'react';

import { Icon } from '@/components/Icon.tsx';
import { THEME_LABELS, THEME_PREFERENCES } from '@/theme/theme.ts';
import { useTheme } from '@/theme/useTheme.ts';

export function SettingsScreen() {
  const { preference, resolved, setPreference } = useTheme();
  const groupId = useId();

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>You</h1>
      </header>

      <section className="settings-group" aria-labelledby={groupId}>
        <h2 id={groupId} className="settings-group__title">
          Appearance
        </h2>

        <ul className="option-list" role="list">
          {THEME_PREFERENCES.map((option) => {
            const selected = option === preference;
            return (
              <li key={option}>
                <button
                  type="button"
                  className="option"
                  aria-pressed={selected}
                  onClick={() => {
                    setPreference(option);
                  }}
                >
                  <span className="option__label">{THEME_LABELS[option]}</span>
                  {selected && <Icon name="check" size={18} className="option__check" />}
                </button>
              </li>
            );
          })}
        </ul>

        <p className="settings-group__note">
          Currently showing {THEME_LABELS[resolved]}.
        </p>
      </section>
    </div>
  );
}
