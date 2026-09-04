import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { CaptureWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { Recordings } from './Recordings.tsx';

/**
 * The audio lives in a bucket on another origin, behind a presigned URL. Two
 * requests go there for one recording — the `<audio>` element's, and the
 * "Download audio" fetch — and on the live app the second failed every time:
 * the element had fetched in no-cors mode, S3 had answered without
 * `Access-Control-Allow-Origin` (no `Origin` was sent), and Chromium served
 * that cached response to the CORS `fetch()`, which then failed its check.
 */

const AUDIO_URL =
  'https://chintan-content.s3.test/tenants/t1/captures/cap-1/audio.webm?X-Amz-Signature=abc';

const CAPTURE: CaptureWire = {
  id: 'cap-1',
  status: 'appended',
  created_at: '2026-08-06T09:10:00.000Z',
  version: 1,
  note_id: 'roof-repair',
  duration_ms: 12_000,
  has_peaks: false,
  has_segments: false,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/** The API hands out the presigned URL; the bucket itself is the global `fetch`. */
function mount() {
  const api = vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/download')) {
      if (url.searchParams.get('kind') === 'audio') {
        return json({ url: AUDIO_URL, expires_at: new Date(Date.now() + 900_000).toISOString() });
      }
      return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
    }
    return json({ items: [] });
  });

  const bucket = vi.fn<typeof fetch>(
    async () =>
      new Response(new Blob(['webm bytes'], { type: 'audio/webm' }), {
        status: 200,
        headers: { 'content-type': 'audio/webm' },
      }),
  );
  vi.stubGlobal('fetch', bucket);

  render(
    <TestProviders api={testApiContext(api)}>
      <MemoryRouter>
        <Recordings captures={[CAPTURE]} />
      </MemoryRouter>
    </TestProviders>,
  );
  return { bucket };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('the audio is fetched the same way twice', () => {
  it('loads the recording into the element as a CORS request', async () => {
    mount();

    await screen.findByRole('region', { name: 'Recording' });
    const audio = document.querySelector('audio');
    expect(audio).not.toBeNull();
    expect(audio).toHaveAttribute('src', AUDIO_URL);
    // Without this the element's no-cors response poisons the cache for the
    // download's CORS fetch. See the component.
    expect(audio).toHaveAttribute('crossorigin', 'anonymous');
  });

  it('downloads with a request the media element’s cached answer can never satisfy', async () => {
    const user = userEvent.setup();
    URL.createObjectURL = vi.fn(() => 'blob:mock-url');
    URL.revokeObjectURL = vi.fn();
    const clicks: string[] = [];
    const originalClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
      clicks.push(this.download);
    };

    try {
      const { bucket } = mount();
      await user.click(await screen.findByRole('button', { name: 'Download audio' }));

      expect(await screen.findByText('Downloaded')).toBeInTheDocument();
      expect(bucket).toHaveBeenCalledWith(AUDIO_URL, expect.objectContaining({ cache: 'no-store' }));
      // Named after the object's real extension, not the query string.
      expect(clicks).toEqual(['chintan-cap-1.webm']);
    } finally {
      HTMLAnchorElement.prototype.click = originalClick;
    }
  });
});
