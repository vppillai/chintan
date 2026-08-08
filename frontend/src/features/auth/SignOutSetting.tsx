import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';

import { useApi, useSession } from '@/api/ApiProvider.tsx';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';

import { hasUnsentWork, performSignOut, readUnsentWork } from './signOut.ts';

/**
 * Sign out.
 *
 * Lives in Settings — two taps from anywhere (You → Sign out) plus a confirm.
 * It is deliberately *not* on the library strip: that strip is navigation,
 * operated one-handed while walking, and putting an irreversible
 * end-my-session control a stray thumb away from "Notes" is how someone signs
 * out of a live recording by accident.
 *
 * The confirm is not ceremony. Unsent audio lives in IndexedDB and nowhere
 * else, and signing out clears it, so this dialog is the last moment anyone can
 * be told that a recording is about to go.
 */
export function SignOutSetting() {
  const api = useApi();
  const session = useSession();
  const queryClient = useQueryClient();
  const headingId = useId();

  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  // Read before the dialog opens, so the copy can name what is at risk.
  const { data: unsent } = useQuery({
    queryKey: ['auth', 'unsent-work'],
    queryFn: () => readUnsentWork(),
    staleTime: 0,
  });

  const enrolled = useQuery({
    queryKey: ['webauthn', 'status'],
    queryFn: () => api.webauthnStatus(),
    retry: false,
  }).data?.enrolled ?? false;

  const work = unsent ?? { captures: 0, queued: 0 };
  const risky = hasUnsentWork(work);

  return (
    <section className="settings-group" aria-labelledby={headingId}>
      <h2 id={headingId} className="settings-group__title">
        Session
      </h2>

      <button
        type="button"
        className="option option--destructive"
        disabled={busy}
        onClick={() => {
          void queryClient.invalidateQueries({ queryKey: ['auth', 'unsent-work'] });
          setMessage(null);
          setOpen(true);
        }}
      >
        <span className="option__label">Sign out</span>
        <span className="option__hint">{busy ? 'Signing out…' : 'On this device'}</span>
      </button>

      <p className="settings-group__note">
        Signs you out here and ends the session with the identity provider, so the next
        person is asked to sign in.
      </p>

      {message && (
        <p className="settings-group__note" role="status" aria-live="polite">
          {message}
        </p>
      )}

      <ConfirmDialog
        open={open}
        title={risky ? 'Sign out and lose unsent work?' : 'Sign out?'}
        body={confirmBody(work, enrolled)}
        confirmLabel={risky ? 'Sign out and discard' : 'Sign out'}
        destructive
        onCancel={() => {
          setOpen(false);
        }}
        onConfirm={() => {
          setOpen(false);
          setBusy(true);
          void performSignOut({
            session,
            queryClient,
            api,
            revokeBiometric: enrolled,
          })
            .then((result) => {
              setBusy(false);
              if (result.biometricLeftBehind) {
                setMessage(
                  'Signed out, but biometric unlock could not be turned off — you were offline. Turn it off from another device if this one is not yours.',
                );
              }
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

/** Names exactly what is about to be destroyed, in the order it matters. */
export function confirmBody(
  work: { captures: number; queued: number },
  enrolled: boolean,
): string {
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

  const parts: string[] = [];

  if (losses.length > 0) {
    parts.push(
      `This device is still holding ${losses.join(' and ')}. Signing out deletes ${
        losses.length > 1 ? 'them' : 'it'
      } — ${
        work.captures > 0 ? 'the audio is not saved anywhere else' : 'the changes are not saved anywhere else'
      }.`,
    );
  } else {
    parts.push('Your notes stay on the server. Nothing is waiting to sync from this device.');
  }

  if (enrolled) {
    // Said plainly, because it is irreversible and it is the difference between
    // a sign-out that holds and one the next person walks straight through.
    parts.push(
      'Biometric unlock will also be turned off, so nobody can unlock back into this account on this device. Turning it on again needs a fresh sign-in.',
    );
  }

  return parts.join(' ');
}
