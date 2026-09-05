import { Link, useLocation } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon, type IconName } from './Icon.tsx';
import { RecordButton } from './RecordButton.tsx';

interface Tab {
  label: string;
  to: string;
  icon: IconName;
  /** Whether the current URL belongs to this tab. */
  matches: (pathname: string) => boolean;
}

const TABS: readonly Tab[] = [
  {
    // "Home", not "Notes": the tab is the way back to the start of the app,
    // and the library's own heading already says "Notes". Two controls with
    // the same word on one screen read as two different places.
    label: 'Home',
    to: ROUTES.notes,
    icon: 'home',
    // A note is stacked on the library, so the Home tab stays lit while
    // reading one — the tab names the section, not the exact URL.
    matches: (pathname) => pathname === ROUTES.home || pathname.startsWith('/notes'),
  },
  {
    label: 'You',
    to: ROUTES.settings,
    icon: 'you',
    matches: (pathname) => pathname === ROUTES.settings,
  },
];

/**
 * The bottom tab bar: Home · Record · You.
 *
 * The record button is centred *in the bar* and is a grid child like the two
 * tabs — not a floating action button. A FAB overlays the last note row in the
 * list and covers the one thing the user scrolled to reach; a bar row in normal
 * flow cannot overlay anything, because the shell reserves the row.
 *
 * Tabs are links, not buttons: each is a navigation to a real URL, which is
 * what makes Back work without any state of its own.
 */
export function TabBar() {
  const { pathname } = useLocation();
  const [home, you] = TABS as [Tab, Tab];

  return (
    <nav className="tab-bar" aria-label="Main">
      <TabLink tab={home} current={home.matches(pathname)} />
      <div className="tab-bar__record">
        <RecordButton />
      </div>
      <TabLink tab={you} current={you.matches(pathname)} />
    </nav>
  );
}

function TabLink({ tab, current }: { tab: Tab; current: boolean }) {
  return (
    <Link to={tab.to} className="tab-bar__tab" aria-current={current ? 'page' : undefined}>
      <Icon name={tab.icon} size={22} />
      <span className="tab-bar__label">{tab.label}</span>
    </Link>
  );
}
