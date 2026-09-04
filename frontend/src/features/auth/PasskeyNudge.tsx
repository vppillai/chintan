import { useState } from 'react';

import { config } from '@/config/env.ts';

import { dismissNudge, nudgeDismissed, passkeyAddUrl, passkeysSupported } from './passkeys.ts';

/**
 * One row at the top of the library: sign in faster next time.
 *
 * Shown on this device until the person answers it — "Set up" hands off to the
 * managed login's passkey page exactly as the You screen's card does, "Not now"
 * remembers the answer in localStorage. A `success` return from that page
 * also dismisses it (`usePasskeyReturn`), so a passkey added from the card
 * silences the nudge too.
 *
 * Hidden outright where it could not work: a browser without WebAuthn, or a
 * build with no Cognito to hand off to.
 */
export function PasskeyNudge({
  navigate = (url) => {
    window.location.assign(url);
  },
}: {
  navigate?: (url: string) => void;
}) {
  const [hidden, setHidden] = useState(
    () =>
      nudgeDismissed() ||
      !passkeysSupported() ||
      config.cognitoDomain.length === 0 ||
      config.clientId.length === 0,
  );

  if (hidden) return null;

  return (
    <div className="passkey-nudge" role="note" aria-label="Passkey suggestion">
      <div>
        <p className="passkey-nudge__title">Sign in faster next time</p>
        <p className="passkey-nudge__detail">
          Add a passkey and the sign-in page will take Face ID, a fingerprint or your security
          key instead of a password.
        </p>
      </div>
      <div className="passkey-nudge__actions">
        <button
          type="button"
          className="passkey-nudge__action"
          onClick={() => {
            dismissNudge('not-now');
            setHidden(true);
          }}
        >
          Not now
        </button>
        <button
          type="button"
          className="passkey-nudge__action passkey-nudge__action--primary"
          onClick={() => {
            dismissNudge('added');
            navigate(passkeyAddUrl());
          }}
        >
          Set up
        </button>
      </div>
    </div>
  );
}
