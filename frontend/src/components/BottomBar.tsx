import { NavLink, useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { SHEET_TAB_LABELS, pathForTab, type SheetTab } from '@/app/sheet.ts';

import { Icon, type IconName } from './Icon.tsx';
import { RecordButton } from './RecordButton.tsx';

const LEFT_TABS: readonly SheetTab[] = ['notes', 'search'];
const RIGHT_TABS: readonly SheetTab[] = ['you'];

const TAB_ICONS: Record<SheetTab, IconName> = {
  notes: 'notes',
  search: 'search',
  you: 'you',
};

function Tab({ tab }: { tab: SheetTab }) {
  return (
    <NavLink to={pathForTab(tab)} className="bottom-bar__tab">
      <Icon name={TAB_ICONS[tab]} size={20} />
      <span className="bottom-bar__tab-label">{SHEET_TAB_LABELS[tab]}</span>
    </NavLink>
  );
}

/**
 * The expanded sheet's bottom bar (spec §5.2).
 *
 * The record button is centred *in the bar* and is a grid child like every
 * other control — not a floating action button. That is the whole point: a
 * FAB overlays the last note row in the list and covers the one thing the
 * user scrolled to reach.
 *
 * The bar is symmetric by construction (two controls, record, two controls),
 * so the record target sits on the horizontal centre line of the viewport.
 */
export function BottomBar() {
  const navigate = useNavigate();

  return (
    <nav className="bottom-bar" aria-label="Library">
      <div className="bottom-bar__group">
        {LEFT_TABS.map((tab) => (
          <Tab key={tab} tab={tab} />
        ))}
      </div>

      <div className="bottom-bar__record">
        <RecordButton variant="bar" />
      </div>

      <div className="bottom-bar__group">
        {RIGHT_TABS.map((tab) => (
          <Tab key={tab} tab={tab} />
        ))}
        <button
          type="button"
          className="bottom-bar__tab"
          onClick={() => {
            void navigate(ROUTES.home);
          }}
        >
          <Icon name="chevron" size={20} className="bottom-bar__collapse-icon" />
          <span className="bottom-bar__tab-label">Close</span>
        </button>
      </div>
    </nav>
  );
}
