import { zipSync } from 'fflate';

import type { RecordingUrlWire } from '@/api/schema.ts';

/**
 * Several recordings as one archive, built on the device.
 *
 * The server hands out presigned URLs and filenames in one manifest
 * (`GET /v1/notes/{id}/recordings/urls`); this fetches each file and zips them.
 * Stored, not deflated: the audio is already Opus or AAC and deflating it
 * again costs seconds of CPU on a phone for a saving of nothing. `fflate` is
 * eight kilobytes for exactly this — no backend change, no Lambda spending a
 * minute streaming megabytes back through the API.
 *
 * Fetched one at a time so the progress line ("3 of 12…") means what it says
 * and a cellular link is not asked for twelve parallel downloads. `no-store`:
 * never the media element's cached answer, whatever mode it was fetched in —
 * see the `<audio>` element in `Recordings`.
 */
export async function zipRecordings(
  items: readonly RecordingUrlWire[],
  onProgress: (done: number, total: number) => void = () => {},
  fetchImpl: typeof fetch = (...args) => globalThis.fetch(...args),
): Promise<Blob> {
  const files: Record<string, [Uint8Array, { level: 0 }]> = {};
  let done = 0;
  for (const item of items) {
    const response = await fetchImpl(item.url, { cache: 'no-store' });
    if (!response.ok) throw new Error(`audio fetch failed: ${String(response.status)}`);
    files[item.filename] = [new Uint8Array(await response.arrayBuffer()), { level: 0 }];
    done += 1;
    onProgress(done, items.length);
  }
  const bytes = zipSync(files);
  return new Blob([bytes as BlobPart], { type: 'application/zip' });
}

/**
 * `<note-slug>-recordings.zip`: the same slug the manifest's own filenames
 * carry, so the archive and the files inside it are named alike. Lowercased,
 * anything that is not a letter or digit becomes one hyphen, and a title that
 * slugs to nothing falls back to `note`.
 */
export function archiveName(title: string): string {
  const slug = title
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60);
  return `${slug || 'note'}-recordings.zip`;
}
