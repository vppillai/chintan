import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { UsageSection } from '@/features/settings/UsageSection.tsx';

/**
 * Usage — reached from the one row on You (round-3 T20).
 *
 * The card was a third of a three-screen You and buried Passkeys and About
 * beneath it; here it is the whole screen, unchanged, for the person who
 * wants the bill rather than the settings. Loaded lazily by the router, so
 * Home never pays for it.
 */
export function UsageScreen() {
  return (
    <div className="screen you">
      <header className="screen__header you__header">
        <Link to={ROUTES.settings} className="back-link">
          <Icon name="back" size={18} />
          <span className="visually-hidden">Back to </span>You
        </Link>
        <h1>Usage</h1>
      </header>
      <UsageSection />
    </div>
  );
}
