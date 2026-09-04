import { test, type Page, type TestInfo } from '@playwright/test';

import { note, record, shot, sleep } from './helpers.ts';

/** Chrome DevTools' "Fast 3G" preset. */
const FAST_3G = {
  offline: false,
  latency: 562.5,
  downloadThroughput: ((1.6 * 1024 * 1024) / 8) * 0.9,
  uploadThroughput: ((750 * 1024) / 8) * 0.9,
};

async function measure(
  page: Page,
  info: TestInfo,
  path: string,
  readySelector: string,
  readyText: RegExp | null,
  label: string,
  throttle: boolean,
): Promise<void> {
  const rec = record(page);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Network.enable');
  if (throttle) {
    await cdp.send('Network.emulateNetworkConditions', FAST_3G);
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  }
  const t0 = Date.now();
  await page.goto(path, { waitUntil: 'commit' });
  const el = page.locator(readySelector).first();
  if (readyText) await el.filter({ hasText: readyText }).first().waitFor({ timeout: 120_000 });
  else await el.waitFor({ timeout: 120_000 });
  const interactive = Date.now() - t0;
  await sleep(1_500);
  const timing = await page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined;
    const paints = Object.fromEntries(performance.getEntriesByType('paint').map((p) => [p.name, Math.round(p.startTime)]));
    const lcp = (performance.getEntriesByType('largest-contentful-paint') as PerformanceEntry[]).map((e) => Math.round(e.startTime));
    const resources = (performance.getEntriesByType('resource') as PerformanceResourceTiming[]).map((r) => ({
      name: r.name.replace(/^https?:\/\/[^/]+/, ''),
      type: r.initiatorType,
      transfer: r.transferSize,
      decoded: r.decodedBodySize,
      ms: Math.round(r.duration),
    }));
    return {
      responseStart: nav ? Math.round(nav.responseStart) : null,
      domContentLoaded: nav ? Math.round(nav.domContentLoadedEventEnd) : null,
      load: nav ? Math.round(nav.loadEventEnd) : null,
      paints,
      lcp,
      resources,
    };
  });
  const metrics = await cdp.send('Performance.enable').then(() => cdp.send('Performance.getMetrics'));
  const pick = (n: string): number => Math.round((metrics.metrics.find((m) => m.name === n)?.value ?? 0) * 1000);
  const js = timing.resources.filter((r) => r.name.endsWith('.js'));
  const css = timing.resources.filter((r) => r.name.endsWith('.css'));
  const api = timing.resources.filter((r) => r.name.includes('/v1/'));
  const total = timing.resources.reduce((s, r) => s + r.transfer, 0);
  note(info, `[${label}] ${path}: interactive (${readySelector}${readyText ? ` ~ ${readyText}` : ''}) at ${interactive} ms after navigation start; responseStart ${timing.responseStart} ms; DCL ${timing.domContentLoaded} ms; load ${timing.load} ms; paints ${JSON.stringify(timing.paints)}; LCP ${JSON.stringify(timing.lcp)}; ScriptDuration ${pick('ScriptDuration')} ms; TaskDuration ${pick('TaskDuration')} ms; JSHeapUsed ${Math.round((metrics.metrics.find((m) => m.name === 'JSHeapUsedSize')?.value ?? 0) / 1024)} KB`);
  note(info, `[${label}] resources: ${timing.resources.length} total, ${Math.round(total / 1024)} KB transferred; JS ${js.map((r) => `${r.name.split('/').pop()} ${Math.round(r.transfer / 1024)}KB gz/${Math.round(r.decoded / 1024)}KB (${r.ms}ms)`).join(', ')}; CSS ${css.map((r) => `${r.name.split('/').pop()} ${Math.round(r.transfer / 1024)}KB/${Math.round(r.decoded / 1024)}KB`).join(', ')}; API ${api.map((r) => `${r.name.split('?')[0]} ${r.ms}ms`).join(', ')}`);
  const other = timing.resources.filter((r) => !js.includes(r) && !css.includes(r) && !api.includes(r));
  note(info, `[${label}] other fetches: ${other.map((r) => `${r.name.split('/').slice(-1)[0]?.slice(0, 40)} ${Math.round(r.transfer / 1024)}KB ${r.ms}ms`).join(', ')}`);
  await shot(page, info, `perf-${label}`);
  rec.dump(info, `perf-${label}`);
  if (throttle) {
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });
  }
}

test.describe('performance (cold contexts)', () => {
  test('library / on Fast 3G + 4x CPU', async ({ page }, info) => {
    await measure(page, info, './', '.note-row', null, 'library-fast3g', true);
  });
  test('capture shortcut /capture on Fast 3G + 4x CPU', async ({ page }, info) => {
    await measure(page, info, './capture', '.capture__state', /Recording/, 'capture-fast3g', true);
    await page.getByRole('button', { name: 'Cancel' }).click().catch(() => undefined);
  });
  test('library / unthrottled baseline', async ({ page }, info) => {
    await measure(page, info, './', '.note-row', null, 'library-baseline', false);
  });
  test('capture shortcut unthrottled baseline', async ({ page }, info) => {
    await measure(page, info, './capture', '.capture__state', /Recording/, 'capture-baseline', false);
    await page.getByRole('button', { name: 'Cancel' }).click().catch(() => undefined);
  });
});
