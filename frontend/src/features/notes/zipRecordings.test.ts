import { unzipSync } from 'fflate';
import { describe, expect, it, vi } from 'vitest';

import { bytesOf } from '@/test/blob.ts';

import { archiveName, zipRecordings } from './zipRecordings.ts';

const ITEMS = [
  { capture_id: 'a', filename: 'roof-20260806-0910.webm', url: 'https://b.test/a', expires_at: '' },
  { capture_id: 'b', filename: 'roof-20260806-0912.webm', url: 'https://b.test/b', expires_at: '' },
];

describe('zipRecordings', () => {
  it('fetches each file with no-store, reports progress, and stores them uncompressed', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input) => new Response(`bytes of ${String(input)}`));
    const progress: [number, number][] = [];

    const blob = await zipRecordings(ITEMS, (done, total) => progress.push([done, total]), fetchImpl);

    expect(fetchImpl.mock.calls.map(([url, init]) => [url, init?.cache])).toEqual([
      ['https://b.test/a', 'no-store'],
      ['https://b.test/b', 'no-store'],
    ]);
    expect(progress).toEqual([
      [1, 2],
      [2, 2],
    ]);
    expect(blob.type).toBe('application/zip');
    const files = unzipSync(await bytesOf(blob));
    expect(new TextDecoder().decode(files['roof-20260806-0912.webm'])).toBe('bytes of https://b.test/b');
    // Stored: the archive is no smaller than what went in, plus headers.
    const raw = ITEMS.reduce((sum, item) => sum + `bytes of ${item.url}`.length, 0);
    expect(blob.size).toBeGreaterThan(raw);
  });

  it('fails the whole archive when one file cannot be fetched', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => new Response(null, { status: 403 }));
    await expect(zipRecordings(ITEMS, undefined, fetchImpl)).rejects.toThrow(/403/);
  });
});

describe('archiveName', () => {
  it('slugs the title the way the manifest filenames are slugged', () => {
    expect(archiveName('Roof repair')).toBe('roof-repair-recordings.zip');
    expect(archiveName('  Café: notes / ideas!  ')).toBe('cafe-notes-ideas-recordings.zip');
    expect(archiveName('???')).toBe('note-recordings.zip');
  });
});
