import type { AuthGateState } from './useAuth.ts';

/**
 * Where a signed-out visitor lands.
 *
 * There was no such place. A visitor with no token got the full shell — record
 * button, library strip, progress card — every screen of which immediately
 * fired an authenticated request and took a 401, with nothing on screen
 * admitting the app had no idea who they were. `GET /v1/captures?status=pending
 * → 401` in the console was the only symptom.
 *
 * Deliberately not an automatic redirect to the hosted UI. An app that bounces
 * to an identity provider the instant it loads cannot be opened offline, cannot
 * be reasoned about from the address bar, and turns Back into a redirect loop.
 * One button, pressed on purpose.
 *
 * One button only. The hosted UI is where every credential lives — password,
 * and a passkey once the user has added one there — so there is no second way
 * in for this screen to offer, and nothing here has to know how the person
 * chose to prove who they are.
 */
export function SignedOutScreen({ phase, error, signIn, configured }: AuthGateState) {
  const busy = phase === 'redirecting' || phase === 'exchanging';

  return (
    <div className="signed-out">
      <h1 className="signed-out__title">Chintan</h1>
      <p className="signed-out__hint">Speak a thought. It files itself.</p>

      {error && (
        <p className="signed-out__error" role="alert">
          {error}
        </p>
      )}

      {configured ? (
        <>
          <button
            type="button"
            className="signed-out__action"
            onClick={signIn}
            disabled={busy}
          >
            {phase === 'exchanging'
              ? 'Signing you in…'
              : phase === 'redirecting'
                ? 'Opening sign-in…'
                : 'Sign in'}
          </button>

          <p className="signed-out__note">
            Signing in happens on the Cognito page, where you can use a passkey once you have
            set one up.
          </p>
        </>
      ) : (
        /*
         * An unconfigured bundle is a broken deploy, not a runtime state to
         * absorb — `config.required()` returns an empty string and only warns
         * in dev. Saying so beats a button that cannot work.
         */
        <p className="signed-out__error" role="alert">
          This build has no sign-in configured. Its <span className="numeric">VITE_</span>
          variables were not set at build time.
        </p>
      )}

      <p className="signed-out__note">
        Anything you recorded on this device is still here and will be offered back once
        you are signed in.
      </p>
    </div>
  );
}
