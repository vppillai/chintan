import { render, screen } from '@testing-library/react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { TestProviders } from '@/test/providers.tsx';

import { routes } from './router.tsx';

/*
 * A lazy screen's chunk that never arrives — a deploy between the shell and
 * the tap, a flaky link. `React.lazy` throws the rejection on render, and
 * the route's `ErrorBoundary` must catch it as it catches any render fault:
 * the shell and its navigation stay, and the screen is the one with two
 * ways out. Without the boundary React Router replaces the document with
 * its raw error page.
 */
vi.mock('@/screens/AboutScreen.tsx', () => {
  throw new Error('Failed to fetch dynamically imported module');
});

describe('a lazy screen whose chunk fails to load', () => {
  it('is drawn as the route error inside the shell, with its two ways out', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const router = createMemoryRouter(routes, { initialEntries: ['/about'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    expect(
      await screen.findByRole('heading', { name: 'This screen could not be drawn' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to your notes' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reload the app' })).toBeInTheDocument();
    // The boundary is the route's, not the root's: the tab bar survives.
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeInTheDocument();
  });
});
