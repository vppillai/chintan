import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter, type RouteObject } from 'react-router';
import { describe, expect, it } from 'vitest';

import { TestProviders } from '@/test/providers.tsx';

import { routes } from './router.tsx';

type Router = ReturnType<typeof createMemoryRouter>;

function mount(initialEntries: string[] = ['/']) {
  const router = createMemoryRouter(routes, { initialEntries });
  const view = render(
    <TestProviders>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { router, view };
}

/** Browser / Android Back, flushed so the shell has re-rendered. */
async function goBack(router: Router): Promise<void> {
  await act(async () => {
    await router.navigate(-1);
  });
}

/** Lets the back guard's history seed, or a redirect, settle. */
async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
  });
}

const path = (router: Router) => router.state.location.pathname;

const shell = () => document.querySelector('.app');

describe('the shell renders one landmark set', () => {
  it('has a skip link, a banner, one main and one navigation', () => {
    mount();
    expect(screen.getByRole('link', { name: /skip to content/i })).toHaveAttribute(
      'href',
      '#main',
    );
    // The shell's own banner. A screen's `<header>` inside <main> is not one
    // in the browser's accessibility tree, whatever jsdom thinks.
    expect(screen.getByText('Chintan').closest('header')).toHaveClass('app__banner');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    expect(screen.getAllByRole('navigation')).toHaveLength(1);
  });

  it('exposes a polite live region for route announcements', () => {
    mount();
    const region = screen.getByTestId('status-region');
    expect(region).toHaveAttribute('aria-live', 'polite');
    expect(region).toHaveAttribute('role', 'status');
    expect(region).toHaveTextContent('Notes screen');
  });
});

describe('notes first', () => {
  it('lands on the library, with the tab bar beneath it', async () => {
    mount();
    expect(shell()).toHaveAttribute('data-screen', 'library');
    expect(screen.getByRole('heading', { name: 'Notes' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Notes' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'You' })).not.toHaveAttribute('aria-current');
  });

  it('seats the record button in the tab bar, not floating over content', () => {
    mount();
    const record = screen.getByRole('button', { name: /record/i });
    // A floating action button would be a sibling of <main> and would overlay
    // the last note row.
    expect(record.closest('.tab-bar')).not.toBeNull();
  });

  it('keeps the Notes tab lit while reading a note', async () => {
    const user = userEvent.setup();
    const { router } = mount();
    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    expect(path(router)).toBe('/notes/roof-repair');
    expect(shell()).toHaveAttribute('data-screen', 'note');
    expect(screen.getByRole('link', { name: 'Notes' })).toHaveAttribute('aria-current', 'page');
  });

  it('lights the You tab on settings', async () => {
    const user = userEvent.setup();
    const { router } = mount();
    await user.click(screen.getByRole('link', { name: 'You' }));
    expect(path(router)).toBe('/settings');
    expect(screen.getByRole('link', { name: 'You' })).toHaveAttribute('aria-current', 'page');
  });
});

describe('the capture screen is full screen', () => {
  it('drops the tab bar while recording', async () => {
    const user = userEvent.setup();
    const { router } = mount();

    await user.click(screen.getByRole('button', { name: /record/i }));

    expect(path(router)).toBe('/capture');
    expect(shell()).toHaveAttribute('data-screen', 'capture');
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
  });
});

describe('old URLs still land somewhere', () => {
  it('sends /notes to the library', async () => {
    const { router } = mount(['/notes']);
    await settle();
    await waitFor(() => {
      expect(path(router)).toBe('/');
    });
    expect(screen.getByRole('heading', { name: 'Notes' })).toBeInTheDocument();
  });
});

describe('Back always means back', () => {
  it('pops the note detail screen back to the library', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/']);

    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    expect(path(router)).toBe('/notes/roof-repair');

    await goBack(router);

    expect(path(router)).toBe('/');
    expect(shell()).toHaveAttribute('data-screen', 'library');
  });

  it('seeds the library beneath a cold-start deep link, where v1 would have exited', async () => {
    // Entering directly at a note gives the app one history entry, so Back
    // would leave the tab. useBackGuard seeds home beneath it.
    const { router } = mount(['/notes/roof-repair']);

    // The seed replaces the initial entry with home and pushes the note back
    // on top, so the deep link is still what is rendered.
    await settle();
    expect(router.state.location.key).not.toBe('default');
    expect(path(router)).toBe('/notes/roof-repair');

    await goBack(router);

    expect(path(router)).toBe('/');
    expect(screen.getByRole('heading', { name: 'Notes' })).toBeInTheDocument();
  });

  it('leaves the capture screen back to the library', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/']);

    await user.click(screen.getByRole('button', { name: /record/i }));
    expect(path(router)).toBe('/capture');

    await goBack(router);

    expect(path(router)).toBe('/');
    expect(shell()).toHaveAttribute('data-screen', 'library');
  });

  it('does not seed anything at the library itself', async () => {
    const { router } = mount(['/']);
    await settle();
    expect(router.state.location.key).toBe('default');
  });
});

describe('accessibility of the library', () => {
  it('renders note rows as real buttons, not clickable divs', async () => {
    mount();
    const row = await screen.findByRole('button', { name: /roof repair/i });
    expect(row.tagName).toBe('BUTTON');
    expect(row).toHaveAttribute('type', 'button');
  });

  it('moves focus to the routed region on navigation', async () => {
    const user = userEvent.setup();
    mount();

    await user.click(screen.getByRole('link', { name: 'You' }));

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole('main'));
    });
  });
});

describe('a render fault never leaves the user with no controls', () => {
  function Boom(): never {
    throw new Error('the cache held the wrong shape');
  }

  function crashingRoutes(): RouteObject[] {
    // The real route config, with the index screen swapped for a thrower.
    // Everything else — including whatever error handling the config declares,
    // which is the thing under test — is the shipped one.
    const root = routes[0];
    const children = (root?.children ?? []).map((child) =>
      'index' in child && child.index ? { ...child, Component: Boom } : child,
    );
    return [{ ...root, children }] as RouteObject[];
  }

  it('renders a screen with a way out instead of the raw error page', async () => {
    // Counted on the real failure: 0 links and 0 buttons survived, and there is
    // no application error boundary anywhere. On a phone the only escape was OS
    // Back — which returns to the screen that caused it — or knowing to reload.
    const router = createMemoryRouter(crashingRoutes(), { initialEntries: ['/'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    await screen.findByRole('alert');
    expect(screen.getAllByRole('button').length + screen.getAllByRole('link').length)
      .toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: /back to your notes/i })).toBeInTheDocument();
    // The message is a sentence, not a stack trace.
    expect(screen.queryByText(/at Object\.|\.tsx:\d/)).toBeNull();
  });
});
