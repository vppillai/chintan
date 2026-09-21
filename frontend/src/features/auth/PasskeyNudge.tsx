import { useState } from 'react';

import { config } from '@/config/env.ts';

import { dismissNudge, nudgeDismissed, passkeyAddUrl, passkeysSupported } from './passkeys.ts';

/**
 * One row at the top of the library: sign in faster next time.
 *
 * One 44 px row — the sentence, then "Set up · Not now" as text actions —
 * rather than the card with a second line of prose it was, which took
 * 135–150 px of the phone before the first note (round-3 T17). The passkey
 * card on You still explains what a passkey is; this is a reminder that it
 * exists, for someone who already has notes to protect (the library shows
 * it only once there is at least one, round-3 T56).
 *
 * Shown on this device until the person answers it — "Set up" hands off to
 * the managed login's passkey page exactly as the You screen's card does,
 * "Not now" remembers the answer in localStorage. A `success` return from
 * that page also dismisses it (`usePasskeyReturn`), so a passkey added from
 * the card silences the nudge too.
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
      <p className="passkey-nudge__title">Sign in faster next time</p>
      <div className="passkey-nudge__actions">
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
        <span className="passkey-nudge__dot" aria-hidden="true">
          ·
        </span>
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
      </div>
    </div>
  );
}
