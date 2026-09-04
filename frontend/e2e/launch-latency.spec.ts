import process from 'node:process';

import type { CDPSession, Page } from '@playwright/test';

import { expect, freshState, installApi, test } from './fixtures.ts';

/**
 * How long the capture shortcut takes to reach a live microphone.
 *
 * A measurement, not a regression fence: it prints numbers and asserts nothing
 * about them, because a threshold on a throttled synthetic device would fail
 * on a busy CI runner for reasons that have nothing to do with the app. Run it
 * by hand around a change that claims to help:
 *
 *     LAUNCH_PERF=1 bun run e2e -- --project=chromium launch-latency
 *
 * The conditions are the ones QA measured B14 under — Chrome DevTools' "Fast
 * 3G" preset and a 4× CPU slowdown — applied through CDP. The service worker
 * is blocked so every run is a cold load of the shell and nothing is served
 * from a precache the previous iteration warmed.
 *
 * Reported per run, all on the page's own clock (`performance.now()`), so
 * the numbers are not padded by Playwright's polling:
 *
 *   dcl        DOMContentLoaded — the HTML and the module graph are parsed
 *   firstApi   the first request to the API leaves the page
 *   recording  `.capture__state` reads "Recording": the stream is live
 *   apiBefore  how many API requests were sent before that point
 */

const RUNS = Number(process.env['LAUNCH_PERF_RUNS'] ?? 3);
const ENABLED = process.env['LAUNCH_PERF'] === '1';

/** Chrome DevTools "Fast 3G", with the 0.9 factor DevTools applies. */
const FAST_3G = {
  offline: false,
  latency: 562.5,
  downloadThroughput: ((1.6 * 1024 * 1024) / 8) * 0.9,
  uploadThroughput: ((750 * 1024) / 8) * 0.9,
};

async function throttle(page: Page): Promise<CDPSession> {
  const client = await page.context().newCDPSession(page);
  await client.send('Network.enable');
  // Every run is a cold load. Without this the second iteration onwards is
  // served from the HTTP cache — 37 ms to DOMContentLoaded on "Fast 3G" — and
  // the throttle measures nothing.
  await client.send('Network.setCacheDisabled', { cacheDisabled: true });
  await client.send('Network.emulateNetworkConditions', FAST_3G);
  await client.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  return client;
}

interface Sample {
  dcl: number;
  firstApi: number | null;
  recording: number;
  apiBefore: number;
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0 ? ((sorted[mid - 1] ?? 0) + (sorted[mid] ?? 0)) / 2 : (sorted[mid] ?? 0);
}

test.describe('launch latency of /capture', () => {
  test.skip(!ENABLED, 'set LAUNCH_PERF=1 to measure');
  test.setTimeout(120_000);

  test('cold load of /capture on Fast 3G with a 4x CPU slowdown', async ({ browser }) => {
    const samples: Sample[] = [];

    for (let run = 0; run < RUNS; run += 1) {
      /*
       * A new browser context per run: an empty HTTP cache, no storage, and a
       * CDP session of its own. The emulation did not survive a navigation
       * away and back on one page — every run after the first came in at
       * ~30 ms to DOMContentLoaded, which is the cache, not the network.
       */
      const context = await browser.newContext({ permissions: ['microphone'] });
      const page = await context.newPage();
      await installApi(page, freshState());
      await page.route('**/sw.js', (route) => route.fulfill({ status: 404, body: '' }));
      await throttle(page);

      // Stamps the moment the screen says "Recording", on the page's clock.
      await page.addInitScript(() => {
        const stamp = (): boolean => {
          const state = document.querySelector('.capture__state');
          if (state?.textContent?.trim() !== 'Recording') return false;
          (window as unknown as { __recordingAt: number }).__recordingAt = performance.now();
          return true;
        };
        const observer = new MutationObserver(() => {
          if (stamp()) observer.disconnect();
        });
        document.addEventListener('DOMContentLoaded', () => {
          if (!stamp()) {
            observer.observe(document.documentElement, {
              childList: true,
              subtree: true,
              characterData: true,
            });
          }
        });
      });

      await page.goto('/capture', { waitUntil: 'commit' });
      await expect(page.locator('.capture__state')).toHaveText('Recording', { timeout: 60_000 });

      const timing = await page.evaluate(() => {
        const [entry] = performance.getEntriesByType('navigation') as PerformanceNavigationTiming[];
        const api = (performance.getEntriesByType('resource') as PerformanceResourceTiming[])
          .filter((resource) => resource.name.includes('/api/'))
          .map((resource) => resource.startTime);
        return {
          dcl: Math.round(entry?.domContentLoadedEventEnd ?? 0),
          api,
          recordingAt: (window as unknown as { __recordingAt?: number }).__recordingAt ?? null,
        };
      });
      const recordingAt = Math.round(timing.recordingAt ?? 0);

      samples.push({
        dcl: timing.dcl,
        firstApi: timing.api.length > 0 ? Math.round(Math.min(...timing.api)) : null,
        recording: recordingAt,
        apiBefore: timing.api.filter((at) => at <= recordingAt).length,
      });

      await context.close();
    }

    const line = (sample: Sample) =>
      `dcl ${String(sample.dcl).padStart(5)} ms · firstApi ${String(sample.firstApi ?? '—').padStart(5)} ms · recording ${String(sample.recording).padStart(5)} ms · apiBefore ${String(sample.apiBefore)}`;

    console.log('\nlaunch latency — /capture, Fast 3G, 4x CPU, no service worker');
    samples.forEach((sample, index) => {
      console.log(`  run ${String(index + 1)}: ${line(sample)}`);
    });
    console.log(
      `  median: ${line({
        dcl: median(samples.map((sample) => sample.dcl)),
        firstApi: median(samples.flatMap((sample) => (sample.firstApi === null ? [] : [sample.firstApi]))),
        recording: median(samples.map((sample) => sample.recording)),
        apiBefore: median(samples.map((sample) => sample.apiBefore)),
      })}`,
    );
  });
});
