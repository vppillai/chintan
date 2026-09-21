import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
import type { ExportJobWire } from '@/api/schema.ts';
import { TestProviders } from '@/test/providers.tsx';

import { EXPORT_POLL_MS, ExportCard, exportBlob, exportFilename } from './ExportCard.tsx';

/** An API whose export job answers the given statuses in turn on each poll. */
function apiWith(polls: ExportJobWire['status'][], url = 'https://s3.invalid/export.json') {
  const starts: (string | undefined)[] = [];
  const gets: string[] = [];
  const api = {
    startExport: async (key?: string): Promise<ExportJobWire> => {
      starts.push(key);
      return { id: 'job-1', status: 'pending' };
    },
    getExport: async (id: string): Promise<ExportJobWire> => {
      gets.push(id);
      const status = polls[Math.min(gets.length, polls.length) - 1] ?? 'ready';
      return status === 'ready' ? { id, status, url, bytes: 12 } : { id, status };
    },
  } as unknown as ChintanApi;
  return { api, starts, gets };
}

describe('exporting the notes', () => {
  it('starts the job once, polls it until ready, and fetches the file from the presigned URL', async () => {
    const { api, starts, gets } = apiWith(['running', 'running', 'ready']);
    const slept: number[] = [];
    const fetched: string[] = [];
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      fetched.push(String(input));
      return new Response('{"notes":[]}', { status: 200 });
    }) as unknown as typeof fetch;

    const blob = await exportBlob(api, {
      fetchImpl,
      sleep: async (ms) => {
        slept.push(ms);
      },
    });

    expect(await blob.text()).toBe('{"notes":[]}');
    // One POST under an idempotency key, three polls, one fetch of the file.
    expect(starts).toHaveLength(1);
    expect(starts[0]).toMatch(/\S/);
    expect(gets).toEqual(['job-1', 'job-1', 'job-1']);
    expect(slept).toEqual([EXPORT_POLL_MS, EXPORT_POLL_MS, EXPORT_POLL_MS]);
    expect(fetched).toEqual(['https://s3.invalid/export.json']);
  });

  it('fails plainly when the job fails, and when the wait runs out', async () => {
    const failed = apiWith(['failed']);
    await expect(exportBlob(failed.api, { sleep: async () => undefined })).rejects.toThrow(
      /export failed/i,
    );

    // A clock that jumps past the ceiling on the first poll.
    let clock = 0;
    const stuck = apiWith(['running', 'running', 'running']);
    await expect(
      exportBlob(stuck.api, {
        sleep: async () => undefined,
        now: () => {
          clock += 61_000;
          return clock;
        },
      }),
    ).rejects.toThrow(/did not finish in time/i);
  });

  it('names the file after the app and the day', () => {
    expect(exportFilename(new Date('2026-09-21T10:00:00Z'))).toBe('chintan-notes-2026-09-21.json');
  });

  it('is one row on You, through the same download button as every other save', () => {
    render(
      <TestProviders>
        <ExportCard />
      </TestProviders>,
    );
    expect(screen.getByRole('region', { name: 'Your data' })).toBeInTheDocument();
    const row = screen.getByRole('button', { name: 'Download my notes (JSON)' });
    expect(row).toHaveClass('you-row');
  });
});
