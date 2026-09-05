import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { routes } from '@/app/router.tsx';
import { config } from '@/config/env.ts';
import { TestProviders } from '@/test/providers.tsx';

import { AboutScreen, BACKLOG_URL, LICENSE_URL, REPOSITORY_URL } from './AboutScreen.tsx';

function mountAlone() {
  render(
    <MemoryRouter initialEntries={['/about']}>
      <AboutScreen />
    </MemoryRouter>,
  );
}

describe('About', () => {
  it('opens on the app’s own name and sentence, from the instance config', () => {
    mountAlone();

    expect(screen.getByRole('heading', { name: config.appName, level: 1 })).toBeInTheDocument();
    expect(screen.getByText(config.appDescription)).toHaveClass('about__lede');
    // What it does, in one paragraph under the name.
    expect(screen.getByText(/tap record and talk/i)).toHaveTextContent(
      /hear what you actually said/i,
    );
  });

  it('has three sections a person can find by heading: filing, data, and the source', () => {
    mountAlone();
    const headings = screen.getAllByRole('heading', { level: 2 }).map((heading) => heading.textContent);
    expect(headings).toEqual(['How filing works', 'Your data', 'Privacy & open source']);
  });

  it('draws filing as the five steps a recording takes, then says how the router decides', () => {
    mountAlone();

    const steps = screen.getByRole('list', { name: /five steps/i });
    expect(
      Array.from(steps.querySelectorAll('.about__step-title'), (title) => title.textContent?.replace(/^\d/, '')),
    ).toEqual(['Record', 'Transcribe', 'Route', 'Clean up', 'Append']);

    // The two ways a recording finds its note, both named, and what happens when it is not sure.
    expect(screen.getByText(/add this to the roof note/i)).toBeInTheDocument();
    expect(screen.getByText(/record into this/i)).toBeInTheDocument();
    expect(screen.getByText(/waits at the top of your notes/i)).toBeInTheDocument();
    // Cleanup follows the note.
    expect(screen.getByText(/a note marked verbatim is left exactly as spoken/i)).toBeInTheDocument();
  });

  it('says where each kind of data lives, in the reader’s own account, and for how long', () => {
    mountAlone();

    const stores = document.querySelector('.about__stores');
    expect(stores).toHaveTextContent(/Sign-in\s*Cognito/);
    expect(stores).toHaveTextContent(/Notes\s*DynamoDB/);
    expect(stores).toHaveTextContent(/Recordings and transcripts\s*S3/);

    expect(screen.getByText(/in your own aws account and nowhere else/i)).toHaveTextContent(
      /clears it when you sign out/i,
    );
    expect(screen.getByText(/thirty days after you archive it/i)).toBeInTheDocument();
  });

  it('mentions the daily spending cap once, plainly, and never its amount (U13b)', () => {
    mountAlone();
    const line = screen.getByText(/a daily spending cap on the transcription and cleanup providers/i);
    expect(line).toHaveTextContent(/recordings say so and resume the next day/i);
    expect(line.textContent).not.toMatch(/\$/);
    expect(line.closest('.about__section')).toContainElement(
      screen.getByRole('heading', { name: 'Your data' }),
    );
  });

  it('names the build it is running, as the Version row on You does', () => {
    mountAlone();
    expect(screen.getByText(config.version, { selector: 'code' })).toBeInTheDocument();
    expect(screen.getByText(/app version/i)).toBeInTheDocument();
  });

  it('names the licence and links to the repository and to what is next, in a new tab', () => {
    mountAlone();

    const licence = screen.getByRole('link', { name: /mit licence/i });
    expect(licence).toHaveAttribute('href', LICENSE_URL);

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
    expect(screen.getByRole('heading', { name: config.appName, level: 1 })).toBeInTheDocument();
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
