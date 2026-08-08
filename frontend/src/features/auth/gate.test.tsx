import { render, screen, waitFor } from '@testing-library/react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { routes } from '@/app/router.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { SignedOutScreen } from './SignedOutScreen.tsx';
import { confirmBody } from './SignOutSetting.tsx';

/*
 * A configured build. Under Vitest no `VITE_*` variables are set, so
 * `config.required()` yields empty strings and the gate correctly renders "this
 * build has no sign-in configured" instead of a button — which is the right
 * behaviour and the wrong thing to be testing here.
 */
vi.mock('@/config/env.ts', () => ({
  config: {
    apiUrl: 'https://api.test',
    userPoolId: 'us-west-2_test',
    clientId: 'client-abc',
    cognitoDomain: 'https://cognito.test',
    instance: 'dev',
  },
  isConfigured: () => true,
}));

/** Mounts the real shell with no token, as a signed-out visitor has. */
function mountSignedOut(initialEntries: string[] = ['/']) {
  const fetchImpl = vi.fn<typeof fetch>(
    async () =>
      new Response(JSON.stringify({ items: [] }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
  );

  const router = createMemoryRouter(routes, { initialEntries });
  render(
    <TestProviders api={testApiContext(fetchImpl, null)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { fetchImpl, router };
}

describe('a signed-out visitor gets a sign-in, not a shell of 401s', () => {
  it('offers a way in', async () => {
    mountSignedOut();
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('mounts none of the authenticated surfaces', async () => {
    /*
     * Every screen's first act is an authenticated query, and the shell runs
     * two more of its own — the progress card polls
     * `GET /v1/captures?status=pending` and the resume prompt offers a Send.
     * Rendering any of it without a token is what produced a console of 401s
     * with nothing on screen explaining itself.
     */
    const { fetchImpl } = mountSignedOut();
    await screen.findByRole('button', { name: 'Sign in' });

    expect(screen.queryByRole('button', { name: /record/i })).toBeNull();
    expect(screen.queryByRole('navigation', { name: 'Library' })).toBeNull();
    expect(screen.queryByRole('region', { name: /captures in progress/i })).toBeNull();

    await waitFor(() => {
      expect(fetchImpl, 'the signed-out shell called the API').not.toHaveBeenCalled();
    });
  });

  it('gates a deep link too, not just home', async () => {
    mountSignedOut(['/notes']);
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Notes' })).toBeNull();
  });

  it('keeps exactly one main landmark', async () => {
    mountSignedOut();
    await screen.findByRole('button', { name: 'Sign in' });
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('says the audio on this device is not lost', async () => {
    // The signed-out screen is where someone with an unsent recording lands
    // after a session expires. Saying nothing would read as "it is gone".
    mountSignedOut();
    expect(await screen.findByText(/still here and will be offered back/i)).toBeInTheDocument();
  });
});

describe('an unconfigured build says so rather than offering a dead button', () => {
  it('names the missing build-time variables', () => {
    // `config.required()` returns an empty string for a missing variable and
    // only warns under DEV, so a pipeline that forgot to export them produces a
    // bundle that compiles and cannot sign anyone in. `check-vite-env.sh`
    // guards the contract; this is what the user sees if it ever slips.
    render(
      <SignedOutScreen
        phase="signed-out"
        error={null}
        signIn={vi.fn()}
        unlock={vi.fn()}
        needsReEnrolment={false}
        configured={false}
      />,
    );

    expect(screen.getByRole('alert')).toHaveTextContent(/no sign-in configured/i);
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull();
    // Neither way in works without the configuration, and offering the shortcut
    // alone would be a button that reaches an API this bundle has no URL for.
    expect(screen.queryByRole('button', { name: /unlock/i })).toBeNull();
  });
});

describe('the signed-out screen offers the unlock beside the sign-in', () => {
  it('renders it when the browser can assert a credential', () => {
    render(
      <SignedOutScreen
        phase="signed-out"
        error={null}
        signIn={vi.fn()}
        unlock={vi.fn()}
        needsReEnrolment={false}
        configured
      />,
    );

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Unlock with biometrics' }),
    ).toBeInTheDocument();
  });

  it('omits it when the browser cannot', () => {
    render(
      <SignedOutScreen
        phase="signed-out"
        error={null}
        signIn={vi.fn()}
        unlock={null}
        needsReEnrolment={false}
        configured
      />,
    );

    expect(screen.queryByRole('button', { name: /unlock/i })).toBeNull();
  });

  it('asks for re-enrolment instead of reporting a failure', () => {
    // The assertion verified; the vault behind it was sealed by a retired key
    // and has been discarded. Nothing is wrong with the user's finger, so
    // "biometric verification failed" would send them to try it again forever.
    render(
      <SignedOutScreen
        phase="signed-out"
        error={null}
        signIn={vi.fn()}
        unlock={vi.fn()}
        needsReEnrolment
        configured
      />,
    );

    expect(screen.getByRole('status')).toHaveTextContent(/set up again on this device/i);
    expect(screen.queryByRole('alert')).toBeNull();
    // And the way out of it is still on screen.
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });
});

describe('the sign-out confirmation names what it destroys', () => {
  it('warns about a recording that never reached the server', () => {
    const body = confirmBody({ captures: 1, queued: 0 }, false);
    expect(body).toMatch(/one recording that has not reached the server/i);
    expect(body).toMatch(/not saved anywhere else/i);
  });

  it('counts more than one', () => {
    expect(confirmBody({ captures: 3, queued: 0 }, false)).toMatch(/3 recordings/);
  });

  it('warns about queued changes', () => {
    expect(confirmBody({ captures: 0, queued: 2 }, false)).toMatch(/2 unsynced changes/);
  });

  it('is reassuring when there is genuinely nothing to lose', () => {
    const body = confirmBody({ captures: 0, queued: 0 }, false);
    expect(body).toMatch(/notes stay on the server/i);
    expect(body).not.toMatch(/deletes/i);
  });

  it('says biometric unlock is going too, because that is irreversible', () => {
    expect(confirmBody({ captures: 0, queued: 0 }, true)).toMatch(
      /biometric unlock will also be turned off/i,
    );
  });

  it('says nothing about biometrics when nothing is enrolled', () => {
    expect(confirmBody({ captures: 0, queued: 0 }, false)).not.toMatch(/biometric/i);
  });
});
