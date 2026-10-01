import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  RouterProvider,
  createBrowserRouter,
  createMemoryRouter,
  type RouteObject,
} from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';

import { screenForPath } from '@/components/AppShell.tsx';
import { TestProviders } from '@/test/providers.tsx';

import { routes } from './router.tsx';
import { historyIndex } from './useTabNavigation.ts';

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
const url = (router: Router) => `${path(router)}${router.state.location.search}`;

const shell = () => document.querySelector('.app');

describe('screenForPath', () => {
  it('names every screen the shell lays out, and only the rest "other"', () => {
    expect(screenForPath('/')).toBe('library');
    expect(screenForPath('/capture')).toBe('capture');
    expect(screenForPath('/notes/roof-repair')).toBe('note');
    expect(screenForPath('/elsewhere')).toBe('other');
  });
});

describe('the shell renders one landmark set', () => {
  it('has a skip link, a banner, one main and one navigation', async () => {
    mount();
    expect(screen.getByRole('link', { name: /skip to content/i })).toHaveAttribute(
      'href',
      '#main',
    );
    // The shell's own banner is there but empty on the library (round-3 T17):
    // the wordmark sits in the library's own heading row instead. A screen's
    // `<header>` inside <main> is not a banner in the browser's accessibility
    // tree, whatever jsdom thinks.
    expect(document.querySelector('.app__banner')).toBeEmptyDOMElement();
    expect(screen.getByText('Chintan').closest('header')).toHaveClass('library-header');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    expect(screen.getAllByRole('navigation')).toHaveLength(1);

    // Everywhere else the banner carries the wordmark.
    await userEvent.setup().click(screen.getByRole('link', { name: 'You' }));
    await waitFor(() => {
      expect(screen.getByText('Chintan').closest('header')).toHaveClass('app__banner');
    });
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
    expect(screen.getByRole('heading', { name: /^Notes/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Home' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'You' })).not.toHaveAttribute('aria-current');
  });

  it('calls the first tab Home while the library is still headed Notes', () => {
    // Two controls reading "Notes" on one screen — the heading and the tab —
    // read as two different places. The tab is the way home; the heading
    // names what is there (a later design pass restyles the header).
    mount();
    expect(screen.getByRole('link', { name: 'Home' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Notes' })).toBeNull();
    expect(screen.getByRole('heading', { name: /^Notes/ })).toBeInTheDocument();
  });

  it('seats the record button in the tab bar, not floating over content', () => {
    mount();
    const record = screen.getByRole('button', { name: /record/i });
    // A floating action button would be a sibling of <main> and would overlay
    // the last note row.
    expect(record.closest('.tab-bar')).not.toBeNull();
  });

  it('keeps the Home tab lit while reading a note', async () => {
    const user = userEvent.setup();
    const { router } = mount();
    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    expect(path(router)).toBe('/notes/roof-repair');
    expect(shell()).toHaveAttribute('data-screen', 'note');
    expect(screen.getByRole('link', { name: 'Home' })).toHaveAttribute('aria-current', 'page');
  });

  it('lights the You tab on settings', async () => {
    const user = userEvent.setup();
    const { router } = mount();
    await user.click(screen.getByRole('link', { name: 'You' }));
    // The screen is a lazy chunk (round-3 T47), so the navigation settles a tick later.
    await waitFor(() => {
      expect(path(router)).toBe('/settings');
    });
    expect(screen.getByRole('link', { name: 'You' })).toHaveAttribute('aria-current', 'page');
  });
});

describe('the capture screen is full screen', () => {
  it('drops the tab bar while recording', async () => {
    const user = userEvent.setup();
    const { router } = mount();

    await user.click(screen.getByRole('button', { name: /record/i }));

    // The capture screen is a lazy chunk (round-3 T47): the shell follows the URL a tick later.
    await waitFor(() => {
      expect(path(router)).toBe('/capture');
      expect(shell()).toHaveAttribute('data-screen', 'capture');
    });
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
    expect(screen.getByRole('heading', { name: /^Notes/ })).toBeInTheDocument();
  });

  it('sends /talk, the deleted PTT screen, Home with its query, since an installed app keeps the old shortcut for days (F7)', async () => {
    const { router } = mount(['/talk?note=roof-repair']);
    await settle();
    await waitFor(() => {
      expect(path(router)).toBe('/');
    });
    expect(screen.getByRole('heading', { name: /^Notes/ })).toBeInTheDocument();
  });

  it('sends /archive to the archived view, with the library beneath it for Back', async () => {
    const { router } = mount(['/archive']);
    await waitFor(() => {
      expect(url(router)).toBe('/?view=archived');
    });
    // The URL settles a render before the chip reflects it; on a slow runner
    // the synchronous assertion here read the previous render.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^Archived/ })).toHaveAttribute(
        'aria-pressed',
        'true',
      );
    });

    await goBack(router);
    expect(url(router)).toBe('/');
  });

  it('sends /search?q= to the library with the query kept', async () => {
    const { router } = mount(['/search?q=roof']);
    await waitFor(() => {
      expect(url(router)).toBe('/?q=roof');
    });
    await waitFor(() => {
      expect(screen.getByRole('searchbox', { name: /search notes/i })).toHaveValue('roof');
    });
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

  it('seeds the library beneath a cold-start deep link so Back stays in the app', async () => {
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
    expect(screen.getByRole('heading', { name: /^Notes/ })).toBeInTheDocument();
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

/**
 * The app's history is a stack with Home at the bottom (R8, F2): You and the
 * archive only ever sit one above it, and the Home tab goes back down to it
 * rather than pushing a second Home. These run on jsdom's real `history`,
 * because the moves read React Router's `history.state.idx`, which a memory
 * router never writes.
 */
describe("an app's stack, not a browser's", () => {
  // A browser router listens to the window's `popstate` until disposed.
  const mounted: Router[] = [];
  afterEach(() => {
    for (const router of mounted.splice(0)) router.dispose();
  });

  function mountBrowser(entry = '/') {
    // A fresh first entry: React Router numbers from whatever state it finds.
    window.history.replaceState(null, '', entry);
    const router = createBrowserRouter(routes);
    mounted.push(router);
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );
    return router;
  }

  const at = (router: Router) => ({ url: url(router), idx: historyIndex() });
  const tab = (name: string) =>
    screen.getByRole('navigation', { name: 'Main' }).querySelector(`a[href$="${name}"]`)!;

  async function landed(router: Router, expected: { url: string; idx: number }): Promise<void> {
    await waitFor(() => {
      expect(at(router)).toEqual(expected);
    });
  }

  it('pushes You from Home, and the Home tab goes back down to it', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(screen.getByRole('link', { name: 'You' }));
    await landed(router, { url: '/settings', idx: 1 });
    await user.click(screen.getByRole('link', { name: 'Home' }));
    await landed(router, { url: '/', idx: 0 });
    expect(router.state.historyAction).toBe('POP');
  });

  it('swaps You in for a note opened from Home, so Back from You is Home', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    await landed(router, { url: '/notes/roof-repair', idx: 1 });
    await user.click(screen.getByRole('link', { name: 'You' }));
    await landed(router, { url: '/settings', idx: 1 });
    await goBack(router);
    await landed(router, { url: '/', idx: 0 });
  });

  it('goes Home from two deep in one Back: You, About, then Home', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(screen.getByRole('link', { name: 'You' }));
    await user.click(await screen.findByRole('link', { name: /About/ }));
    await landed(router, { url: '/about', idx: 2 });
    await user.click(screen.getByRole('link', { name: 'Home' }));
    await landed(router, { url: '/', idx: 0 });
  });

  it('takes You from deep in the stack by going back to entry 1 and replacing it', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    // A second note opened from the first, as an Ask citation or "Open" does.
    await act(async () => {
      await router.navigate('/notes/reading-list');
    });
    await landed(router, { url: '/notes/reading-list', idx: 2 });
    await user.click(screen.getByRole('link', { name: 'You' }));
    await landed(router, { url: '/settings', idx: 1 });
    await goBack(router);
    await landed(router, { url: '/', idx: 0 });
  });

  it('sends "‹ You" back to You instead of stacking a second one', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(screen.getByRole('link', { name: 'You' }));
    await user.click(await screen.findByRole('link', { name: /About/ }));
    await user.click(await screen.findByRole('link', { name: /back to\s*you/i }, { timeout: 4_000 }));
    await landed(router, { url: '/settings', idx: 1 });
  });

  it('puts You in a cold About\'s place under "‹ You", leaving Home then You', async () => {
    const user = userEvent.setup();
    const router = mountBrowser('/about');
    await landed(router, { url: '/about', idx: 1 });
    await user.click(await screen.findByRole('link', { name: /back to\s*you/i }, { timeout: 4_000 }));
    await landed(router, { url: '/settings', idx: 1 });
    await goBack(router);
    await landed(router, { url: '/', idx: 0 });
  });

  it('seeds Home beneath a cold start on the archive', async () => {
    const router = mountBrowser('/?view=archived');
    await landed(router, { url: '/?view=archived', idx: 1 });
    await goBack(router);
    await landed(router, { url: '/', idx: 0 });
  });

  it('does nothing for the tab that is already open', async () => {
    const user = userEvent.setup();
    const router = mountBrowser();
    await user.click(screen.getByRole('link', { name: 'You' }));
    await landed(router, { url: '/settings', idx: 1 });
    const key = router.state.location.key;
    await user.click(screen.getByRole('link', { name: 'You' }));
    await settle();
    expect(router.state.location.key).toBe(key);
    expect(historyIndex()).toBe(1);
  });

  it('leaves a modified click on a tab to the browser', async () => {
    const router = mountBrowser();
    const you = tab('/settings');
    expect(you).toHaveAttribute('href', '/settings');
    // A new tab or window is the browser's; this one stays where it is.
    fireEvent.click(you, { ctrlKey: true });
    fireEvent.click(you, { metaKey: true });
    fireEvent.click(you, { button: 1 });
    await settle();
    expect(at(router)).toEqual({ url: '/', idx: 0 });
  });
});

/**
 * The deploy is a sub-path: `/chintan/dev/`, which is also the manifest's
 * `scope` and the service worker's. Every URL the app writes has to stay
 * under it. With the slash stripped from the basename, React Router's home
 * URL was the basename verbatim — `/chintan/dev` — so leaving a note,
 * pressing Archived or typing a search all landed outside the installed app
 * and outside the worker, where an offline reload is a browser error page.
 */
describe('every in-app URL stays inside the deploy scope', () => {
  const BASE = '/chintan/dev/';

  function mountScoped(entry = BASE) {
    const router = createMemoryRouter(routes, { initialEntries: [entry], basename: BASE });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );
    return router;
  }

  /** The address bar, not the basename-stripped location the screens see. */
  const address = (router: Router) =>
    `${router.state.location.pathname}${router.state.location.search}`;

  it('keeps the trailing slash on the library and its filters', async () => {
    const user = userEvent.setup();
    const router = mountScoped();
    await screen.findByRole('button', { name: /roof repair/i });

    // The archive's count is asked for once the launch is idle, which jsdom
    // (no requestIdleCallback) stands in for with a two-second delay.
    await user.click(await screen.findByRole('button', { name: /^Archived/ }, { timeout: 4_000 }));
    expect(address(router)).toBe('/chintan/dev/?view=archived');

    await user.click(screen.getByRole('button', { name: 'All' }));
    expect(address(router)).toBe('/chintan/dev/');
  });

  it('puts notes, settings and the capture screen under the scope', async () => {
    const user = userEvent.setup();
    const router = mountScoped();

    await user.click(await screen.findByRole('button', { name: /roof repair/i }));
    expect(address(router)).toBe('/chintan/dev/notes/roof-repair');

    await user.click(screen.getByRole('button', { name: /back to\s*notes/i }));
    expect(address(router)).toBe('/chintan/dev/');

    expect(screen.getByRole('link', { name: 'You' })).toHaveAttribute('href', '/chintan/dev/settings');
    await user.click(screen.getByRole('button', { name: /record/i }));
    expect(address(router)).toBe('/chintan/dev/capture');
  });

  it('seeds the library under a deep link at its scoped address', async () => {
    const router = mountScoped('/chintan/dev/notes/roof-repair');
    await settle();
    expect(address(router)).toBe('/chintan/dev/notes/roof-repair');
    await goBack(router);
    expect(address(router)).toBe('/chintan/dev/');
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
