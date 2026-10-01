import type { CSSProperties } from 'react';
import { Link, useLocation } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { isPlainClick, useTabNavigation } from '@/app/useTabNavigation.ts';
import { holdProgress } from '@/features/capture/holdGesture.ts';
import { useHoldToTalk } from '@/features/capture/useHoldToTalk.ts';

import { HoldChrome, useCoach } from './HoldChrome.tsx';
import { Icon, type IconName } from './Icon.tsx';
import { RecordButton } from './RecordButton.tsx';
import { useRecordTarget } from './useRecordTarget.ts';

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
 * Tabs are links, not buttons: each is a real URL, so a modified click opens
 * it in a new tab and a screen reader reads a link. A plain click is the
 * app's own move (`useTabNavigation`, R8 F2): Home goes back down to the
 * first entry, and You takes the place above it, so Back never walks every
 * screen visited.
 *
 * The bar is also where push-to-talk is drawn (R8, F5; `HoldChrome`). While
 * the disc is held, the Home slot reads "‹ Slide to cancel", the You slot
 * shows the live level, and the lock pill stands above the disc; a lock hands
 * the take to the capture screen, where the bar is not drawn. The tabs keep
 * their boxes, hidden and inert, so the bar never changes height and nothing
 * in `.app__main` moves. The clock pill rides the bar's top edge where "Into
 * this note" sits.
 */
export function TabBar() {
  const { pathname } = useLocation();
  const [home, you] = TABS as [Tab, Tab];
  const { goHome, goTab } = useTabNavigation();
  const into = useRecordTarget();
  const [coach, retireCoach] = useCoach();
  const hold = useHoldToTalk({ noteId: into, onSent: retireCoach });

  const { phase } = hold.gesture;
  const holding = phase === 'holding';
  const progress = holdProgress(hold.gesture);

  // The finger's offset and the progress toward each line, for the CSS.
  const style = holding
    ? ({
        '--hold-dx': `${String(hold.gesture.dx)}px`,
        '--hold-dy': `${String(hold.gesture.dy)}px`,
        '--hold-cancel': String(progress.cancel),
        '--hold-lock': String(progress.lock),
      } as CSSProperties)
    : undefined;

  return (
    <nav
      className="tab-bar"
      aria-label="Main"
      data-hold={holding ? phase : undefined}
      data-notice={hold.notice ?? undefined}
      style={style}
    >
      <TabLink
        tab={home}
        current={home.matches(pathname)}
        slot="start"
        hidden={holding}
        onGo={goHome}
      />
      <HoldChrome hold={hold} into={into} coach={coach && pathname === ROUTES.home}>
        <RecordButton noteId={into} hold={hold} />
      </HoldChrome>
      <TabLink
        tab={you}
        current={you.matches(pathname)}
        slot="end"
        hidden={holding}
        onGo={() => {
          goTab(you.to);
        }}
      />
    </nav>
  );
}

function TabLink({
  tab,
  current,
  slot,
  hidden,
  onGo,
}: {
  tab: Tab;
  current: boolean;
  slot: 'start' | 'end';
  /** Held: the slot shows the hold, and the link keeps its box. */
  hidden: boolean;
  /** The app's own move for a plain click; the `href` serves the rest. */
  onGo: () => void;
}) {
  return (
    <Link
      to={tab.to}
      className={`tab-bar__tab tab-bar__slot tab-bar__slot--${slot}`}
      aria-current={current ? 'page' : undefined}
      inert={hidden}
      onClick={(event) => {
        if (!isPlainClick(event)) return;
        event.preventDefault();
        onGo();
      }}
    >
      <Icon name={tab.icon} size={22} />
      <span className="tab-bar__label">{tab.label}</span>
    </Link>
  );
}
