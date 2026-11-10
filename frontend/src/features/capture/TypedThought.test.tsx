import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { TYPED_THOUGHT_KEY, TypedThought } from './TypedThought.tsx';

/*
 * The typed thought lived in component state alone: Cancel, Back or a
 * discarded page lost a paragraph with no question asked, where a recording
 * of ten seconds is asked about (review round 19, FE-4).
 */

function mount(onDone = vi.fn()) {
  const fetchImpl: typeof fetch = async (input) =>
    new URL(String(input)).pathname.endsWith('/v1/captures/text')
      ? new Response(
          JSON.stringify({ capture: { id: 'srv-typed', status: 'transcribed', created_at: new Date().toISOString(), version: 1 } }),
          { status: 202, headers: { 'content-type': 'application/json' } },
        )
      : new Response(JSON.stringify({ items: [] }), { status: 200, headers: { 'content-type': 'application/json' } });
  const view = render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <TypedThought initialNoteId={null} onDone={onDone} />
    </TestProviders>,
  );
  return { onDone, view };
}

afterEach(() => {
  sessionStorage.clear();
});

describe('a typed thought is kept until it is sent or discarded', () => {
  it('comes back after the screen is left without Cancel', async () => {
    const user = userEvent.setup();
    const first = mount();
    await user.type(screen.getByRole('textbox', { name: 'Your thought' }), 'Call the roofer');
    // System Back, or a page the OS discarded: no handler runs.
    first.view.unmount();

    mount();
    expect(screen.getByRole('textbox', { name: 'Your thought' })).toHaveValue('Call the roofer');
  });

  it('asks before Cancel discards it, and Keep changes nothing', async () => {
    const user = userEvent.setup();
    const { onDone } = mount();
    await user.type(screen.getByRole('textbox', { name: 'Your thought' }), 'Call the roofer');

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    const dialog = screen.getByRole('dialog', { name: 'Discard what you typed?' });
    await user.click(within(dialog).getByRole('button', { name: 'Keep' }));
    expect(onDone).not.toHaveBeenCalled();
    expect(screen.getByRole('textbox', { name: 'Your thought' })).toHaveValue('Call the roofer');

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Discard' }));
    expect(onDone).toHaveBeenCalledWith(null);
    expect(sessionStorage.getItem(TYPED_THOUGHT_KEY)).toBeNull();
  });

  it('leaves at once on Cancel with nothing typed', async () => {
    const user = userEvent.setup();
    const { onDone } = mount();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onDone).toHaveBeenCalledWith(null);
  });

  it('is forgotten once sent', async () => {
    const user = userEvent.setup();
    const { onDone } = mount();
    await user.type(screen.getByRole('textbox', { name: 'Your thought' }), 'Call the roofer');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    await vi.waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });
    expect(sessionStorage.getItem(TYPED_THOUGHT_KEY)).toBeNull();
  });
});
