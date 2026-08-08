import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { ThemeProvider } from '@/theme/ThemeProvider.tsx';

import { routes } from './router.tsx';

type Router = ReturnType<typeof createMemoryRouter>;

function mount(initialEntries: string[] = ['/']) {
  const router = createMemoryRouter(routes, { initialEntries });
  const view = render(
    <ThemeProvider>
      <RouterProvider router={router} />
    </ThemeProvider>,
  );
  return { router, view };
}

/** Browser / Android Back, flushed so the shell has re-rendered. */
async function goBack(router: Router): Promise<void> {
  await act(async () => {
    await router.navigate(-1);
  });
}

/** Lets the back guard's history seed settle before assertions. */
async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
  });
}

const path = (router: Router) => router.state.location.pathname;

const shell = () => document.querySelector('.app');

describe('the shell renders one landmark set', () => {
  it('has a skip link, a banner, and exactly one main', () => {
    mount();
    expect(screen.getByRole('link', { name: /skip to content/i })).toHaveAttribute(
      'href',
      '#main',
    );
    expect(screen.getByRole('banner')).toBeInTheDocument();
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('exposes a polite live region for pipeline status', () => {
    mount();
    const region = screen.getByTestId('status-region');
    expect(region).toHaveAttribute('aria-live', 'polite');
    expect(region).toHaveAttribute('role', 'status');
  });
});

describe('record-first home', () => {
  it('shows the record target and the collapsed strip', () => {
    mount();
    expect(shell()).toHaveAttribute('data-sheet-state', 'collapsed');
    expect(screen.getByRole('button', { name: /record/i })).toBeInTheDocument();
    for (const label of ['Notes', 'Search', 'You']) {
      expect(screen.getByRole('link', { name: label })).toBeInTheDocument();
    }
  });
});

describe('the expanded sheet', () => {
  it('puts the record button in the bottom bar, not floating over content', async () => {
    const user = userEvent.setup();
    const { router } = mount();

    await user.click(screen.getByRole('link', { name: 'Notes' }));

    expect(path(router)).toBe('/notes');
    expect(shell()).toHaveAttribute('data-sheet-state', 'expanded');

    const record = screen.getByRole('button', { name: /record/i });
    // The record control is a descendant of the bottom bar. A floating action
    // button would be a sibling of <main> and would overlay the last note row.
    expect(record.closest('.bottom-bar')).not.toBeNull();
  });

  it('locks shut while recording', async () => {
    const user = userEvent.setup();
    const { router } = mount();

    await user.click(screen.getByRole('button', { name: /record/i }));

    expect(path(router)).toBe('/capture');
    expect(shell()).toHaveAttribute('data-sheet-state', 'locked');
    expect(screen.queryByRole('navigation', { name: 'Library' })).not.toBeInTheDocument();
  });
});

describe('Back never exits the app', () => {
  it('collapses the sheet instead of leaving, after opening the library', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/']);

    await user.click(screen.getByRole('link', { name: 'Notes' }));
    expect(path(router)).toBe('/notes');
    expect(shell()).toHaveAttribute('data-sheet-state', 'expanded');

    // Browser / Android Back.
    await goBack(router);

    expect(path(router)).toBe('/');
    expect(shell()).toHaveAttribute('data-sheet-state', 'collapsed');
    // Still inside the app: the record surface is mounted.
    expect(screen.getByRole('button', { name: /record/i })).toBeInTheDocument();
  });

  it('collapses the sheet from a cold-start deep link, where v1 would have exited', async () => {
    // Entering directly at /notes gives the app one history entry, so Back
    // would leave the tab. useBackGuard seeds home beneath it.
    const { router } = mount(['/notes']);

    // The seed replaces the initial entry with home and pushes /notes back on
    // top, so the deep link is still what is rendered.
    await settle();
    expect(router.state.location.key).not.toBe('default');
    expect(path(router)).toBe('/notes');

    await goBack(router);

    expect(path(router)).toBe('/');
    expect(shell()).toHaveAttribute('data-sheet-state', 'collapsed');
    expect(screen.getByRole('button', { name: /record/i })).toBeInTheDocument();
  });

  it('pops the note detail screen back to the library', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/']);

    await user.click(screen.getByRole('link', { name: 'Notes' }));
    await user.click(screen.getByRole('button', { name: /roof repair/i }));

    expect(path(router)).toBe('/notes/roof-repair');

    await goBack(router);

    expect(path(router)).toBe('/notes');
    expect(shell()).toHaveAttribute('data-sheet-state', 'expanded');
  });

  it('leaves the capture screen back to home', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/']);

    await user.click(screen.getByRole('button', { name: /record/i }));
    expect(path(router)).toBe('/capture');

    await goBack(router);

    expect(path(router)).toBe('/');
    expect(shell()).toHaveAttribute('data-sheet-state', 'collapsed');
  });
});

describe('accessibility of the library', () => {
  it('renders note rows as real buttons, not clickable divs', async () => {
    const user = userEvent.setup();
    mount();
    await user.click(screen.getByRole('link', { name: 'Notes' }));

    const row = screen.getByRole('button', { name: /roof repair/i });
    expect(row.tagName).toBe('BUTTON');
    expect(row).toHaveAttribute('type', 'button');
  });

  it('moves focus to the routed region on navigation', async () => {
    const user = userEvent.setup();
    mount();

    await user.click(screen.getByRole('link', { name: 'Notes' }));

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole('main'));
    });
  });
});

describe('search state lives in the URL', () => {
  it('reflects the typed query into the query string', async () => {
    const user = userEvent.setup();
    const { router } = mount(['/search']);
    await settle();
    expect(path(router)).toBe('/search');

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'roof');

    await waitFor(() => {
      expect(router.state.location.search).toBe('?q=roof');
    });
    expect(screen.getByRole('button', { name: /roof repair/i })).toBeInTheDocument();
  });
});
