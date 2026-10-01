import { useCallback, useState, type CSSProperties, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { holdProgress, type HoldNotice } from '@/features/capture/holdGesture.ts';
import { Waveform } from '@/features/capture/Waveform.tsx';
import { formatElapsed } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { useHoldToTalk } from '@/features/capture/useHoldToTalk.ts';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

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

/** What the clock pill and the status line say for each notice. */
const NOTICES: Record<HoldNotice, { pill: string; spoken: string }> = {
  sent: { pill: 'Sent', spoken: 'Sent' },
  short: { pill: 'Too short — hold to talk', spoken: 'Too short' },
  cancelled: { pill: 'Cancelled', spoken: 'Cancelled' },
  busy: { pill: 'Still sending the last one…', spoken: 'Still sending the last one…' },
  blocked: { pill: 'Microphone blocked', spoken: 'Microphone blocked' },
  unavailable: { pill: 'No microphone', spoken: 'No microphone' },
};

/*
 * The first-run coach: "Hold to talk · tap to record" above the disc on Home
 * for the first three launches, until the first hold that sends (owner
 * decision D4). A launch is a page load, counted once however often the bar
 * mounts. Storage that throws (a private window) shows nothing.
 */
const COACH_KEY = 'chintan.coach.ptt';
const COACH_LAUNCHES = 3;
let launchCounted = false;

function readCoach(): boolean {
  try {
    const stored = window.localStorage.getItem(COACH_KEY);
    if (stored === 'done') return false;
    const launches = Number(stored ?? '0') + (launchCounted ? 0 : 1);
    if (!launchCounted) window.localStorage.setItem(COACH_KEY, String(launches));
    launchCounted = true;
    return launches <= COACH_LAUNCHES;
  } catch {
    return false;
  }
}

function retireCoach(): void {
  try {
    window.localStorage.setItem(COACH_KEY, 'done');
  } catch {
    /* Storage denied: the coach simply shows again next launch. */
  }
}

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
 *
 * The bar is also where push-to-talk is drawn (R8, F5). While the disc is
 * held, the Home slot reads "‹ Slide to cancel", the You slot shows the live
 * level, and the lock pill stands above the disc; locked, the slots are
 * Discard and Stop and the disc is Send. The tabs keep their boxes, hidden
 * and inert, so the bar never changes height and nothing in `.app__main`
 * moves. The clock pill rides the bar's top edge where "Into this note" sits.
 */
export function TabBar() {
  const { pathname } = useLocation();
  const [home, you] = TABS as [Tab, Tab];
  const into = useRecordTarget();
  const [coach, setCoach] = useState(readCoach);
  const onSent = useCallback(() => {
    retireCoach();
    setCoach(false);
  }, []);
  const hold = useHoldToTalk({ noteId: into, onSent });
  const model = useCaptureStore((state) => state.model);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);
  const reducedMotion = useReducedMotion();

  const { phase } = hold.gesture;
  const { notice } = hold;

  const holding = phase === 'holding';
  const locked = phase === 'locked';
  const active = holding || locked;
  const progress = holdProgress(hold.gesture);
  // The recording's own target, not the route's: a locked take aimed at a
  // note keeps saying so on Home, and one aimed at a new note says nothing
  // when you open a note.
  const clock = `${formatElapsed(model.elapsedMs)}${model.noteId !== null ? ' · Into this note' : ''}`;

  let pill: ReactNode = null;
  if (notice) pill = NOTICES[notice].pill;
  else if (locked) {
    pill = (
      <>
        <Icon name="lock" size={14} />
        {model.state === 'requesting' ? 'Allow the microphone…' : clock}
      </>
    );
  } else if (holding) {
    pill = (
      <>
        <span className="tab-bar__dot" />
        {clock}
      </>
    );
  } else if (into !== null) pill = 'Into this note';
  else if (coach && pathname === ROUTES.home) pill = 'Hold to talk · tap to record';

  const spoken = notice
    ? NOTICES[notice].spoken
    : locked
      ? model.state === 'requesting'
        ? 'Allow the microphone…'
        : 'Locked. Recording hands-free.'
      : holding
        ? 'Recording'
        : '';

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
      data-hold={active ? phase : undefined}
      data-notice={notice ?? undefined}
      style={style}
    >
      <TabLink tab={home} current={home.matches(pathname)} slot="start" hidden={active} />
      <div className="tab-bar__record">
        {holding && (
          <span
            className="tab-bar__lock"
            aria-hidden="true"
            data-leaning={progress.cancel > progress.lock ? 'cancel' : undefined}
          >
            <Icon name="lock-open" size={20} className="tab-bar__padlock" />
            <Icon name="chevrons-up" size={20} className="tab-bar__chevrons" />
          </span>
        )}
        <RecordButton noteId={into} hold={hold} />
      </div>
      {/* The sighted reading of the button's name and the hold's clock; the status line speaks. */}
      {pill !== null && (
        <span className="tab-bar__into" aria-hidden="true">
          {pill}
        </span>
      )}
      <TabLink tab={you} current={you.matches(pathname)} slot="end" hidden={active} />

      {holding && (
        <span
          className="tab-bar__slot tab-bar__slot--start tab-bar__cancel"
          aria-hidden="true"
          data-near={progress.cancel >= 0.6 || undefined}
        >
          <Icon name="trash" size={20} className="tab-bar__cancel-glyph" />
          <span>‹ Slide to cancel</span>
        </span>
      )}
      {holding && (
        <span className="tab-bar__slot tab-bar__slot--end tab-bar__level" aria-hidden="true">
          <Waveform
            key={model.localId}
            read={read}
            active={model.state === 'recording'}
            reducedMotion={reducedMotion}
          />
        </span>
      )}
      {locked && (
        <button
          type="button"
          className="tab-bar__tab tab-bar__slot tab-bar__slot--start"
          data-armed={hold.gesture.discardArmedUntil !== null || undefined}
          aria-label={
            hold.gesture.discardArmedUntil !== null ? 'Tap again to discard' : 'Discard recording'
          }
          onClick={hold.discard}
        >
          <Icon name="trash" size={22} />
          <span className="tab-bar__label">
            {hold.gesture.discardArmedUntil !== null ? 'Discard?' : 'Discard'}
          </span>
        </button>
      )}
      {locked && (
        <button
          type="button"
          className="tab-bar__tab tab-bar__slot tab-bar__slot--end"
          aria-label="Stop and review"
          onClick={hold.stop}
        >
          <Icon name="stop" size={22} />
          <span className="tab-bar__label">Stop</span>
        </button>
      )}

      <p className="visually-hidden" role="status" aria-live="polite">
        {spoken}
      </p>
    </nav>
  );
}

function TabLink({
  tab,
  current,
  slot,
  hidden,
}: {
  tab: Tab;
  current: boolean;
  slot: 'start' | 'end';
  /** Held or locked: the slot shows the hold, and the link keeps its box. */
  hidden: boolean;
}) {
  return (
    <Link
      to={tab.to}
      className={`tab-bar__tab tab-bar__slot tab-bar__slot--${slot}`}
      aria-current={current ? 'page' : undefined}
      inert={hidden}
    >
      <Icon name={tab.icon} size={22} />
      <span className="tab-bar__label">{tab.label}</span>
    </Link>
  );
}
