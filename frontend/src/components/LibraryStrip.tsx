import { NavLink } from 'react-router';

import { SHEET_TABS, SHEET_TAB_LABELS, pathForTab } from '@/app/sheet.ts';

import { Icon, type IconName } from './Icon.tsx';

const TAB_ICONS: Record<string, IconName> = {
  notes: 'notes',
  search: 'search',
  you: 'you',
};

/**
 * The collapsed sheet (spec §5.2): a persistent strip showing
 * Notes · Search · You, so each is one tap rather than a discovered gesture.
 *
 * These are links, not buttons, because each is a navigation to a real URL —
 * which is also what makes Back collapse the strip back to the record surface.
 */
export function LibraryStrip() {
  return (
    <nav className="strip" aria-label="Library">
      <ul className="strip__list" role="list">
        {SHEET_TABS.map((tab) => (
          <li key={tab} className="strip__item">
            <NavLink to={pathForTab(tab)} className="strip__link">
              <Icon name={TAB_ICONS[tab] ?? 'notes'} size={20} />
              <span className="strip__label">{SHEET_TAB_LABELS[tab]}</span>
            </NavLink>
          </li>
        ))}
      </ul>
      <span className="strip__grip" aria-hidden="true" />
    </nav>
  );
}
