import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';

import { routes } from '@/app/router.tsx';
import { INITIAL_CAPTURE, type CaptureState } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { TestProviders } from '@/test/providers.tsx';

function mount(initialEntries: string[] = ['/']) {
  const router = createMemoryRouter(routes, { initialEntries });
  render(
    <TestProviders>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return router;
}

function setCaptureState(state: CaptureState): void {
  act(() => {
    useCaptureStore.setState({
      model: { ...INITIAL_CAPTURE, state, localId: 'cap-live', elapsedMs: 7_000, bytes: 4_096 },
    });
  });
}

afterEach(() => {
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

describe('the microphone being open is never invisible', () => {
  it('says so on the library after backing out of the capture screen', async () => {
    /*
     * Press system Back while recording — the natural one-handed dismissal, and
     * something a pocket or a steering wheel does by accident — and you landed
     * on the home screen showing the ordinary Record button and nothing else.
     * The recorder kept running, kept writing chunks, kept the wake lock, and
     * kept counting toward the twenty-minute cap, with no indicator anywhere.
     * `isCaptureBusy` existed, was documented, and had no call sites.
     */
    const router = mount(['/']);
    setCaptureState('recording');

    const indicator = await screen.findByRole('button', { name: /recording — tap to return/i });

    await userEvent.setup().click(indicator);
    expect(router.state.location.pathname).toBe('/capture');
  });

  it('shows on the You screen too, not just the library', async () => {
    mount(['/settings']);
    setCaptureState('recording');
    expect(await screen.findByRole('button', { name: /tap to return/i })).toBeInTheDocument();
  });

  it('covers every busy state, not just a live microphone', async () => {
    mount(['/settings']);
    for (const state of ['requesting', 'paused', 'stopping', 'uploading'] as CaptureState[]) {
      setCaptureState(state);
      expect(
        await screen.findByRole('button', { name: /tap to return/i }),
        `no indicator while ${state}`,
      ).toBeInTheDocument();
    }
  });

  it('stands aside on the library while uploading, where the filing row already says so', async () => {
    // Send hands off to the library at once and the upload's own row shows
    // "Uploading… N%" at the top of it; a second line saying the same thing
    // above the tab bar would be noise. Every other screen still gets it.
    mount(['/']);
    setCaptureState('uploading');
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.queryByRole('button', { name: /tap to return/i })).toBeNull();
  });

  it('is absent when nothing is being captured', () => {
    mount(['/']);
    expect(screen.queryByRole('button', { name: /tap to return/i })).toBeNull();
  });

  it('is absent on the capture screen, which is its own indicator', async () => {
    const router = mount(['/capture']);
    // The back guard seeds home under the deep link; wait for it to settle.
    await act(async () => {
      await Promise.resolve();
    });
    expect(router.state.location.pathname).toBe('/capture');

    setCaptureState('recording');
    expect(screen.queryByRole('button', { name: /tap to return/i })).toBeNull();
  });
});
