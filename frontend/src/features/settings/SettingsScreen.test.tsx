import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { SettingsWire, UsageWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { SettingsScreen } from './SettingsScreen.tsx';

const STORED: SettingsWire = {
  cleanup_mode: 'faithful',
  retention_days: 0,
  theme: 'ink',
  default_language: 'en',
  daily_spend_cap_micros: 0,
};

const NO_USAGE: UsageWire = { month: '2026-09', cost_micros: 0, calls: 0, ops: {}, days: [] };

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/** You, over a stub API that stores what PUT sends and answers GET with it. */
export function mountSettings(
  overrides: { settings?: SettingsWire; usage?: UsageWire } = {},
) {
  let stored: SettingsWire = overrides.settings ?? STORED;
  const puts: SettingsWire[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/usage')) return json(overrides.usage ?? NO_USAGE);
    if (url.pathname.endsWith('/v1/settings')) {
      if ((init?.method ?? 'GET') === 'PUT') {
        const body = JSON.parse(String(init?.body)) as SettingsWire;
        puts.push(body);
        stored = { ...body, daily_spend_cap_micros: stored.daily_spend_cap_micros ?? 0 };
      }
      return json(stored);
    }
    return json({ items: [] });
  });

  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={['/settings']}>
        <SettingsScreen />
      </MemoryRouter>
    </TestProviders>,
  );
  return { puts, fetchImpl };
}

describe('the default transcription language', () => {
  it('shows what is stored, and saves a change through PUT /v1/settings', async () => {
    const { puts } = mountSettings();

    const select = (await screen.findByRole('combobox', {
      name: /default transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('en');
    });
    expect(screen.getByText(/all changes saved/i)).toBeInTheDocument();

    await userEvent.selectOptions(select, 'ml');

    // A draft change, not a save: the screen says so and offers the save.
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
    expect(screen.getByText(/transcribed as Malayalam/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.default_language).toBe('ml');
    // The rest of the record rides along unchanged: PUT replaces the whole thing.
    expect(puts[0]?.cleanup_mode).toBe('faithful');
    await screen.findByText(/all changes saved/i);
  });

  it('explains auto-detect honestly, including what it cannot do', async () => {
    mountSettings({ settings: { ...STORED, default_language: 'auto' } });

    const select = (await screen.findByRole('combobox', {
      name: /default transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('auto');
    });
    expect(screen.getByText(/mixes two is detected as one of them/i)).toBeInTheDocument();
  });

  it('reads a record from before the field existed as English', async () => {
    const { default_language: _absent, ...legacy } = STORED;
    mountSettings({ settings: legacy });

    const select = (await screen.findByRole('combobox', {
      name: /default transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('en');
    });
    // Not dirty: the absent field and English are the same stored state.
    expect(screen.getByText(/all changes saved/i)).toBeInTheDocument();
  });
});
