import { expect, test } from './fixtures.ts';

/**
 * The manifest and the icons it names, served by the real build.
 *
 * Reported from the live app: a deep link asked for `/chintan/icon-192.png` and
 * got a 404, while `/chintan/dev/icon-192.png` was 200. Everything with a URL in
 * it — the manifest's fields and the document's own icon links — was relative,
 * so what resolved depended on how deep the current path happened to be.
 *
 * These fetch what the browser would fetch. Asserting that icons are declared
 * would pass against the bug; asserting that the declared URL answers 200 from
 * a deep link is the thing that failed.
 */

test('every icon the manifest names actually exists', async ({ page, request }) => {
  await page.goto('/');

  const manifestHref = await page
    .locator('link[rel="manifest"]')
    .getAttribute('href');
  expect(manifestHref).toBeTruthy();

  const manifest = (await (await request.get(manifestHref as string)).json()) as {
    start_url: string;
    scope: string;
    icons: { src: string }[];
    shortcuts: { url: string; icons: { src: string }[] }[];
  };

  // Nothing left to resolve: every URL states its own full path.
  for (const url of [
    manifest.start_url,
    manifest.scope,
    ...manifest.icons.map((icon) => icon.src),
    ...manifest.shortcuts.map((shortcut) => shortcut.url),
  ]) {
    expect(url.startsWith('/'), `${url} is relative`).toBe(true);
  }

  for (const icon of manifest.icons) {
    const response = await request.get(icon.src);
    expect(response.status(), `${icon.src} does not exist`).toBe(200);
  }
});

test('the document icon links resolve from a deep link, not from its directory', async ({
  page,
  request,
}) => {
  /*
   * The actual failure. `href="icon-192.png"` resolves against the *document's*
   * directory, so on `/notes/roof-repair` the browser asked for
   * `/notes/icon-192.png`. The icons only ever loaded from paths that happened
   * to sit at the right depth.
   */
  await page.goto('/notes/roof-repair');

  const hrefs = await page
    .locator('link[rel="apple-touch-icon"], link[rel="icon"]')
    .evaluateAll((links) =>
      links.map((link) => (link as HTMLLinkElement).getAttribute('href') ?? ''),
    );

  expect(hrefs.length).toBeGreaterThan(0);
  for (const href of hrefs) {
    expect(href.startsWith('/'), `${href} is relative to the document`).toBe(true);
    expect((await request.get(href)).status(), `${href} does not exist`).toBe(200);
  }
});
