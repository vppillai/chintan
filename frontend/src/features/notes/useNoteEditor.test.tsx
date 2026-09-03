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

describe('a same-tick edit-then-commit is not silently dropped', () => {
  /*
   * TagEditor's add/remove call `onChange` (which calls `edit()`) and then
   * `onCommit` (which calls `saveNow()`) back to back, synchronously, in the
   * same event handler — unlike a debounced text field, which only ever
   * calls `edit()` and lets the timer call `save()` later, after at least one
   * render has passed.
   *
   * `saveNow()` used to call `save()`, which read `latest.current` — a ref
   * mirrored from `model` by an effect that runs after the next render. With
   * no render between `edit()` and `saveNow()`, `save()` saw the pre-edit
   * draft, found nothing dirty, and returned — and the debounce that would
   * have caught it a moment later had already been cancelled by that same
   * `saveNow()`. The tag was never sent, with no error, until a reload made
   * the loss visible.
   */
  it('saves the value edit() just applied when saveNow() is called synchronously after it, with no render in between', async () => {
    const { patches, wrapper } = harness();
    const view = renderHook(() => useNoteEditor(NOTE), { wrapper });

    await act(async () => {
      view.result.current.edit({ tags: ['recipe'] });
      await view.result.current.saveNow();
    });

    expect(patches).toHaveLength(1);
    expect(patches[0]?.body['tags']).toEqual(['recipe']);
  });
});

describe('one PATCH at a time', () => {
  /*
   * Type, wait out the debounce, and keep typing while the first PATCH is on a
   * slow connection. There used to be two requests in flight carrying the same
   * `version`: the second lost with a 409 and the screen announced "a voice
   * capture or another device saved this note while you were editing" — about
   * the user's own keystrokes — and offered to throw one burst away. On
   * cellular a three-second PATCH is routine, so this was the ordinary case,
   * not the corner.
   */
  it('sends exactly two PATCHes for a keystroke made mid-save, the second with the version the first returned', async () => {
    const patches: Record<string, unknown>[] = [];
    let release: (() => void) | null = null;
    // The server jumps to a version the client could not have guessed, so a
    // `version + 1` regression fails here rather than passing by coincidence.
    const SERVER_VERSION_AFTER_FIRST = 7;

    const fetchImpl = vi.fn<typeof fetch>(async (_input, init) => {
      if ((init?.method ?? 'GET') === 'PATCH') {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        patches.push(body);
        const first = patches.length === 1;
        if (first) {
          await new Promise<void>((resolve) => {
            release = resolve;
          });
        }
        return new Response(
          JSON.stringify({
            ...NOTE,
            version: first ? SERVER_VERSION_AFTER_FIRST : SERVER_VERSION_AFTER_FIRST + 1,
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        );
      }
      return new Response(JSON.stringify(NOTE), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    });
    const api = testApiContext(fetchImpl);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <TestProviders api={api}>{children}</TestProviders>
    );
    const view = renderHook(() => useNoteEditor(NOTE), { wrapper });

    // First burst, and the save leaves.
    act(() => {
      view.result.current.edit({ body: 'Ellis quoted' });
    });
    let firstSave: Promise<void> | undefined;
    act(() => {
      firstSave = view.result.current.saveNow();
    });
    await waitFor(() => {
      expect(patches).toHaveLength(1);
    });
    expect(patches[0]?.['version']).toBe(NOTE.version);
    expect(view.result.current.model.state).toBe('saving');

    // Second burst while it is on the wire. Asking to save again must not put
    // a second request out with the same version.
    act(() => {
      view.result.current.edit({ body: 'Ellis quoted nine hundred pounds.' });
    });
    let secondSave: Promise<void> | undefined;
    act(() => {
      secondSave = view.result.current.saveNow();
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(patches, 'a second PATCH left while the first was in flight').toHaveLength(1);

    // The first lands. The re-save follows on its own, carrying what the
    // server said the version now is.
    await act(async () => {
      release?.();
      await Promise.all([firstSave, secondSave]);
    });
    await waitFor(() => {
      expect(patches).toHaveLength(2);
    });
    expect(patches[1]?.['version']).toBe(SERVER_VERSION_AFTER_FIRST);
    expect(patches[1]?.['body']).toBe('Ellis quoted nine hundred pounds.');

    await waitFor(() => {
      expect(view.result.current.model.state).toBe('saved');
    });
    expect(view.result.current.model.version).toBe(SERVER_VERSION_AFTER_FIRST + 1);
    // And no fabricated conflict at any point.
    expect(view.result.current.model.theirs).toBeNull();
    expect(patches).toHaveLength(2);
  });
});
