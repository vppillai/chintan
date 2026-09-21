import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { config } from '@/config/env.ts';

export function NotFoundScreen() {
  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Nothing here</h1>
      </header>
      {/* The instance's own name, not a constant: a staging build is not "Chintan" (round-3 T56). */}
      <p className="screen__empty">That address does not match anything in {config.appName}.</p>
      <p>
        <Link to={ROUTES.home} className="text-link">
          Back to your notes
        </Link>
      </p>
    </div>
  );
}
