import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { FilingBanner, bannerCapture } from './FilingBanner.tsx';
import { INITIAL_CAPTURE, type CaptureModel } from './machine.ts';

beforeEach(() => {
  localStorage.clear();
});

function capture(overrides: Partial<CaptureWire> = {}): CaptureWire {
  return {
    id: 'cap-1',
    note_id: 'roof-repair',
    status: 'transcribing',
    created_at: new Date().toISOString(),
    version: 1,
    ...overrides,
  };
}

function note(captures: CaptureWire[]): NoteDetailWire {
  return { ...TEST_NOTES[0]!, body: 'Ridge tiles.', captures };
}

function mount(captures: CaptureWire[], localUpload: CaptureModel | null = null) {
  const calls: string[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    calls.push(`${init?.method ?? 'GET'} ${new URL(String(input)).pathname}`);
    return new Response(JSON.stringify(capture({ status: 'transcribing' })), {
      status: 202,
      headers: { 'content-type': 'application/json' },
    });
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <FilingBanner note={note(captures)} localUpload={localUpload} />
      </MemoryRouter>
    </TestProviders>,
  );
  return calls;
}

describe('the filing banner', () => {
  it('shows the stage strip while a recording is still being filed into the note', () => {
    mount([capture()]);
    const banner = screen.getByRole('region', { name: 'Filing a recording' });
    expect(banner).toHaveTextContent('Filing your recording');
    expect(screen.getByRole('list', { name: 'Filing progress' })).toBeInTheDocument();
    expect(banner).toHaveTextContent('Transcribing');
    // Nothing to act on yet: the strip is the whole message.
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('says nothing once the recording has landed — the body is the receipt', () => {
    mount([capture({ status: 'appended' })]);
    expect(screen.queryByRole('region', { name: 'Filing a recording' })).toBeNull();
  });

  it("offers the row's Retry and Dismiss for a capture that stopped short", async () => {
    const user = userEvent.setup();
    const calls = mount([capture({ status: 'failed', error: 'Transcription timed out' })]);
    expect(screen.getByRole('region', { name: 'Filing a recording' })).toHaveTextContent(
      'Transcription timed out',
    );

    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => {
      expect(calls).toContain('POST /v1/captures/cap-1/retry');
    });

    // Dismissed is remembered on the device, like the library's receipts.
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByRole('region', { name: 'Filing a recording' })).toBeNull();
    expect(localStorage.getItem('chintan.filing.dismissed')).toContain('cap-1');
  });

  it("shows this device's own upload, with its bar, before the server has a row", () => {
    mount([], {
      ...INITIAL_CAPTURE,
      state: 'uploading',
      localId: 'local-1',
      noteId: 'roof-repair',
      uploadProgress: 0.4,
    });
    expect(screen.getByRole('region', { name: 'Filing a recording' })).toHaveTextContent(
      'Uploading… 40%',
    );
  });
});

describe('which capture the banner is about', () => {
  it('is the newest that is moving or failed and not dismissed; never an appended one', () => {
    const older = capture({ id: 'a', status: 'failed', created_at: '2026-09-01T10:00:00.000Z' });
    const newer = capture({ id: 'b', status: 'uploaded', created_at: '2026-09-02T10:00:00.000Z' });
    const landed = capture({ id: 'c', status: 'appended', created_at: '2026-09-03T10:00:00.000Z' });
    expect(bannerCapture([older, landed, newer], new Set())?.id).toBe('b');
    expect(bannerCapture([older, landed], new Set())?.id).toBe('a');
    expect(bannerCapture([older, landed], new Set(['a']))).toBeNull();
    expect(bannerCapture([landed], new Set())).toBeNull();
  });
});
