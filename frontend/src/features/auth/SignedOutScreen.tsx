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
 */
export function SignedOutScreen({
  phase,
  error,
  signIn,
  unlock,
  needsReEnrolment,
  configured,
}: AuthGateState) {
  const busy = phase === 'redirecting' || phase === 'exchanging' || phase === 'unlocking';

  return (
    <div className="signed-out">
      <h1 className="signed-out__title">Chintan</h1>
      <p className="signed-out__hint">Speak a thought. It files itself.</p>

      {error && (
        <p className="signed-out__error" role="alert">
          {error}
        </p>
      )}

      {/*
        Not an error, and deliberately not worded as one. The assertion was
        good; the vault it should have opened was sealed by a key that has since
        been retired, and the server discarded it. Nothing is wrong with the
        user's finger, so telling them verification failed would send them to
        try it again forever. Signing in once re-enrols them.
      */}
      {needsReEnrolment && (
        <p className="signed-out__error" role="status">
          Biometric unlock has to be set up again on this device. Sign in once, then turn
          it back on in Settings.
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

          {/*
            Beside Sign in, which is its entire purpose: an enrolled user should
            not have to take the hosted-UI round trip. Offered unconditionally
            rather than gated on an enrolment check, because asking the server
            "is this device enrolled?" before anyone has authenticated is a
            question with no session to answer it for — the server answers 503
            if nothing is enrolled and the button says so.
          */}
          {unlock && (
            <button
              type="button"
              className="signed-out__action signed-out__action--quiet"
              onClick={unlock}
              disabled={busy}
            >
              {phase === 'unlocking' ? 'Unlocking…' : 'Unlock with biometrics'}
            </button>
          )}
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
