import { act, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { UpdatePrompt } from './UpdatePrompt.tsx';

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
});

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
});
