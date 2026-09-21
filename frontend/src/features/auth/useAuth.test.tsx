import { render, screen } from '@testing-library/react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { routes } from '@/app/router.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { PENDING_AUTH_KEY, rememberPending } from './pending.ts';
import { describeAuthError } from './useAuth.ts';

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

afterEach(() => {
  window.history.replaceState({}, '', '/');
  localStorage.clear();
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
