import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { routes } from '@/app/router.tsx';
import { TestProviders, defaultTestFetch, testApiContext } from '@/test/providers.tsx';

import { redirectUri } from './oauth.ts';
import { PENDING_AUTH_KEY, rememberPending, type PendingAuth } from './pending.ts';
import { challengeFor } from './pkce.ts';
import { beginSignIn, describeAuthError } from './useAuth.ts';

/*
 * A configured build, as in gate.test.tsx: without it the gate renders "no
 * sign-in configured" and never reaches the branch under test.
 */
vi.mock('@/config/env.ts', () => ({
  config: {
    apiUrl: 'https://api.test',
    userPoolId: 'us-west-2_test',
    clientId: 'client-abc',
    cognitoDomain: 'https://cognito.test',
    instance: 'dev',
    appName: 'Chintan',
    appDescription: 'Speak a thought. It files itself.',
  },
  isConfigured: () => true,
}));

/**
 * The gate reads the real `window.location`, not the memory router — Cognito
 * redirects to the registered base URL, and that is where the parameters land.
 */
function landWith(search: string): void {
  window.history.replaceState({}, '', `/${search}`);
  const router = createMemoryRouter(routes, { initialEntries: ['/'] });
  render(
    <TestProviders api={testApiContext(undefined, null)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
}

function startedAFlow(): void {
  rememberPending({ state: 'st-1', verifier: 'v', returnTo: '/', startedAt: Date.now() });
}

/**
 * Cognito's token endpoint, answering as the test says. `exchangeCode` reads
 * the global `fetch`; the app's own API keeps the providers' stub, so the shell
 * that mounts after a sign-in has its notes to render.
 */
function tokenEndpoint(answer: () => Response) {
  const api = defaultTestFetch();
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) =>
    String(input) === 'https://cognito.test/oauth2/token' ? answer() : api(input, init),
  );
  vi.stubGlobal('fetch', fetchImpl);
  return fetchImpl;
}

afterEach(() => {
  window.history.replaceState({}, '', '/');
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe('what the sign-in screen says when Cognito answers with an error', () => {
  it('says the fixed sentence for the code and never the description on the URL', async () => {
    // `error_description` is a string anyone can put on a link. Rendered as
    // the alert it read, in the app's voice, as a reason to call a number.
    startedAFlow();
    landWith(
      '?error=access_denied&error_description=Your+account+is+locked.+Call+555-0100+to+unlock+it.&state=st-1',
    );

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('That sign-in was cancelled.');
    expect(alert).not.toHaveTextContent(/555|locked/);
  });

  it('reports nothing when no flow from this device is waiting for an answer', async () => {
    // The hosted UI redirects here only in reply to a request from here. The
    // same parameters on a link someone was sent are not an outcome.
    landWith('?error=server_error&error_description=Sign+in+at+chintan-support.example+instead.');

    await screen.findByRole('button', { name: 'Sign in' });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(window.location.search).toBe('');
  });

  it('ends the flow it answers and takes the parameters off the address bar', async () => {
    startedAFlow();
    landWith('?error=access_denied&state=st-1');

    await screen.findByRole('alert');
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
    expect(window.location.search).toBe('');
  });

  it('leaves another flow’s answer alone: no alert, and that flow still pending', async () => {
    // The pending entry is in `localStorage`, shared across tabs. A composed
    // link opened while a genuine sign-in waits in another tab must neither
    // report a refusal here nor end that tab's flow.
    startedAFlow();
    landWith('?error=access_denied&state=not-this-device');

    await screen.findByRole('button', { name: 'Sign in' });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(localStorage.getItem(PENDING_AUTH_KEY)).not.toBeNull();
    expect(window.location.search).toBe('');
  });
});

describe('the sentence for each code', () => {
  const cases: [code: string, expected: RegExp][] = [
    ['access_denied', /cancelled/],
    ['server_error', /not available right now/],
    ['temporarily_unavailable', /not available right now/],
    ['invalid_request', /could not be completed/],
    ['unauthorized_client', /could not be completed/],
    ['<script>alert(1)</script>', /could not be completed/],
  ];
  for (const [code, expected] of cases) {
    it(`${code} → ${String(expected)}`, () => {
      expect(describeAuthError(code)).toMatch(expected);
    });
  }
});

/*
 * The code half of the callback. Cognito sends `?code=&state=` to the base
 * URL; the gate redeems it once, against the verifier this device remembered,
 * and only when the state is the one this device sent.
 */
describe('redeeming the code Cognito sends back', () => {
  it('signs in when the exchange succeeds, and never flashes the signed-out screen first', async () => {
    startedAFlow();
    const token = tokenEndpoint(
      () =>
        new Response(
          JSON.stringify({
            id_token: 'id',
            access_token: 'access',
            refresh_token: 'refresh',
            expires_in: 3600,
            token_type: 'Bearer',
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        ),
    );
    landWith('?code=abc&state=st-1');

    // Read before the first paint: the button is already the exchange.
    expect(screen.getByRole('button', { name: 'Signing you in…' })).toBeDisabled();

    expect(await screen.findByRole('navigation', { name: 'Main' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /sign in/i })).toBeNull();
    // One exchange, with the code and the verifier the flow started with.
    const exchanges = token.mock.calls.filter(([input]) => String(input).endsWith('/oauth2/token'));
    expect(exchanges).toHaveLength(1);
    const body = exchanges[0]?.[1]?.body as URLSearchParams;
    expect(body.get('grant_type')).toBe('authorization_code');
    expect(body.get('code')).toBe('abc');
    expect(body.get('code_verifier')).toBe('v');
    expect(body.get('redirect_uri')).toBe(redirectUri());
    // Spent: the verifier is gone from storage and the code from the address bar.
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
    expect(window.location.search).toBe('');
  });

  it('refuses a code whose state is not the flow this device started', async () => {
    // The state check is the CSRF boundary: a code delivered with someone
    // else's state did not come from a flow this tab began.
    startedAFlow();
    const token = tokenEndpoint(() => new Response(null, { status: 500 }));
    landWith('?code=abc&state=someone-elses');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('That sign-in did not match this device. Please try again.');
    expect(token.mock.calls.some(([input]) => String(input).endsWith('/oauth2/token'))).toBe(false);
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
    expect(window.location.search).toBe('');
  });

  it('says so when no flow is waiting for the code', async () => {
    const token = tokenEndpoint(() => new Response(null, { status: 500 }));
    landWith('?code=abc&state=st-1');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('That sign-in could not be completed. Please try again.');
    expect(token.mock.calls.some(([input]) => String(input).endsWith('/oauth2/token'))).toBe(false);
    expect(window.location.search).toBe('');
  });

  it('reports a refused exchange in the app’s words and offers the button again', async () => {
    startedAFlow();
    tokenEndpoint(
      () =>
        new Response(JSON.stringify({ error: 'invalid_grant' }), {
          status: 400,
          headers: { 'content-type': 'application/json' },
        }),
    );
    landWith('?code=abc&state=st-1');

    const alert = await screen.findByRole('alert');
    // `exchangeCode`'s own sentence for a refusal, through `ApiError.userMessage`.
    expect(alert).toHaveTextContent('Try signing in again.');
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
    expect(window.location.search).toBe('');
  });
});

describe('starting a sign-in', () => {
  it('sends the browser to the hosted UI with a PKCE challenge and remembers the flow', async () => {
    window.history.replaceState({}, '', '/notes/roof-repair?tab=recordings');
    const navigate = vi.fn<(url: string) => void>();

    await beginSignIn(navigate);

    const url = new URL(navigate.mock.calls[0]?.[0] ?? '');
    const pending = JSON.parse(localStorage.getItem(PENDING_AUTH_KEY) ?? 'null') as PendingAuth;
    expect(`${url.origin}${url.pathname}`).toBe('https://cognito.test/oauth2/authorize');
    expect(url.searchParams.get('response_type')).toBe('code');
    expect(url.searchParams.get('client_id')).toBe('client-abc');
    expect(url.searchParams.get('redirect_uri')).toBe(redirectUri());
    expect(url.searchParams.get('scope')).toBe('openid email profile');
    expect(url.searchParams.get('state')).toBe(pending.state);
    expect(url.searchParams.get('code_challenge_method')).toBe('S256');
    expect(url.searchParams.get('code_challenge')).toBe(await challengeFor(pending.verifier));
    // The verifier is the secret the challenge stands for; it never travels.
    expect(url.href).not.toContain(pending.verifier);
    // Where the user was, so signing in does not also lose their place.
    expect(pending.returnTo).toBe('/notes/roof-repair?tab=recordings');
  });

  it('says so when the browser cannot do the crypto, and offers the button again', async () => {
    const user = userEvent.setup();
    vi.spyOn(crypto.subtle, 'digest').mockRejectedValue(new Error('no SubtleCrypto'));
    landWith('');

    await user.click(await screen.findByRole('button', { name: 'Sign in' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This browser could not start a secure sign-in.',
    );
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
  });
});
