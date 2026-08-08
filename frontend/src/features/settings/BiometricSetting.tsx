import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';

import { useApi, useSession } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';

import { rememberEnrolment } from '@/features/auth/enrolment.ts';

import { performRegistration, isWebAuthnAvailable } from './webauthn.ts';

/**
 * Biometric unlock.
 *
 * Disabling destroys the KMS-sealed refresh-token vault on the server, so it is
 * confirmed: it is irreversible and re-enrolling requires signing in again.
 */
export function BiometricSetting() {
  const api = useApi();
  const session = useSession();
  const queryClient = useQueryClient();
  const headingId = useId();
  const [pendingDisable, setPendingDisable] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const available = isWebAuthnAvailable();

  const status = useQuery({
    queryKey: ['webauthn', 'status'],
    queryFn: async () => {
      const answer = await api.webauthnStatus();
      /*
       * The only moment the app can learn this. The status route is
       * authenticated, so the signed-out screen — which is where the unlock is
       * offered — has no way to ask; it goes on what this device recorded here.
       */
      rememberEnrolment(answer.enrolled);
      return answer;
    },
    enabled: available,
    retry: false,
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['webauthn', 'status'] });
  };

  const enroll = useMutation({
    mutationFn: async () => {
      const refreshToken = session.current()?.refreshToken;
      if (!refreshToken) {
        // Enrollment vaults the refresh token; without one there is nothing to
        // unlock into later, and enrolling would produce a credential that
        // cannot actually sign the user in.
        throw new ApiError({
          kind: 'http',
          status: 400,
          title: 'Sign in again first',
          detail: 'Biometric unlock needs a fresh sign-in before it can be enabled.',
        });
      }
      const options = await api.webauthnRegisterOptions();
      const credential = await performRegistration(options.options);
      await api.webauthnRegister({
        challenge_id: options.challenge_id,
        credential,
        refresh_token: refreshToken,
      });
    },
    onSuccess: () => {
      rememberEnrolment(true);
      setMessage('Biometric unlock is on.');
      invalidate();
    },
    onError: (error: unknown) => {
      setMessage(
        error instanceof ApiError
          ? error.userMessage
          : 'That did not complete. Nothing was changed.',
      );
    },
  });

  const disable = useMutation({
    mutationFn: () => api.webauthnDisable(),
    onSuccess: () => {
      rememberEnrolment(false);
      setMessage('Biometric unlock is off.');
      invalidate();
    },
    onError: () => {
      setMessage('Could not turn it off. Nothing was changed.');
    },
  });

  if (!available) {
    return (
      <section className="settings-group" aria-labelledby={headingId}>
        <h2 id={headingId} className="settings-group__title">
          Biometric unlock
        </h2>
        <p className="settings-group__note">
          This device or browser does not support biometric unlock.
        </p>
      </section>
    );
  }

  const enrolled = status.data?.enrolled ?? false;
  const busy = enroll.isPending || disable.isPending;

  return (
    <section className="settings-group" aria-labelledby={headingId}>
      <h2 id={headingId} className="settings-group__title">
        Biometric unlock
      </h2>

      <button
        type="button"
        className="option"
        aria-pressed={enrolled}
        disabled={busy || status.isLoading}
        onClick={() => {
          setMessage(null);
          if (enrolled) setPendingDisable(true);
          else enroll.mutate();
        }}
      >
        <span className="option__label">
          {enrolled ? 'On — unlock with your face or fingerprint' : 'Off'}
        </span>
        <span className="option__hint">{busy ? 'Working…' : enrolled ? 'Turn off' : 'Turn on'}</span>
      </button>

      {message && (
        <p className="settings-group__note" role="status" aria-live="polite">
          {message}
        </p>
      )}

      <ConfirmDialog
        open={pendingDisable}
        title="Turn off biometric unlock?"
        body="The stored credential is destroyed. You will need to sign in with your password to turn it back on."
        confirmLabel="Turn it off"
        destructive
        onCancel={() => {
          setPendingDisable(false);
        }}
        onConfirm={() => {
          setPendingDisable(false);
          disable.mutate();
        }}
      />
    </section>
  );
}
