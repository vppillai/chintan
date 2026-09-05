import { useId, useState } from 'react';
import { useSearchParams } from 'react-router';

import { config } from '@/config/env.ts';

import {
  PASSKEY_STATUS_PARAM,
  dismissNudge,
  passkeyAddUrl,
  passkeysSupported,
  type PasskeyResult,
} from './passkeys.ts';
import { beginSignIn } from './useAuth.ts';

/**
 * "Passkeys", on You.
 *
 * Cognito's managed login never offers to register a passkey after a password
 * sign-in — it only offers to *use* one that already exists — so nobody ever
 * gets one unless something sends them to the page that makes it. This card
 * is that something. The ceremony cannot run in the app (the relying party is
 * Cognito's domain, see `passkeys.ts`), so the button is a hand-off to the
 * managed login's own `/passkeys/add` page, and the lead sentence says so
 * rather than pretending the app did it. The heading is the noun, not the
 * sign-in page's own "Sign in with a passkey" button: that is what appears
 * *after* this card has done its work, and naming the section after it read
 * as though signing in happened here.
 *
 * The answer comes back on the URL. `usePasskeyReturn` moves it here as
 * `?passkey=…`, and this card renders one sentence per outcome. The dismiss
 * clears the parameter so a reload does not repeat the news.
 */
export function PasskeyCard({
  navigate = (url) => {
    window.location.assign(url);
  },
}: {
  /** Injected by tests; production leaves the document. */
  navigate?: (url: string) => void;
}) {
  const headingId = useId();
  const [params, setParams] = useSearchParams();
  const result = params.get(PASSKEY_STATUS_PARAM) as PasskeyResult | null;
  const supported = passkeysSupported();
  const configured = config.cognitoDomain.length > 0 && config.clientId.length > 0;
  const [signingIn, setSigningIn] = useState(false);

  const clearResult = (): void => {
    const next = new URLSearchParams(params);
    next.delete(PASSKEY_STATUS_PARAM);
    setParams(next, { replace: true });
  };

  return (
    <section className="settings-group" aria-labelledby={headingId}>
      <h2 id={headingId} className="settings-group__title">
        Passkeys
      </h2>

      <p className="settings-group__note">
        A passkey is set up on Cognito&rsquo;s sign-in page, not here: it belongs to that
        page&rsquo;s domain, so the app cannot register one itself. Once you have added one, the
        sign-in page offers <em>Sign in with a passkey</em> in place of your password.
      </p>

      {result === 'success' && (
        <p className="passkey-result" role="status">
          Passkey added. The sign-in page will offer it next time — Face ID, a fingerprint or
          your security key instead of a password.{' '}
          <button type="button" className="text-link passkey-result__dismiss" onClick={clearResult}>
            OK
          </button>
        </p>
      )}

      {result === 'invalid_session' && (
        <div className="passkey-result passkey-result--warn" role="alert">
          <p>
            The sign-in page no longer had your session, so no passkey was added. Sign in again,
            then come back here and try once more.
          </p>
          <div className="passkey-result__actions">
            <button
              type="button"
              className="settings-status__action settings-status__action--primary"
              disabled={signingIn}
              onClick={() => {
                setSigningIn(true);
                clearResult();
                beginSignIn(navigate).catch(() => {
                  setSigningIn(false);
                });
              }}
            >
              {signingIn ? 'Opening sign-in…' : 'Sign in again'}
            </button>
            <button type="button" className="settings-status__action" onClick={clearResult}>
              Not now
            </button>
          </div>
        </div>
      )}

      {result === 'other' && (
        <p className="passkey-result passkey-result--warn" role="alert">
          The sign-in page came back without adding a passkey. You can try again.{' '}
          <button type="button" className="text-link passkey-result__dismiss" onClick={clearResult}>
            OK
          </button>
        </p>
      )}

      {!supported ? (
        <p className="settings-group__note" role="note">
          This browser cannot create passkeys. Open {config.appName} in Safari, Chrome or Edge
          on this device to set one up.
        </p>
      ) : (
        <button
          type="button"
          className="option"
          disabled={!configured}
          onClick={() => {
            // The dismissal is per device and this device is about to have a
            // passkey; the nudge has done its job whichever button they press
            // on the far side.
            dismissNudge('added');
            navigate(passkeyAddUrl());
          }}
        >
          <span className="option__label">Add a passkey on this device</span>
          <span className="option__hint">Opens the sign-in page</span>
        </button>
      )}

      <p className="settings-group__note">
        To remove a passkey, delete it from this device&rsquo;s own passkey manager.
      </p>
    </section>
  );
}
