import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';

import { useSession } from '@/api/ApiProvider.tsx';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { Icon } from '@/components/Icon.tsx';

import { identityFromIdToken, initialFor, signedInLabel } from './identity.ts';
import { hasUnsentWork, performSignOut, readUnsentWork } from './signOut.ts';

/**
 * Who is signed in, at the top of You, with the way out beside it.
 *
 * The screen is called You and used to open on a theme picker; the person it
 * is named after appeared nowhere on it. The header is the account — a serif
 * initial in a roundel, the email from the id token, how long ago the session
 * began — and Sign out is a quiet text action on the second line rather than
 * the accent-bordered card it was, sitting between the passkey section and
 * About as though it were one more setting. It is the one control on the
 * screen that ends something, and it reads as such by being the only text
 * action here, not by being red.
 *
 * It lives on You and not on the tab bar on purpose: that strip is navigation,
 * operated one-handed while walking, and an irreversible end-my-session
 * control a stray thumb away from Home is how someone signs out of a live
 * recording by accident. Two taps (You → Sign out) plus a confirm.
 *
 * The confirm is not ceremony. Unsent audio lives in IndexedDB and nowhere
 * else, and signing out clears it, so this dialog is the last moment anyone
 * can be told that a recording is about to go.
 */
export function AccountHeader() {
  const session = useSession();
  const queryClient = useQueryClient();
  const identity = identityFromIdToken(session.current()?.idToken);
  const initial = initialFor(identity.email);
  const since = signedInLabel(identity.signedInAt);

  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  // Read before the dialog opens, so the copy can name what is at risk.
  const { data: unsent } = useQuery({
    queryKey: ['auth', 'unsent-work'],
    queryFn: () => readUnsentWork(),
    staleTime: 0,
  });

  const work = unsent ?? { captures: 0, queued: 0 };
  const risky = hasUnsentWork(work);

  return (
    <section className="account" aria-label="Account">
      <div className="account__roundel" aria-hidden="true">
        {initial ?? <Icon name="you" size={26} />}
      </div>
      <div className="account__text">
        <p className="account__name">{identity.email ?? 'Signed in'}</p>
        <p className="account__meta">
          <span>{since ?? (identity.email ? 'Signed in on this device' : 'On this device')}</span>
          <span className="account__dot" aria-hidden="true">
            ·
          </span>
          <button
            type="button"
            className="text-link account__sign-out"
            disabled={busy}
            onClick={() => {
              void queryClient.invalidateQueries({ queryKey: ['auth', 'unsent-work'] });
              setMessage(null);
              setOpen(true);
            }}
          >
            {busy ? 'Signing out…' : 'Sign out'}
          </button>
        </p>
        {message && (
          <p className="account__message" role="status" aria-live="polite">
            {message}
          </p>
        )}
      </div>

      <ConfirmDialog
        open={open}
        title={risky ? 'Sign out and lose unsent work?' : 'Sign out?'}
        body={confirmBody(work)}
        confirmLabel={risky ? 'Sign out and discard' : 'Sign out'}
        destructive
        onCancel={() => {
          setOpen(false);
        }}
        onConfirm={() => {
          setOpen(false);
          setBusy(true);
          void performSignOut({ session, queryClient })
            .then(() => {
              setBusy(false);
            })
            .catch(() => {
              setBusy(false);
              setMessage('Could not complete the sign-out. Nothing was changed.');
            });
        }}
      />
    </section>
  );
}

/**
 * Names exactly what is about to be destroyed, in the order it matters, and
 * then what signing out does: here and at the identity provider, so the next
 * person to open the app is asked to sign in.
 */
export function confirmBody(work: { captures: number; queued: number }): string {
  const losses: string[] = [];

  if (work.captures > 0) {
    losses.push(
      work.captures === 1
        ? 'one recording that has not reached the server yet'
        : `${String(work.captures)} recordings that have not reached the server yet`,
    );
  }
  if (work.queued > 0) {
    losses.push(
      work.queued === 1 ? 'one unsynced change' : `${String(work.queued)} unsynced changes`,
    );
  }

  const ends =
    'Signs you out here and ends the session with the identity provider, so the next person is asked to sign in.';

  if (losses.length === 0) {
    return `Your notes stay on the server. Nothing is waiting to sync from this device. ${ends}`;
  }

  return `This device is still holding ${losses.join(' and ')}. Signing out deletes ${
    losses.length > 1 ? 'them' : 'it'
  } — ${
    work.captures > 0 ? 'the audio is not saved anywhere else' : 'the changes are not saved anywhere else'
  }. ${ends}`;
}
