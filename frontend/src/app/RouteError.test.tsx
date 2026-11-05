import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { THREAD_KEY } from '@/features/ask/thread.ts';

import { RouteError } from './RouteError.tsx';

function Broken(): never {
  throw new TypeError("Cannot read properties of undefined (reading 'map')");
}

describe('the route error screen', () => {
  it('says a fixed sentence, not the exception, and ends the Ask thread on the way out', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    sessionStorage.setItem(THREAD_KEY, '[{"status":"asking"}]');
    const router = createMemoryRouter(
      [
        { path: '/', element: <p>Home</p> },
        { path: '/broken', Component: Broken, ErrorBoundary: RouteError },
      ],
      { initialEntries: ['/broken'] },
    );
    render(<RouterProvider router={router} />);

    expect(await screen.findByText('No further detail was available.')).toBeInTheDocument();
    expect(screen.queryByText(/Cannot read properties/)).toBeNull();

    await userEvent.setup().click(screen.getByRole('link', { name: 'Back to your notes' }));
    expect(sessionStorage.getItem(THREAD_KEY)).toBeNull();
  });
});
