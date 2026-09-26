import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';

import type { CaptureWire } from '@/api/schema.ts';
import type { StoredCapture } from '@/offline/db.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { ResumePrompt, UNSENT_CAPTURES_KEY, relativeTime } from './ResumePrompt.tsx';
import { appendChunk, readCaptureRecord, saveCaptureRecord, unconfirmedCaptures } from './buffer.ts';
import { INITIAL_CAPTURE } from './machine.ts';
import { useCaptureStore } from './store.ts';

/*
 * The prompt reads the same IndexedDB the recorder writes (`buffer.ts`, real
 * under fake-indexeddb), so a test seeds a record the way a killed tab leaves
 * one: created, never confirmed. Send runs the real `uploadCapture`; with the
 * server already holding the bytes it confirms and prunes without a PUT, which
 * is the resume the prompt exists for.
 */

const FIVE_MINUTES_AGO = Date.now() - 5 * 60_000;

function stranded(overrides: Partial<StoredCapture> = {}): StoredCapture {
  return {
    localId: 'cap-a',
    serverCaptureId: 'srv-a',
    noteId: null,
    contentType: 'audio/webm',
    durationMs: 12_000,
    bytes: 9,
    chunkCount: 1,
    createdAt: FIVE_MINUTES_AGO,
    uploadedAt: null,
    peaks: null,
    ...overrides,
  };
}

/** The server, holding the capture at the status the test names. */
function serverWith(status: CaptureWire['status']): typeof fetch {
  return async (input) => {
    const url = new URL(String(input));
    const id = /\/v1\/captures\/([^/]+)$/.exec(url.pathname)?.[1];
    const body: CaptureWire | { items: never[] } = id
      ? { id, status, created_at: new Date().toISOString(), version: 1, note_id: null }
      : { items: [] };
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };
}

/**
 * Mounts the prompt. `settled` reads the device first, so a prompt that has
 * nothing to show renders nothing from its first paint — the assertion is
 * then on a finished state, not on an update the test would otherwise have
 * to wait for with nothing on screen to wait on.
 */
async function mount(
  fetchImpl: typeof fetch = serverWith('appended'),
  settled = false,
): Promise<void> {
  const queryClient = testQueryClient();
  if (settled) {
    await queryClient.prefetchQuery({ queryKey: UNSENT_CAPTURES_KEY, queryFn: unconfirmedCaptures });
  }
  render(
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      <ResumePrompt />
    </TestProviders>,
  );
}

afterEach(() => {
  // The store is a module singleton and outlives the test; a prompt still
  // mounted when it is reset would re-render outside React's own turn.
  act(() => {
    useCaptureStore.setState({ model: INITIAL_CAPTURE });
  });
});

describe('an unsent recording is offered back', () => {
  it('renders nothing when every recording on the device was confirmed', async () => {
    await saveCaptureRecord(stranded({ uploadedAt: Date.now() }));
    await mount(undefined, true);

    expect(screen.queryByRole('region', { name: 'Unsent recording' })).toBeNull();
  });

  it('names the oldest one by its age and length, and counts the rest', async () => {
    await saveCaptureRecord(stranded());
    await saveCaptureRecord(
      stranded({ localId: 'cap-b', serverCaptureId: 'srv-b', createdAt: Date.now() - 60_000 }),
    );
    await mount();

    const region = await screen.findByRole('region', { name: 'Unsent recording' });
    expect(region).toHaveTextContent('You have an unsent recording from 5 minutes ago.');
    expect(region).toHaveTextContent('0:12');
    expect(region).toHaveTextContent('1 more waiting');
    expect(within(region).getByRole('button', { name: 'Send' })).toBeInTheDocument();
    expect(within(region).getByRole('button', { name: 'Discard' })).toBeInTheDocument();
  });

  it('does not offer the recording the store is sending right now', async () => {
    // Send hands off to the library at once, so this list is read mid-upload;
    // offering that recording back would be a resend of the upload one row
    // below is showing progress for.
    await saveCaptureRecord(stranded());
    useCaptureStore.setState({
      model: { ...INITIAL_CAPTURE, state: 'uploading', localId: 'cap-a' },
    });
    await mount(undefined, true);

    expect(screen.queryByRole('region', { name: 'Unsent recording' })).toBeNull();
  });
});

describe('Send', () => {
  it('resumes the upload the server already has, confirms it and drops the prompt', async () => {
    const user = userEvent.setup();
    await saveCaptureRecord(stranded());
    await appendChunk('cap-a', 0, new Blob(['webm bytes'], { type: 'audio/webm' }));
    await mount(serverWith('appended'));

    await user.click(await screen.findByRole('button', { name: 'Send' }));

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Unsent recording' })).toBeNull();
    });
    // Confirmed on the device, as the server's answer said it had been.
    expect((await readCaptureRecord('cap-a'))?.uploadedAt).not.toBeNull();
  });

  it('says why when the upload fails, and keeps the recording', async () => {
    const user = userEvent.setup();
    // A record with no chunks behind it: the one failure `uploadCapture`
    // reports before it talks to the server.
    await saveCaptureRecord(stranded());
    await mount();

    await user.click(await screen.findByRole('button', { name: 'Send' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('The recording is empty.');
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
    expect((await readCaptureRecord('cap-a'))?.uploadedAt).toBeNull();
  });
});

describe('Discard', () => {
  it('asks first, then deletes the recording from the device and hides the prompt', async () => {
    const user = userEvent.setup();
    await saveCaptureRecord(stranded());
    await appendChunk('cap-a', 0, new Blob(['webm bytes'], { type: 'audio/webm' }));
    await mount();

    await user.click(await screen.findByRole('button', { name: 'Discard' }));
    const dialog = await screen.findByRole('dialog', { name: 'Discard this recording?' });
    expect(dialog).toHaveTextContent(/not saved anywhere else/);

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(await readCaptureRecord('cap-a')).toBeDefined();

    await user.click(screen.getByRole('button', { name: 'Discard' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Discard recording' }),
    );

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Unsent recording' })).toBeNull();
    });
    expect(await readCaptureRecord('cap-a')).toBeUndefined();
  });
});

describe('relativeTime', () => {
  it('rounds to the coarsest unit a person would say', () => {
    const now = 1_700_000_000_000;
    expect(relativeTime(now - 30_000, now)).toBe('a moment ago');
    expect(relativeTime(now + 60_000, now)).toBe('a moment ago');
    expect(relativeTime(now - 5 * 60_000, now)).toBe('5 minutes ago');
    expect(relativeTime(now - 60 * 60_000, now)).toBe('an hour ago');
    expect(relativeTime(now - 3 * 3_600_000, now)).toBe('3 hours ago');
    expect(relativeTime(now - 24 * 3_600_000, now)).toBe('yesterday');
    expect(relativeTime(now - 72 * 3_600_000, now)).toBe('3 days ago');
  });
});
