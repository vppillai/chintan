import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { setSystemPrefersDark } from '@/test/setup.ts';

import { ThemeProvider } from './ThemeProvider.tsx';
import { THEME_STORAGE_KEY } from './theme.ts';
import { useTheme } from './useTheme.ts';

function Probe() {
  const { preference, resolved, setPreference } = useTheme();
  return (
    <div>
      <span data-testid="preference">{preference}</span>
      <span data-testid="resolved">{resolved}</span>
      <button type="button" onClick={() => setPreference('nocturne')}>
        Nocturne
      </button>
      <button type="button" onClick={() => setPreference('system')}>
        System
      </button>
      <button type="button" onClick={() => setPreference('ink')}>
        Ink
      </button>
    </div>
  );
}

function renderProbe() {
  return render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );
}

const root = () => document.documentElement;

describe('useTheme', () => {
  it('defaults to Ink & Paper when nothing is stored', () => {
    renderProbe();
    expect(screen.getByTestId('preference')).toHaveTextContent('ink');
    expect(screen.getByTestId('resolved')).toHaveTextContent('ink');
    expect(root()).toHaveAttribute('data-theme', 'ink');
  });

  it('applies an explicit Nocturne choice and persists it', async () => {
    const user = userEvent.setup();
    renderProbe();

    await user.click(screen.getByRole('button', { name: 'Nocturne' }));

    expect(screen.getByTestId('preference')).toHaveTextContent('nocturne');
    expect(screen.getByTestId('resolved')).toHaveTextContent('nocturne');
    expect(root()).toHaveAttribute('data-theme', 'nocturne');
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('nocturne');
  });

  it('paints the browser chrome with the ground the theme resolves to', async () => {
    // jsdom computes no custom properties, so the token is answered by hand,
    // with stand-ins rather than colours (the token lint reads tests too):
    // whatever `--color-ground` resolves to is what the tags must carry.
    const grounds: Record<string, string> = { ink: 'paper-ground', nocturne: 'night-ground' };
    vi.spyOn(window, 'getComputedStyle').mockImplementation(
      () =>
        ({
          getPropertyValue: (name: string) =>
            name === '--color-ground' ? (grounds[root().dataset['theme'] ?? ''] ?? '') : '',
        }) as unknown as CSSStyleDeclaration,
    );
    for (const media of ['(prefers-color-scheme: light)', '(prefers-color-scheme: dark)']) {
      const meta = document.createElement('meta');
      meta.name = 'theme-color';
      meta.media = media;
      meta.content = 'stale';
      document.head.append(meta);
    }
    const contents = () =>
      Array.from(document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]'), (m) => m.content);

    const user = userEvent.setup();
    renderProbe();
    expect(contents()).toEqual(['paper-ground', 'paper-ground']);

    // An explicit Nocturne on a light-mode device: both tags, so the media
    // query cannot pick the pale one.
    await user.click(screen.getByRole('button', { name: 'Nocturne' }));
    expect(contents()).toEqual(['night-ground', 'night-ground']);

    document.querySelectorAll('meta[name="theme-color"]').forEach((meta) => meta.remove());
  });

  it('an explicit choice ignores the system preference', async () => {
    const user = userEvent.setup();
    renderProbe();
    await user.click(screen.getByRole('button', { name: 'Ink' }));

    act(() => {
      setSystemPrefersDark(true);
    });

    expect(screen.getByTestId('resolved')).toHaveTextContent('ink');
    expect(root()).toHaveAttribute('data-theme', 'ink');
  });

  describe('follow system', () => {
    it('resolves to Ink & Paper under prefers-color-scheme: light', async () => {
      const user = userEvent.setup();
      renderProbe();

      await user.click(screen.getByRole('button', { name: 'System' }));

      expect(screen.getByTestId('preference')).toHaveTextContent('system');
      expect(screen.getByTestId('resolved')).toHaveTextContent('ink');
      // The *preference* is what lands on <html>; tokens.css resolves it, so
      // there is no wrong-theme flash before this effect runs.
      expect(root()).toHaveAttribute('data-theme', 'system');
    });

    it('resolves to Nocturne under prefers-color-scheme: dark', async () => {
      const user = userEvent.setup();
      renderProbe();
      await user.click(screen.getByRole('button', { name: 'System' }));

      act(() => {
        setSystemPrefersDark(true);
      });

      expect(screen.getByTestId('resolved')).toHaveTextContent('nocturne');
      expect(root()).toHaveAttribute('data-resolved-theme', 'nocturne');
      expect(root()).toHaveAttribute('data-theme', 'system');
    });

    it('follows the system live, in both directions', async () => {
      const user = userEvent.setup();
      renderProbe();
      await user.click(screen.getByRole('button', { name: 'System' }));

      act(() => {
        setSystemPrefersDark(true);
      });
      expect(screen.getByTestId('resolved')).toHaveTextContent('nocturne');

      act(() => {
        setSystemPrefersDark(false);
      });
      expect(screen.getByTestId('resolved')).toHaveTextContent('ink');
    });

    it('picks up the system preference set before mount', () => {
      setSystemPrefersDark(true);
      window.localStorage.setItem(THEME_STORAGE_KEY, 'system');

      renderProbe();

      expect(screen.getByTestId('resolved')).toHaveTextContent('nocturne');
    });
  });

  it('restores a stored preference on mount', () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, 'nocturne');
    renderProbe();
    expect(screen.getByTestId('preference')).toHaveTextContent('nocturne');
  });

  it('falls back to the default for a corrupt stored value', () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, 'solarized');
    renderProbe();
    expect(screen.getByTestId('preference')).toHaveTextContent('ink');
  });

  it('throws outside a provider rather than silently rendering unthemed', () => {
    expect(() => render(<Probe />)).toThrow(/ThemeProvider/);
  });
});
