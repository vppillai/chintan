/**
 * Whether this device has a biometric credential worth offering to use.
 *
 * The signed-out screen cannot ask. `GET /v1/auth/webauthn/status` is an
 * authenticated route — it answers for *an account*, and signed out there is no
 * account to answer for — so the only honest source is what this device
 * observed while it was signed in.
 *
 * Without it the app offered "Unlock with biometrics" unconditionally, and
 * since signing out deliberately revokes the credential (otherwise the next
 * person to pick up the phone taps unlock and is straight back in), every
 * sign-out produced the same loop: sign in, sign out, see the button, tap it,
 * `POST /v1/auth/webauthn/login/options → 503`. Every time.
 *
 * This is a **hint, not a fact**. It says "there was a credential here when we
 * last looked", which signed-out is exactly the wrong moment to trust: the
 * account can have been changed on another device. So it gates the offer, and
 * the first authoritative answer to the contrary erases it.
 */

const KEY = 'chintan.webauthn.enrolled';

function storage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    // Storage disabled by policy. No hint, so no offer — which is the safe
    // direction: the button simply is not shown.
    return null;
  }
}

/** Records what the server said, or what an enrolment just did. */
export function rememberEnrolment(enrolled: boolean): void {
  const store = storage();
  if (!store) return;
  try {
    if (enrolled) store.setItem(KEY, '1');
    else store.removeItem(KEY);
  } catch {
    /* Quota or private mode. The offer degrades to absent. */
  }
}

/** Erases the hint. Used by sign-out, which revokes the credential itself. */
export function forgetEnrolment(): void {
  rememberEnrolment(false);
}

/**
 * True when this device saw an enrolled credential and has no reason to think
 * it has gone.
 */
export function deviceMayBeEnrolled(): boolean {
  try {
    return storage()?.getItem(KEY) === '1';
  } catch {
    return false;
  }
}
