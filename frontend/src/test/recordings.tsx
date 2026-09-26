import type { QueryClient } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { vi } from 'vitest';

import { useNote } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import type { CaptureModel } from '@/features/capture/machine.ts';
import { Recordings } from '@/features/notes/Recordings.tsx';
import { describeMoment } from '@/features/notes/groups.ts';

import { TEST_NOTES, TestProviders, testApiContext, testQueryClient } from './providers.tsx';

/**
 * The recordings tab under test: one note with a recording or two, a small
 * stateful server behind `fetch`, and the note screen's own wiring around
 * `Recordings`. Shared by `Recordings.test.tsx`, `recordings/RecordingRow.test.tsx`
 * and the e2e-free pieces of the tab, because the row's behaviour — what its
 * menu offers, what its notice line says — is only observable with the list
 * around it, and one server that changes the note as the real one does is
 * better than three copies that drift.
 */

export const AUDIO_URL =
  'https://chintan-content.s3.test/tenants/t1/captures/cap-1/audio.webm?X-Amz-Signature=abc';

export const CAPTURE: CaptureWire = {
  id: 'cap-1',
  status: 'appended',
  created_at: '2026-08-06T09:10:00.000Z',
  version: 1,
  note_id: 'roof-repair',
  duration_ms: 12_000,
  has_peaks: false,
  has_segments: false,
};

export const OLDER: CaptureWire = {
  ...CAPTURE,
  id: 'cap-0',
  created_at: '2026-08-05T17:40:00.000Z',
};

export const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles.\n\nEllis quoted nine hundred.',
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [CAPTURE],
};

/**
 * The row's disclosure button, named by when the recording was made and how
 * long it runs: a filed row says nothing about being filed (T19), so the
 * moment is what tells the rows apart.
 */
export function isSummary(name: string): boolean {
  return [CAPTURE, OLDER].some((capture) => name.startsWith(describeMoment(capture.created_at)));
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/**
 * A small stateful server: the note as `GET /v1/notes/{id}` answers it,
 * presigned URLs, the note list for the move sheet, a recordings manifest, and
 * the two mutations answering as the contract says — and *changing the note*,
 * so the refetch the app makes afterwards sees one recording fewer, as the
 * real server's would. Tests override what they need to.
 */
export function apiStub(
  initial: NoteDetailWire = NOTE,
  overrides: Partial<
    Record<
      'delete' | 'move' | 'manifest' | 'segments' | 'retranscribe',
      (init?: RequestInit) => Response
    >
  > = {},
) {
  const note: NoteDetailWire = structuredClone(initial);
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const drop = (captureId: string): void => {
    note.captures = (note.captures ?? []).filter((capture) => capture.id !== captureId);
    note.version += 1;
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    calls.push({
      method,
      path: url.pathname + url.search,
      ...(typeof init?.body === 'string' ? { body: JSON.parse(init.body) as unknown } : {}),
    });
    if (url.pathname.endsWith('/download')) {
      if (url.searchParams.get('kind') === 'audio') {
        return json({ url: AUDIO_URL, expires_at: new Date(Date.now() + 900_000).toISOString() });
      }
      if (url.searchParams.get('kind') === 'segments' && overrides.segments) {
        return overrides.segments(init);
      }
      return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
    }
    if (url.pathname.endsWith('/recordings/urls')) {
      return (
        overrides.manifest?.(init) ??
        json({
          items: [OLDER, CAPTURE].map((capture) => ({
            capture_id: capture.id,
            filename: `roof-repair-${capture.id}.webm`,
            url: `https://chintan-content.s3.test/${capture.id}/audio.webm?sig=1`,
            expires_at: new Date(Date.now() + 900_000).toISOString(),
          })),
        })
      );
    }
    const captureId = /\/v1\/captures\/([^/]+)/.exec(url.pathname)?.[1] ?? '';
    if (method === 'DELETE' && captureId) {
      if (overrides.delete) return overrides.delete(init);
      drop(captureId);
      return new Response(null, { status: 204 });
    }
    if (method === 'POST' && url.pathname.endsWith('/move')) {
      if (overrides.move) return overrides.move(init);
      drop(captureId);
      // Re-pointed at the note asked for — or, asked for a title, at the
      // note made from it, which only the answer names.
      const body = JSON.parse(String(init?.body)) as { note_id?: string };
      return json({ ...CAPTURE, id: captureId, note_id: body.note_id ?? 'made-from-title' });
    }
    if (method === 'POST' && url.pathname.endsWith('/retranscribe')) {
      if (overrides.retranscribe) return overrides.retranscribe(init);
      // Back at the start of the pipeline, as the real server answers.
      const row = (note.captures ?? []).find((capture) => capture.id === captureId);
      if (row) row.status = 'transcribing';
      return json({ ...CAPTURE, id: captureId, status: 'transcribing' }, 202);
    }
    if (url.pathname.endsWith('/v1/notes')) {
      return json({
        items: [
          ...TEST_NOTES,
          { ...TEST_NOTES[0]!, id: 'fence', title: 'Old fence', archived: false },
        ],
      });
    }
    if (url.pathname.endsWith(`/v1/notes/${note.id}`)) return json(note);
    return json({ items: [] });
  });
  return { fetchImpl, calls, note };
}

export function bucketStub() {
  // A string body: the runtime's `Response` serialises a jsdom `Blob` as
  // "[object Blob]", which is not what a bucket returns.
  const bucket = vi.fn<typeof fetch>(
    async () =>
      new Response('webm bytes', {
        status: 200,
        headers: { 'content-type': 'audio/webm' },
      }),
  );
  vi.stubGlobal('fetch', bucket);
  return bucket;
}

/** The note screen's own wiring: the recordings read the note from the query. */
function Host({
  noteId,
  localUpload,
  onSelectingChange,
}: {
  noteId: string;
  localUpload?: CaptureModel | null;
  onSelectingChange: () => void;
}) {
  const { data } = useNote(noteId);
  if (!data) return <p>Loading…</p>;
  return (
    <Recordings note={data} localUpload={localUpload ?? null} onSelectingChange={onSelectingChange} />
  );
}

export function mount(
  fetchImpl: typeof fetch,
  queryClient: QueryClient = testQueryClient(),
  localUpload: CaptureModel | null = null,
) {
  const onSelectingChange = vi.fn();
  render(
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      <MemoryRouter>
        <Host noteId={NOTE.id} localUpload={localUpload} onSelectingChange={onSelectingChange} />
      </MemoryRouter>
    </TestProviders>,
  );
  return { queryClient, onSelectingChange };
}

/** The save path: what filename each `<a download>` was clicked with. */
export function captureSaves(): { names: string[]; blobs: Blob[]; restore: () => void } {
  const names: string[] = [];
  const blobs: Blob[] = [];
  URL.createObjectURL = vi.fn((blob: Blob | MediaSource) => {
    blobs.push(blob as Blob);
    return 'blob:mock-url';
  });
  URL.revokeObjectURL = vi.fn();
  const originalClick = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
    names.push(this.download);
  };
  return {
    names,
    blobs,
    restore: () => {
      HTMLAnchorElement.prototype.click = originalClick;
    },
  };
}

export const SEGMENTS_DOC = {
  version: 1,
  language: 'English',
  segments: [
    { start_ms: 0, end_ms: 3_000, text: ' Ridge tiles have slipped.' },
    { start_ms: 3_000, end_ms: 6_000, text: ' Ellis quoted nine hundred.' },
  ],
};

/**
 * The bucket answers the segments document for its URL and audio bytes for
 * the rest. `doc` is what it answers next, so a test can land a new one.
 */
export function artifactsStub(language = 'English'): { doc: typeof SEGMENTS_DOC } {
  const bucket = { doc: { ...SEGMENTS_DOC, language } };
  // jsdom implements no scrolling, and the transcript follows playback.
  Element.prototype.scrollIntoView = vi.fn();
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async (input) =>
      String(input).includes('/segments')
        ? new Response(JSON.stringify(bucket.doc), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
        : new Response('webm bytes', { status: 200, headers: { 'content-type': 'audio/webm' } }),
    ),
  );
  return bucket;
}

/** The roof note with a transcript behind its newest recording, and a presigned URL for it. */
export function withSegments(note: Partial<NoteDetailWire> = {}, captures = [{ ...CAPTURE, has_segments: true }, OLDER]) {
  return apiStub(
    { ...NOTE, ...note, captures },
    {
      segments: () =>
        json({
          url: 'https://chintan-content.s3.test/cap-1/segments.json?sig=1',
          expires_at: new Date(Date.now() + 900_000).toISOString(),
        }),
    },
  );
}

/** The rows of the recordings list — not the segments of a stage strip inside one. */
export async function recordingRows(): Promise<HTMLElement[]> {
  const region = await screen.findByRole('region', { name: 'Recordings' });
  return within(region)
    .getAllByRole('listitem')
    .filter((item) => item.matches('.recording, .recordings__filing'));
}
