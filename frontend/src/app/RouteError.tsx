import { Link, isRouteErrorResponse, useRouteError } from 'react-router';

import { THREAD_KEY } from '@/features/ask/thread.ts';

import { ROUTES } from './routes.ts';

/**
 * Both ways out end the Ask thread. It lives in `sessionStorage`, which a
 * reload keeps, and a thread that broke the screen would otherwise be read
 * back — and re-sent — by the screen being returned to. A question is not a
 * document; losing one is the cheap side of that trade.
 */
function forgetAskThread(): void {
  try {
    sessionStorage.removeItem(THREAD_KEY);
  } catch {
    /* Storage blocked: there is no thread to come back to either. */
  }
}

/**
 * What a render fault looks like instead of nothing.
 *
 * The app had no error boundary at any level, so one bad cache read replaced
 * the entire document with React Router's raw error page: a message, a stack
 * trace, and — counted — zero links and zero buttons. On a phone the only way
 * out was OS Back, which returned to the screen that caused it, or knowing to
 * reload. This is mounted on every route so a broken screen is always a screen
 * with two controls on it.
 *
 * Deliberately reassuring about durability, because that is the true and useful
 * thing to say: notes live on the server and unsent audio lives in IndexedDB.
 * Neither is affected by a component that failed to render.
 */
export function RouteError() {
  const error = useRouteError();

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>This screen could not be drawn</h1>
      </header>

      <p className="screen__empty" role="alert">
        Something went wrong rendering this part of the app. Nothing has been lost — your
        notes are on the server, and any recording waiting to be sent is still on this
        device.
      </p>

      <div className="screen__actions">
        <Link className="screen__action" to={ROUTES.home} onClick={forgetAskThread}>
          Back to your notes
        </Link>
        <button
          type="button"
          className="screen__action"
          onClick={() => {
            forgetAskThread();
            window.location.reload();
          }}
        >
          Reload the app
        </button>
      </div>

      <p className="screen__count">{describeRouteError(error)}</p>
    </div>
  );
}

/**
 * One line a person can quote in a bug report: a route's status, or a fixed
 * sentence. Never the exception's own message — "Cannot read properties of
 * undefined (reading 'map')" is a stack trace's first line, not a sentence
 * for the person holding the phone; the console still has it.
 */
function describeRouteError(error: unknown): string {
  if (isRouteErrorResponse(error)) {
    return `${String(error.status)} ${error.statusText}`;
  }
  return 'No further detail was available.';
}
