import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { openFromNotification, pathToOpen } from './notificationOpen.ts';
import { usePushWakeup } from './usePushWakeup.ts';

const URL_OF_NOTE = 'https://app.example/chintan/dev/notes/roof-repair';

/** A window as the worker sees it; `answers` is whether the app in it routes the open itself. */
function fakeWindow(answers: boolean) {
  return {
    focus: vi.fn(() => Promise.resolve()),
    navigate: vi.fn(() => Promise.resolve()),
    postMessage: vi.fn((_message: unknown, transfer: Transferable[]) => {
      if (answers) (transfer[0] as MessagePort).postMessage({ type: 'OPENED' });
    }),
  };
}

function fakeClients(windows: ReturnType<typeof fakeWindow>[]) {
  return { matchAll: vi.fn(() => Promise.resolve(windows)), openWindow: vi.fn(() => Promise.resolve()) };
}

describe('a tapped notification, in the worker', () => {
  it('asks the open window to route there and does not navigate the document when it answers', async () => {
    const open = fakeWindow(true);
    const clients = fakeClients([open]);

    await openFromNotification(clients, URL_OF_NOTE, 50);

    expect(open.focus).toHaveBeenCalled();
    expect(open.postMessage.mock.calls[0]?.[0]).toEqual({ type: 'OPEN', url: URL_OF_NOTE });
    // The reload that ended a recording in that window.
    expect(open.navigate).not.toHaveBeenCalled();
    expect(clients.openWindow).not.toHaveBeenCalled();
  });

  it('navigates a window that does not answer', async () => {
    const open = fakeWindow(false);
    const clients = fakeClients([open]);

    await openFromNotification(clients, URL_OF_NOTE, 10);

    expect(open.navigate).toHaveBeenCalledWith(URL_OF_NOTE);
    expect(clients.openWindow).not.toHaveBeenCalled();
  });

  it('opens a new window when there is none, or when the one found cannot be navigated', async () => {
    const none = fakeClients([]);
    await openFromNotification(none, URL_OF_NOTE, 10);
    expect(none.openWindow).toHaveBeenCalledWith(URL_OF_NOTE);

    const uncontrolled = fakeWindow(false);
    uncontrolled.navigate.mockRejectedValue(new TypeError('not controlled'));
    const clients = fakeClients([uncontrolled]);
    await openFromNotification(clients, URL_OF_NOTE, 10);
    expect(clients.openWindow).toHaveBeenCalledWith(URL_OF_NOTE);
  });
});

describe('the path an OPEN message asks for', () => {
  const origin = 'https://app.example';

  it('is the URL under the app’s base, as the router spells it', () => {
    expect(pathToOpen({ type: 'OPEN', url: URL_OF_NOTE }, '/chintan/dev/', origin)).toBe('/notes/roof-repair');
    expect(pathToOpen({ type: 'OPEN', url: `${origin}/chintan/dev/` }, '/chintan/dev/', origin)).toBe('/');
    expect(pathToOpen({ type: 'OPEN', url: `${origin}/notes/a?tab=recordings` }, '/', origin)).toBe(
      '/notes/a?tab=recordings',
    );
  });

  it('is nothing for another deploy on the same origin, another origin, or another message', () => {
    expect(pathToOpen({ type: 'OPEN', url: `${origin}/chintan/prod/notes/a` }, '/chintan/dev/', origin)).toBeNull();
    expect(pathToOpen({ type: 'OPEN', url: 'https://elsewhere.example/notes/a' }, '/', origin)).toBeNull();
    expect(pathToOpen({ type: 'OPEN', url: 'not a url' }, '/', origin)).toBeNull();
    expect(pathToOpen({ type: 'CAPTURES_CHANGED', note_id: 'a' }, '/', origin)).toBeNull();
    expect(pathToOpen(null, '/', origin)).toBeNull();
  });
});

describe('a tapped notification, in the window', () => {
  afterEach(() => {
    Reflect.deleteProperty(navigator, 'serviceWorker');
  });

  function Probe() {
    usePushWakeup();
    return <p>{useLocation().pathname}</p>;
  }

  it('routes to the note in the app and answers the worker', async () => {
    const container = new EventTarget();
    Object.defineProperty(navigator, 'serviceWorker', { value: container, configurable: true });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={['/capture']}>
          <Probe />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const answer = vi.fn();
    const message = Object.assign(new Event('message'), {
      data: { type: 'OPEN', url: `${window.location.origin}/notes/roof-repair` },
      ports: [{ postMessage: answer }],
    });

    act(() => {
      container.dispatchEvent(message);
    });

    expect(await screen.findByText('/notes/roof-repair')).toBeInTheDocument();
    expect(answer).toHaveBeenCalledWith({ type: 'OPENED' });
  });
});
