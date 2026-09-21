import { useApi } from '@/api/ApiProvider.tsx';
import { newIdempotencyKey } from '@/api/client.ts';
import type { ChintanApi } from '@/api/endpoints.ts';
import { DownloadButton } from '@/components/DownloadButton.tsx';
import { config } from '@/config/env.ts';

import { SettingsCard } from './SettingsCard.tsx';

/**
 * How often a running export is asked about. The job is one walk of the
 * tenant's partition and prefix, seconds at today's corpus.
 */
export const EXPORT_POLL_MS = 1_500;

/**
 * After this the wait is reported as a failed download and the row can be
 * tapped again, which starts a fresh job. Two minutes is many times the
 * longest export seen; a job still not ready by then is not going to be.
 */
export const EXPORT_TIMEOUT_MS = 2 * 60_000;

/**
 * Starts an export, waits for it to be ready and fetches the file.
 *
 * `POST /v1/export` answers 202 with the job; `GET /v1/export/{id}` is polled
 * until it is `ready` with a presigned URL, or `failed`. The bytes are
 * fetched here and handed to `saveBlob` rather than the URL being opened,
 * for the reason `DownloadButton` gives: a cross-origin presigned URL with
 * no `Content-Disposition` displays in the tab instead of saving.
 */
export async function exportBlob(
  api: ChintanApi,
  {
    fetchImpl = (...args: Parameters<typeof fetch>) => globalThis.fetch(...args),
    sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)),
    now = Date.now,
  }: {
    fetchImpl?: typeof fetch;
    sleep?: (ms: number) => Promise<void>;
    now?: () => number;
  } = {},
): Promise<Blob> {
  const started = now();
  let job = await api.startExport(newIdempotencyKey());
  while (job.status === 'pending' || job.status === 'running') {
    if (now() - started > EXPORT_TIMEOUT_MS) throw new Error('The export did not finish in time');
    await sleep(EXPORT_POLL_MS);
    job = await api.getExport(job.id);
  }
  if (job.status !== 'ready' || !job.url) throw new Error('The export failed');
  const response = await fetchImpl(job.url);
  if (!response.ok) throw new Error(`The export could not be fetched (${String(response.status)})`);
  return response.blob();
}

/**
 * `chintan-notes-2026-09-21.json`: the app's name, so two instances' files
 * tell apart, and the day where the person is — the UTC day named a file
 * exported late in the evening for tomorrow.
 */
export function exportFilename(now: Date = new Date()): string {
  const slug = config.appName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()]
    .map((part) => String(part).padStart(2, '0'))
    .join('-');
  return `${slug}-notes-${day}.json`;
}

/**
 * "Your data", on You.
 *
 * Export existed for the API and the operator (`chintanctl export`) and not
 * for the person whose notes they are; a per-note markdown download meant
 * portability one note at a time (round-3 T30). One row: everything as one
 * JSON file, through the same button and save path as every other download
 * in the app. "Delete my account" is not here on purpose: it needs a backend
 * route the owner has not decided on, and `chintanctl erase` is the path.
 */
export function ExportCard() {
  const api = useApi();
  return (
    <SettingsCard
      title="Your data"
      lead="Everything you have recorded and written, as one file you keep."
    >
      <DownloadButton
        className="you-row you-row--action"
        label="Download my notes (JSON)"
        // Most of the wait is the server building the file, before any download starts.
        busyLabel="Preparing your file…"
        blob={() => exportBlob(api)}
        filename={() => exportFilename()}
      />
    </SettingsCard>
  );
}
