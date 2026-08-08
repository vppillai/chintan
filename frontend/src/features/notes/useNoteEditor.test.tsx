import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { AUTOSAVE_DELAY_MS } from './autosave.ts';
import { useNoteEditor } from './useNoteEditor.ts';

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles on the south slope have slipped.',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
};

interface Harness {
  patches: { url: string; body: Record<string, unknown> }[];
  wrapper: ({ children }: { children: ReactNode }) => ReactNode;
}

function harness(): Harness {
  const patches: { url: string; body: Record<string, unknown> }[] = [];

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    if ((init?.method ?? 'GET') === 'PATCH') {
      patches.push({ url, body: JSON.parse(String(init?.body)) as Record<string, unknown> });
      return new Response(JSON.stringify({ ...NOTE, version: NOTE.version + 1 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    }
    return new Response(JSON.stringify(NOTE), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });

  const api = testApiContext(fetchImpl);
  return {
    patches,
    wrapper: ({ children }) => <TestProviders api={api}>{children}</TestProviders>,
  };
}

describe('an edit is never dropped on the way out of the screen', () => {
  it('flushes the pending autosave when the screen unmounts', async () => {
    /*
     * Type a sentence and use the system Back gesture — the primary one-handed
     * way to leave a screen — within the 1.2s debounce.
     *
     * The unmount cleanup used to clear the timer without flushing it, and
     * `beforeunload` does not fire on a client-side navigation, so the result
     * was zero PATCHes, an unchanged stored body, and no warning: the indicator
     * said "Unsaved changes" and then the screen was gone. The in-app back
     * arrow only worked by accident, because tapping it blurs the textarea and
     * `onBlur` calls `saveNow()`; removing an element from the DOM fires no
     * blur.
     */
    const { patches, wrapper } = harness();
    const view = renderHook(() => useNoteEditor(NOTE), { wrapper });

    act(() => {
      view.result.current.edit({ body: 'Ellis quoted nine hundred pounds.' });
    });
    expect(patches, 'the debounce should not have fired yet').toHaveLength(0);

    view.unmount();

    await waitFor(() => {
      expect(patches).toHaveLength(1);
    });
    expect(patches[0]?.body['body']).toBe('Ellis quoted nine hundred pounds.');
    expect(patches[0]?.body['version']).toBe(NOTE.version);
  });

  it('flushes when the app is backgrounded rather than closed', async () => {
    // Switching apps freezes the document without unloading it, so
    // `visibilitychange` is routinely the last event the page ever gets.
    const { patches, wrapper } = harness();
    const view = renderHook(() => useNoteEditor(NOTE), { wrapper });

    act(() => {
      view.result.current.edit({ title: 'Roof repair — Ellis' });
    });

    act(() => {
      Object.defineProperty(document, 'visibilityState', {
        value: 'hidden',
        configurable: true,
      });
      document.dispatchEvent(new Event('visibilitychange'));
    });

    await waitFor(() => {
      expect(patches).toHaveLength(1);
    });
    expect(patches[0]?.body['title']).toBe('Roof repair — Ellis');

    Object.defineProperty(document, 'visibilityState', {
      value: 'visible',
      configurable: true,
    });
  });

  it('does not save a note that was never edited', async () => {
    const { patches, wrapper } = harness();
    renderHook(() => useNoteEditor(NOTE), { wrapper }).unmount();

    await new Promise((resolve) => setTimeout(resolve, AUTOSAVE_DELAY_MS / 4));
    expect(patches).toHaveLength(0);
  });
});
