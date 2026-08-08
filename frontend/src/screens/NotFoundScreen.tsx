import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

export function NotFoundScreen() {
  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Nothing here</h1>
      </header>
      <p className="screen__empty">That address does not match anything in Chintan.</p>
      <p>
        <Link to={ROUTES.home} className="text-link">
          Back to recording
        </Link>
      </p>
    </div>
  );
}
