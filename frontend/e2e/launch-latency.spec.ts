import process from 'node:process';

import type { BrowserContext, CDPSession, Page } from '@playwright/test';

import { expect, freshState, installApi, test } from './fixtures.ts';

/**
 * How long a launch takes: Home, and the manifest's "Record a thought"
 * shortcut straight to `/capture`.
 *
 * A measurement, not a regression fence: it prints numbers and asserts nothing
 * about them, because a threshold on a throttled synthetic device would fail
 * on a busy CI runner for reasons that have nothing to do with the app. Run it
 * by hand around a change that claims to help:
 *
 *     LAUNCH_PERF=1 bun run e2e -- --project=chromium launch-latency
 *
 * The conditions are the ones QA measured the shortcut under — Chrome
 * DevTools' "Fast 3G" preset and a 4× CPU slowdown, applied through CDP, on a
 * 360 px phone. Each route is measured twice: with the service worker *cold*
 * (blocked, so every byte comes over the throttled link: a first visit, or a
 * client whose precache was evicted) and *warm* (installed by an unthrottled
 * visit first, so the shell and every chunk are answered from the precache
 * and only the API crosses the link: the installed app's everyday launch).
 *
 * Reported per run, all on the page's own clock (`performance.now()`), so
 * the numbers are not padded by Playwright's polling:
 *
 *   fcp         first contentful paint
 *   dcl         DOMContentLoaded — the HTML and the module graph are parsed
 *   interactive Home: the record disc is on screen and enabled; `/capture`:
 *               the screen has drawn its first control (Cancel, while the
 *               microphone is being asked for)
 *   recording   `/capture` only: `.capture__state` reads "Recording" — the
 *               stream is live and MediaRecorder has started
 *   firstApi    the first request to the API leaves the page
 *   apiBefore   how many API requests were sent before `recording` (or
 *               `interactive` on Home)
 *   js          kilobytes of script the page fetched (cold only: warm reads
 *               them from the precache, where the size is not what is paid)
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

/** A small phone, the layout matrix's narrowest common viewport. */
const PHONE = { width: 360, height: 780 };

async function throttle(page: Page): Promise<CDPSession> {
  const client = await page.context().newCDPSession(page);
  await client.send('Network.enable');
  // Every run is a cold load of the HTTP cache. Without this the second
  // iteration onwards is served from it — 37 ms to DOMContentLoaded on
  // "Fast 3G" — and the throttle measures nothing. The service worker's
  // precache is Cache Storage, not the HTTP cache, so the warm cells keep it.
  await client.send('Network.setCacheDisabled', { cacheDisabled: true });
  await client.send('Network.emulateNetworkConditions', FAST_3G);
  await client.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  return client;
}

/**
 * Installs the service worker with an unthrottled visit to Home, so the next
 * page in this context is served from the precache from its first byte.
 * `ready` resolves once the worker is active, and precaching happens during
 * its install, so an active worker is a full precache.
 */
async function warmServiceWorker(context: BrowserContext): Promise<void> {
  const page = await context.newPage();
  await installApi(page, freshState());
  await page.goto('/');
  await page.evaluate(() => navigator.serviceWorker.ready);
  await expect
    .poll(() => page.evaluate(() => navigator.serviceWorker.controller !== null))
    .toBe(true);
  await page.close();
}

interface Sample {
  fcp: number;
  dcl: number;
  interactive: number;
  recording: number | null;
  firstApi: number | null;
  apiBefore: number;
  js: number;
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0 ? ((sorted[mid - 1] ?? 0) + (sorted[mid] ?? 0)) / 2 : (sorted[mid] ?? 0);
}

const CELLS = [
  { route: '/', sw: 'cold' },
  { route: '/', sw: 'warm' },
  { route: '/capture', sw: 'cold' },
  { route: '/capture', sw: 'warm' },
] as const;

async function measure(page: Page, route: string): Promise<Sample> {
  // Stamps each milestone the moment the DOM shows it, on the page's clock.
  await page.addInitScript(() => {
    const stamps: Record<string, number> = {};
    (window as unknown as { __stamps: Record<string, number> }).__stamps = stamps;
    const milestones: Record<string, () => boolean> = {
      interactive: () => {
        const disc = document.querySelector<HTMLButtonElement>('.record-button, .capture__control');
        return disc !== null && !disc.disabled;
      },
      recording: () => document.querySelector('.capture__state')?.textContent?.trim() === 'Recording',
    };
    const check = (): void => {
      for (const [name, reached] of Object.entries(milestones)) {
        if (!(name in stamps) && reached()) stamps[name] = performance.now();
      }
    };
    document.addEventListener('DOMContentLoaded', () => {
      check();
      new MutationObserver(check).observe(document.documentElement, {
        childList: true,
        subtree: true,
        characterData: true,
        attributes: true,
      });
    });
  });

  await page.goto(route, { waitUntil: 'commit' });
  if (route === '/capture') {
    await expect(page.locator('.capture__state')).toHaveText('Recording', { timeout: 60_000 });
  } else {
    await expect(page.getByRole('button', { name: 'Record', exact: true })).toBeEnabled({
      timeout: 60_000,
    });
  }

  return page.evaluate((isCapture) => {
    const [entry] = performance.getEntriesByType('navigation') as PerformanceNavigationTiming[];
    const resources = performance.getEntriesByType('resource') as PerformanceResourceTiming[];
    const api = resources.filter((r) => r.name.includes('/api/')).map((r) => r.startTime);
    const paint = performance.getEntriesByType('paint').find((p) => p.name === 'first-contentful-paint');
    const stamps = (window as unknown as { __stamps: Record<string, number> }).__stamps;
    const interactive = Math.round(stamps['interactive'] ?? 0);
    const recording = isCapture ? Math.round(stamps['recording'] ?? 0) : null;
    const settled = recording ?? interactive;
    return {
      fcp: Math.round(paint?.startTime ?? 0),
      dcl: Math.round(entry?.domContentLoadedEventEnd ?? 0),
      interactive,
      recording,
      firstApi: api.length > 0 ? Math.round(Math.min(...api)) : null,
      apiBefore: api.filter((at) => at <= settled).length,
      js: Math.round(
        resources.filter((r) => r.name.endsWith('.js')).reduce((sum, r) => sum + r.encodedBodySize, 0) /
          1000,
      ),
    };
  }, route === '/capture');
}

test.describe('launch latency', () => {
  test.skip(!ENABLED, 'set LAUNCH_PERF=1 to measure');
  test.setTimeout(240_000);

  for (const cell of CELLS) {
    test(`${cell.route} with the service worker ${cell.sw}, Fast 3G, 4x CPU, 360 px`, async ({
      browser,
    }) => {
      const samples: Sample[] = [];

      for (let run = 0; run < RUNS; run += 1) {
        /*
         * A new browser context per run: an empty HTTP cache, no storage, no
         * worker, and a CDP session of its own. The emulation did not survive
         * a navigation away and back on one page — every run after the first
         * came in at ~30 ms to DOMContentLoaded, which is the cache, not the
         * network.
         */
        const context = await browser.newContext({ permissions: ['microphone'], viewport: PHONE });
        if (cell.sw === 'warm') await warmServiceWorker(context);
        const page = await context.newPage();
        await installApi(page, freshState());
        if (cell.sw === 'cold') {
          await page.route('**/sw.js', (route) => route.fulfill({ status: 404, body: '' }));
        }
        await throttle(page);
        samples.push(await measure(page, cell.route));
        await context.close();
      }

      const ms = (value: number | null) => `${String(value ?? '—').padStart(5)} ms`;
      const line = (s: Sample) =>
        `fcp ${ms(s.fcp)} · dcl ${ms(s.dcl)} · interactive ${ms(s.interactive)}` +
        (cell.route === '/capture' ? ` · recording ${ms(s.recording)}` : '') +
        ` · firstApi ${ms(s.firstApi)} · apiBefore ${String(s.apiBefore)}` +
        (cell.sw === 'cold' ? ` · js ${String(s.js)} kB` : '');

      console.log(`\nlaunch latency — ${cell.route}, service worker ${cell.sw}, Fast 3G, 4x CPU, 360 px`);
      samples.forEach((sample, index) => {
        console.log(`  run ${String(index + 1)}: ${line(sample)}`);
      });
      const col = (pick: (s: Sample) => number | null) =>
        median(samples.flatMap((s) => (pick(s) === null ? [] : [pick(s) as number])));
      console.log(
        `  median: ${line({
          fcp: col((s) => s.fcp),
          dcl: col((s) => s.dcl),
          interactive: col((s) => s.interactive),
          recording: cell.route === '/capture' ? col((s) => s.recording) : null,
          firstApi: samples.some((s) => s.firstApi !== null) ? col((s) => s.firstApi) : null,
          apiBefore: col((s) => s.apiBefore),
          js: col((s) => s.js),
        })}`,
      );
    });
  }
});
