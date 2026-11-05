import { render, screen, waitFor } from '@testing-library/react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { routes } from '@/app/router.tsx';
import { TestProviders } from '@/test/providers.tsx';

/*
 * Home's two lazy extras — the offer of a stranded recording and the passkey
 * nudge — whose chunks cannot be fetched: the connection dropped between the
 * library's paint and the fetch. Home stays Home.
 */
vi.mock('@/features/capture/ResumePrompt.tsx', () => {
  throw new Error('Failed to fetch dynamically imported module');
});
vi.mock('@/features/auth/PasskeyNudge.tsx', () => {
  throw new Error('Failed to fetch dynamically imported module');
});

describe('Home with a lazy extra that cannot be fetched', () => {
  it('draws the library, not the fault screen', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const router = createMemoryRouter(routes, { initialEntries: ['/'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    expect(await screen.findByRole('heading', { name: /notes/i })).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 50));
    await waitFor(() => {
      expect(screen.queryByRole('heading', { name: 'This screen could not be drawn' })).toBeNull();
    });
    expect(screen.getByRole('searchbox', { name: 'Search notes' })).toBeInTheDocument();
  });
});
