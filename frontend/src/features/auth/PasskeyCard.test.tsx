import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { routes } from '@/app/router.tsx';
import { TestProviders } from '@/test/providers.tsx';

import { PasskeyCard } from './PasskeyCard.tsx';
import { PasskeyNudge } from './PasskeyNudge.tsx';
import { PASSKEY_NUDGE_KEY } from './passkeys.ts';

/*
 * A configured build, as `gate.test.tsx` does: under Vitest no `VITE_*` is set,
 * and an unconfigured card correctly refuses to hand off anywhere.
 */
vi.mock('@/config/env.ts', () => ({
  config: {
    apiUrl: 'https://api.test',
    userPoolId: 'us-west-2_test',
    clientId: 'client-abc',
    cognitoDomain: 'https://cognito.test',
    instance: 'dev',
    version: 'test',
    appName: 'Chintan',
    appDescription: 'Speak a thought. It files itself.',
  },
  isConfigured: () => true,
  LOCAL_VERSION: 'local build',
}));

/*
 * jsdom has no WebAuthn; most of these cases are about a browser that does.
 *
 * Set and removed by hand rather than with `vi.stubGlobal`/`unstubAllGlobals`:
 * the latter also tears down the `Request` and `matchMedia` bridges the global
 * setup installs, and the next test to navigate then fails to construct a
 * Request.
 */
const webAuthn = window as unknown as Record<string, unknown>;

function withWebAuthn(): void {
  webAuthn['PublicKeyCredential'] = class PublicKeyCredential {};
}

function withoutWebAuthn(): void {
  delete webAuthn['PublicKeyCredential'];
}

beforeEach(() => {
  withWebAuthn();
});

afterEach(() => {
  withoutWebAuthn();
});

function mountCard(path = '/settings', navigate = vi.fn<(url: string) => void>()) {
  render(
    <TestProviders>
      <MemoryRouter initialEntries={[path]}>
        <PasskeyCard navigate={navigate} />
      </MemoryRouter>
    </TestProviders>,
  );
  return navigate;
}

describe('the passkey card on You', () => {
  it('hands off to the managed login’s passkey page, and says that is where it goes', async () => {
    /*
     * The ceremony cannot run in the app (the relying party is Cognito's
     * domain — see passkeys.ts), so the one control is a hand-off and the copy
     * says so rather than implying the app registers anything.
     */
    const navigate = mountCard();

    expect(screen.getByRole('heading', { name: 'Passkeys' })).toBeInTheDocument();
    expect(screen.getByText(/set up on cognito’s sign-in page, not here/i)).toBeInTheDocument();
    // The sign-in page's own button is named as what comes *after*, not as
    // what this card does.
    expect(screen.getByText(/offers/i)).toHaveTextContent(/sign in with a passkey/i);
    expect(screen.queryByRole('heading', { name: /sign in with a passkey/i })).toBeNull();

    await userEvent.click(screen.getByRole('button', { name: /add a passkey on this device/i }));

    expect(navigate).toHaveBeenCalledTimes(1);
    const url = new URL(navigate.mock.calls[0]?.[0] ?? '');
    expect(url.origin).toBe('https://cognito.test');
    expect(url.pathname).toBe('/passkeys/add');
    expect(url.searchParams.get('client_id')).toBe('client-abc');
    expect(url.searchParams.get('redirect_uri')).toBe(`${window.location.origin}/`);
  });

  it('settles the library nudge when the hand-off starts', async () => {
    mountCard();
    await userEvent.click(screen.getByRole('button', { name: /add a passkey on this device/i }));
    expect(window.localStorage.getItem(PASSKEY_NUDGE_KEY)).toBe('added');
  });

  it('reports a passkey that was added, and the news can be dismissed', async () => {
    mountCard('/settings?passkey=success');

    expect(screen.getByRole('status')).toHaveTextContent(/passkey added/i);

    await userEvent.click(screen.getByRole('button', { name: 'OK' }));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('explains an ended managed-login session and offers to sign in again', async () => {
    /*
     * `/passkeys/add` needs the managed login's own session cookie, which a
     * user who has been refreshing tokens for days no longer has. Cognito then
     * bounces straight back with `result=invalid_session` and no passkey. The
     * fix is a fresh sign-in, so the card offers exactly that.
     */
    const navigate = mountCard('/settings?passkey=invalid_session');

    expect(screen.getByRole('alert')).toHaveTextContent(/no longer had your session/i);

    await userEvent.click(screen.getByRole('button', { name: /sign in again/i }));

    await waitFor(() => {
      expect(navigate).toHaveBeenCalledTimes(1);
    });
    const url = new URL(navigate.mock.calls[0]?.[0] ?? '');
    expect(url.pathname).toBe('/oauth2/authorize');
    expect(url.searchParams.get('client_id')).toBe('client-abc');
  });

  it('tells a browser without WebAuthn that it cannot take part, instead of a button', () => {
    withoutWebAuthn();
    mountCard();

    expect(screen.getByRole('note')).toHaveTextContent(/cannot create passkeys/i);
    expect(screen.queryByRole('button', { name: /add a passkey/i })).toBeNull();
  });
});

describe('the library nudge', () => {
  function mountNudge(navigate = vi.fn<(url: string) => void>()) {
    render(<PasskeyNudge navigate={navigate} />);
    return navigate;
  }

  it('offers the hand-off once, and Not now is remembered on this device', async () => {
    mountNudge();
    expect(screen.getByText(/sign in faster next time/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /not now/i }));

    expect(screen.queryByText(/sign in faster next time/i)).toBeNull();
    expect(window.localStorage.getItem(PASSKEY_NUDGE_KEY)).toBe('not-now');

    // A remount reads the answer back rather than asking again.
    mountNudge();
    expect(screen.queryByText(/sign in faster next time/i)).toBeNull();
  });

  it('Set up goes to the same page the card does', async () => {
    const navigate = mountNudge();
    await userEvent.click(screen.getByRole('button', { name: /set up/i }));
    expect(navigate.mock.calls[0]?.[0]).toContain('/passkeys/add?');
  });

  it('is not shown where it could not work', () => {
    withoutWebAuthn();
    mountNudge();
    expect(screen.queryByText(/sign in faster next time/i)).toBeNull();
  });
});

describe('coming back from the managed login', () => {
  it('lands on You with the outcome, with the library beneath it, and settles the nudge', async () => {
    /*
     * `/passkeys/add` can only return to the registered callback — the app's
     * base URL, i.e. the library — with `?result=…`. The person left from You,
     * so the shell takes them back there and moves the answer into `?passkey=`.
     */
    const router = createMemoryRouter(routes, { initialEntries: ['/?result=success'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings');
    });
    expect(router.state.location.search).toBe('?passkey=success');
    expect(await screen.findByText(/passkey added/i)).toBeInTheDocument();
    expect(window.localStorage.getItem(PASSKEY_NUDGE_KEY)).toBe('added');

    // Back is the library, not out of the app.
    await router.navigate(-1);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/');
    });
    expect(router.state.location.search).toBe('');
  });
});
