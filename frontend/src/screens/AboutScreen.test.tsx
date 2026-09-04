import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { routes } from '@/app/router.tsx';
import { config } from '@/config/env.ts';
import { TestProviders } from '@/test/providers.tsx';

import { AboutScreen, BACKLOG_URL, REPOSITORY_URL } from './AboutScreen.tsx';

function mountAlone() {
  render(
    <MemoryRouter initialEntries={['/about']}>
      <AboutScreen />
    </MemoryRouter>,
  );
}

describe('About Chintan', () => {
  it('says what the app does, how filing decides, and where the data lives', () => {
    mountAlone();

    expect(screen.getByRole('heading', { name: 'About Chintan', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /what it does/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /how filing works/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /where your data lives/i })).toBeInTheDocument();

    // The two ways a recording finds its note, both named.
    expect(screen.getByText(/add this to the roof note/i)).toBeInTheDocument();
    expect(screen.getByText(/record into this/i)).toBeInTheDocument();

    // Your own account, and the one sentence on retention.
    expect(screen.getByText(/in your own aws account and nowhere else/i)).toBeInTheDocument();
    expect(screen.getByText(/thirty days after you archive it/i)).toBeInTheDocument();
  });

  it('names the build it is running, as the footnote on You does', () => {
    mountAlone();
    expect(screen.getByText(config.version, { selector: 'code' })).toBeInTheDocument();
    expect(screen.getByText(/app version/i)).toBeInTheDocument();
  });

  it('links to the repository and to what is next, in a new tab', () => {
    mountAlone();

    const source = screen.getByRole('link', { name: /source on github/i });
    expect(source).toHaveAttribute('href', REPOSITORY_URL);
    expect(source).toHaveAttribute('target', '_blank');
    expect(source).toHaveAttribute('rel', expect.stringContaining('noreferrer'));

    const next = screen.getByRole('link', { name: /what.s next/i });
    expect(next).toHaveAttribute('href', BACKLOG_URL);
    expect(BACKLOG_URL).toContain('docs/backlog.md');
  });
});

describe('getting there and back', () => {
  it('is a real route, reached from You, and Back returns to You', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/settings'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    await userEvent.click(await screen.findByRole('link', { name: /about chintan/i }));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/about');
    });
    expect(screen.getByRole('heading', { name: 'About Chintan', level: 1 })).toBeInTheDocument();
    // The shell announces it as its own screen, not as a generic one.
    expect(screen.getByRole('main')).toHaveAttribute('aria-label', 'About');

    // The visually-hidden "Back to " and the visible "You" may or may not be
    // joined with a space by the name computation; either reads the same.
    await userEvent.click(screen.getByRole('link', { name: /back to\s*you/i }));
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings');
    });
  });
});
