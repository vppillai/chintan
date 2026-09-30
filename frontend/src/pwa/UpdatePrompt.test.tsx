import { act, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { UPDATE_CHECK_INTERVAL_MS, UpdatePrompt } from './UpdatePrompt.tsx';

/*
 * jsdom has no service worker. Enough of the registration for the prompt's
 * one job: notice a new worker reaching `installed` and offer the update.
 */
class FakeWorker extends EventTarget {
  state: ServiceWorkerState = 'installing';
  postMessage(): void {}
  installed(): void {
    this.state = 'installed';
    this.dispatchEvent(new Event('statechange'));
  }
}

class FakeRegistration extends EventTarget {
  waiting: FakeWorker | null = null;
  installing: FakeWorker | null = null;
  update = vi.fn(() => Promise.resolve());
}

function withServiceWorker(registration: FakeRegistration): void {
  const container = Object.assign(new EventTarget(), {
    ready: Promise.resolve(registration),
    // An existing controller: this is an update, not the first install.
    controller: {},
  });
  Object.defineProperty(navigator, 'serviceWorker', { value: container, configurable: true });
}

afterEach(() => {
  Reflect.deleteProperty(navigator, 'serviceWorker');
  Reflect.deleteProperty(document, 'visibilityState');
  vi.useRealTimers();
});

function becomeVisible(visible = true): void {
  Object.defineProperty(document, 'visibilityState', {
    value: visible ? 'visible' : 'hidden',
    configurable: true,
  });
  document.dispatchEvent(new Event('visibilitychange'));
}

/** Lets `navigator.serviceWorker.ready` resolve inside the effect. */
const settled = () =>
  act(async () => {
    await Promise.resolve();
  });

describe('the update prompt', () => {
  it('offers a worker that is already waiting', async () => {
    const registration = new FakeRegistration();
    registration.waiting = new FakeWorker();
    withServiceWorker(registration);

    render(<UpdatePrompt />);
    await settled();

    expect(screen.getByRole('status')).toHaveTextContent(/a new version of .+ is ready/i);
    expect(screen.getByRole('button', { name: 'Update' })).toBeInTheDocument();
  });

  it('offers a worker whose install began before the prompt mounted', async () => {
    /*
     * The browser checks for an update on its own as the page navigates, so
     * by the time `ready` resolves the new worker can already be
     * `registration.installing` with its `updatefound` long gone. Listening
     * for the event alone missed it, and the prompt waited for the next check.
     */
    const registration = new FakeRegistration();
    const installing = new FakeWorker();
    registration.installing = installing;
    withServiceWorker(registration);

    render(<UpdatePrompt />);
    await settled();
    expect(screen.queryByRole('status')).toBeNull();

    act(() => {
      installing.installed();
    });
    expect(screen.getByRole('status')).toHaveTextContent(/is ready/i);
  });

  it('offers a worker found after the prompt mounted', async () => {
    const registration = new FakeRegistration();
    withServiceWorker(registration);

    render(<UpdatePrompt />);
    await settled();

    const installing = new FakeWorker();
    act(() => {
      registration.installing = installing;
      registration.dispatchEvent(new Event('updatefound'));
    });
    expect(screen.queryByRole('status')).toBeNull();
    act(() => {
      installing.installed();
    });
    expect(screen.getByRole('status')).toHaveTextContent(/is ready/i);
  });

  it('asks for a new build when the app comes back to the foreground, at most every half hour', async () => {
    /*
     * An installed app resumed from the background makes no navigation, so
     * the browser never re-checks the worker on its own: a PWA left open
     * kept the old build through every deploy.
     */
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const registration = new FakeRegistration();
    withServiceWorker(registration);

    render(<UpdatePrompt />);
    await settled();

    // Straight after the load, which was itself a check: nothing to ask.
    becomeVisible();
    expect(registration.update).not.toHaveBeenCalled();

    vi.advanceTimersByTime(UPDATE_CHECK_INTERVAL_MS);
    becomeVisible(false);
    expect(registration.update).not.toHaveBeenCalled();
    becomeVisible();
    expect(registration.update).toHaveBeenCalledTimes(1);

    // Flicked away and back within the half hour: still one.
    becomeVisible(false);
    becomeVisible();
    expect(registration.update).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(UPDATE_CHECK_INTERVAL_MS);
    becomeVisible();
    expect(registration.update).toHaveBeenCalledTimes(2);
  });

  it('stops listening once unmounted', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const registration = new FakeRegistration();
    withServiceWorker(registration);

    const { unmount } = render(<UpdatePrompt />);
    await settled();
    unmount();

    vi.advanceTimersByTime(UPDATE_CHECK_INTERVAL_MS);
    becomeVisible();
    expect(registration.update).not.toHaveBeenCalled();
  });
});
