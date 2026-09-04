import process from 'node:process';

import type { Page } from '@playwright/test';

import { expect, test, type ApiState } from './fixtures.ts';

/**
 * Layout regressions, measured rather than eyeballed.
 *
 * Every assertion here exists because the running app failed it. The owner
 * reported "partially overlapping" UI; the audit in docs/ui-audit.md found four
 * distinct causes, and this spec is the fence around each of them:
 *
 *   1. the shell's chrome (the tab bar) leaving the viewport,
 *   2. the live waveform canvas escaping its panel onto the capture controls,
 *   3. the update prompt floating over the bottom bar and the record button,
 *   4. the confirm dialog running off a short viewport with no way to scroll.
 *
 * The whole matrix runs in both themes because Nocturne and Ink & Paper differ
 * in more than colour — shadows and border weights change box sizes — and a
 * layout suite that only proves one of them is half a suite.
 */

interface Viewport {
  name: string;
  width: number;
  height: number;
}

/**
 * The devices the product actually has to survive. 320×568 is the narrowest
 * phone still in use; 844×390 is a phone in a car mount, which the manifest
 * unlocks on purpose and which is therefore a supported case, not an edge one.
 */
const VIEWPORTS: readonly Viewport[] = [
  { name: '320x568 iphone-se', width: 320, height: 568 },
  { name: '375x667 iphone-8', width: 375, height: 667 },
  { name: '390x844 iphone-14', width: 390, height: 844 },
  { name: '393x873 pixel', width: 393, height: 873 },
  { name: '412x915 pixel-7', width: 412, height: 915 },
  { name: '844x390 phone-landscape', width: 844, height: 390 },
  { name: '768x1024 ipad-portrait', width: 768, height: 1024 },
  { name: '1024x768 ipad-landscape', width: 1024, height: 768 },
  { name: '1280x800 laptop', width: 1280, height: 800 },
  { name: '1440x900 laptop-large', width: 1440, height: 900 },
  { name: '1920x1080 desktop', width: 1920, height: 1080 },
  { name: '2560x1080 ultrawide', width: 2560, height: 1080 },
];

const THEMES = ['ink', 'nocturne'] as const;

/** Screens that must hold up at every size, in both themes. */
const ROUTES = [
  { path: '/', label: 'library' },
  { path: '/notes/roof-repair', label: 'note detail' },
  { path: '/?view=archived', label: 'archive' },
  { path: '/?q=roof', label: 'search with results' },
  { path: '/?q=nothingmatchesthis', label: 'search empty' },
  { path: '/settings', label: 'settings' },
];

/**
 * Content long enough to push every container past its natural size: a title
 * that does not fit on one line at any width, an untruncated 56-character tag,
 * twenty tags, and a body of several thousand words.
 */
function stressTheFixture(api: ApiState): void {
  const note = api.notes['roof-repair'];
  if (!note) return;
  const body = Array.from(
    { length: 60 },
    (_, index) =>
      `Paragraph ${index + 1}. Ridge tiles on the south slope have slipped and the flashing around the chimney needs replacing before the autumn rain arrives in earnest.`,
  ).join('\n\n');

  note.title =
    'The completely unreasonable but entirely plausible title of a note that someone dictated in one breath while driving';
  note.tags = [
    'supercalifragilisticexpialidocious-household-maintenance',
    ...Array.from({ length: 20 }, (_, index) => `tag-number-${index + 1}`),
  ];
  note.body = body;
  note.snippet = body.slice(0, 400);
}

async function useTheme(page: Page, theme: string): Promise<void> {
  await page.addInitScript((value) => {
    localStorage.setItem('chintan.theme', value);
  }, theme);
}

/**
 * Layout has nothing to do with the service worker, and this file opens ~150
 * pages. Letting each of them install a worker and precache 437KB turns a fast
 * suite into a flaky one — the preview server is a single process, and pages
 * start losing their first paint to the stampede. The worker's own behaviour is
 * covered by offline.spec.ts, which is where it belongs.
 */
async function withoutServiceWorker(page: Page): Promise<void> {
  await page.route('**/sw.js', (route) => route.fulfill({ status: 404, body: '' }));
}

/**
 * The numbers below say whether a layout is broken; only a picture says whether
 * it is any good. `LAYOUT_SHOTS=1 bun run e2e layout` writes the whole matrix to
 * e2e/__screenshots__/sweep/ for a human to page through — about 280 images, so
 * it is opt-in and the directory is gitignored. The curated before/after pairs
 * that docs/ui-audit.md cites are committed alongside it.
 */
const SHOTS = process.env['LAYOUT_SHOTS'] === '1';

async function shoot(page: Page, name: string): Promise<void> {
  if (!SHOTS) return;
  await page.screenshot({ path: `e2e/__screenshots__/sweep/${name}.png` });
}

/**
 * One trip into the page per screen. Returning structured findings rather than
 * asserting inside `evaluate` keeps the failure message on the Node side, where
 * it can name the offending selector and the numbers that made it fail.
 */
interface LayoutReport {
  /** The document scrolls sideways — the classic "broken on mobile" symptom. */
  documentOverflow: { scrollWidth: number; clientWidth: number } | null;
  /** Visible elements poking out past the viewport's inline edges. */
  bleeding: { selector: string; left: number; right: number }[];
  /** Shell chrome that is not fully on screen. */
  chromeOffscreen: { selector: string; top: number; bottom: number; viewport: number }[];
  /** Pairs that overlap and must not. */
  overlaps: { a: string; b: string; width: number; height: number; where: string }[];
  /** Containers that scroll sideways — a hidden form of the same bug. */
  sidewaysScroll: { selector: string; scrollWidth: number; clientWidth: number }[];
}

async function inspect(page: Page): Promise<LayoutReport> {
  return page.evaluate(() => {
    const root = document.documentElement;
    const viewportWidth = root.clientWidth;
    const viewportHeight = root.clientHeight;

    const describe = (element: Element): string => {
      const parts: string[] = [];
      let cursor: Element | null = element;
      while (cursor && parts.length < 3) {
        const classes =
          typeof cursor.className === 'string' && cursor.className.trim()
            ? `.${cursor.className.trim().split(/\s+/).slice(0, 2).join('.')}`
            : '';
        parts.unshift(`${cursor.tagName.toLowerCase()}${classes}`);
        cursor = cursor.parentElement;
      }
      return parts.join(' > ');
    };

    /*
     * The rect a user can actually see. An element scrolled inside
     * `.app__main` still reports a bounding box that runs under the bottom bar,
     * but the scroll container clips it and nothing is painted there — so
     * comparing raw boxes reports an overlap that does not exist. Intersecting
     * with every clipping ancestor is what makes the overlap check mean
     * "covers" rather than "shares coordinates with".
     */
    interface Rect {
      top: number;
      right: number;
      bottom: number;
      left: number;
    }

    const clippedRect = (element: Element): Rect | null => {
      const box = element.getBoundingClientRect();
      const rect: Rect = { top: box.top, right: box.right, bottom: box.bottom, left: box.left };
      let ancestor = element.parentElement;
      while (ancestor) {
        const style = getComputedStyle(ancestor);
        if (style.overflowX !== 'visible' || style.overflowY !== 'visible') {
          const clip = ancestor.getBoundingClientRect();
          rect.top = Math.max(rect.top, clip.top);
          rect.left = Math.max(rect.left, clip.left);
          rect.right = Math.min(rect.right, clip.right);
          rect.bottom = Math.min(rect.bottom, clip.bottom);
        }
        ancestor = ancestor.parentElement;
      }
      // Scrolled entirely out of its container: nothing of it is painted, so it
      // is not on screen and cannot be overlapping anything. Returning a DOMRect
      // here would be a trap — DOMRect normalises a negative height by swapping
      // its edges, which turns "invisible" into a plausible-looking box.
      if (rect.bottom <= rect.top || rect.right <= rect.left) return null;
      return rect;
    };

    const isVisible = (element: Element): boolean => {
      const style = getComputedStyle(element);
      if (style.display === 'none' || style.visibility === 'hidden') return false;
      if (Number(style.opacity) === 0) return false;
      if (element.classList.contains('visually-hidden')) return false;
      const box = element.getBoundingClientRect();
      return box.width > 0 && box.height > 0;
    };

    const bleeding: LayoutReport['bleeding'] = [];
    for (const element of document.querySelectorAll('body *')) {
      if (!isVisible(element)) continue;
      const box = clippedRect(element);
      if (box && (box.right > viewportWidth + 1 || box.left < -1)) {
        bleeding.push({
          selector: describe(element),
          left: Math.round(box.left),
          right: Math.round(box.right),
        });
      }
    }

    /*
     * The tab bar is the app's only navigation and it carries the record
     * button. It is a grid row in normal flow by design, which is exactly why
     * it must never be pushed past the fold: unlike a fixed bar, nothing brings
     * it back.
     */
    const chromeOffscreen: LayoutReport['chromeOffscreen'] = [];
    for (const selector of ['.tab-bar']) {
      const element = document.querySelector(selector);
      if (!element || !isVisible(element)) continue;
      const box = element.getBoundingClientRect();
      if (box.bottom > viewportHeight + 1 || box.top < -1) {
        chromeOffscreen.push({
          selector,
          top: Math.round(box.top),
          bottom: Math.round(box.bottom),
          viewport: viewportHeight,
        });
      }
    }

    /*
     * Pairs that share the screen and must stay out of each other's way. The
     * shell puts each of these in its own grid row precisely so none of them
     * can cover another; an intersection here means something has floated free
     * of the row it was given.
     */
    const CHROME = ['.tab-bar'];
    const LAYERS = [
      '.app__main',
      '.filing',
      '.resume-prompt',
      '.update-prompt',
      '.record-button',
      '.note-row',
      '.load-more',
    ];
    const overlaps: LayoutReport['overlaps'] = [];
    for (const chromeSelector of CHROME) {
      const chrome = document.querySelector(chromeSelector);
      if (!chrome || !isVisible(chrome)) continue;
      const chromeBox = clippedRect(chrome);
      if (!chromeBox) continue;
      for (const layerSelector of LAYERS) {
        for (const layer of document.querySelectorAll(layerSelector)) {
          if (!isVisible(layer)) continue;
          if (chrome.contains(layer) || layer.contains(chrome)) continue;
          const layerBox = clippedRect(layer);
          if (!layerBox) continue;
          const width =
            Math.min(chromeBox.right, layerBox.right) - Math.max(chromeBox.left, layerBox.left);
          const height =
            Math.min(chromeBox.bottom, layerBox.bottom) - Math.max(chromeBox.top, layerBox.top);
          if (width > 1 && height > 1) {
            overlaps.push({
              a: chromeSelector,
              b: describe(layer),
              width: Math.round(width),
              height: Math.round(height),
              // Kept in the failure message: knowing *where* the two boxes are
              // is the difference between a five-minute fix and an afternoon.
              where:
                `chrome ${Math.round(chromeBox.top)}–${Math.round(chromeBox.bottom)}` +
                ` vs layer ${Math.round(layerBox.top)}–${Math.round(layerBox.bottom)}`,
            });
          }
        }
      }
    }

    /*
     * A scroll container hiding the overflow is not a fix. Nothing in this app
     * is meant to scroll horizontally — with one exception, the library's chip
     * row, which is a single sideways-scrolling line by design — so any other
     * container that can is a layout failure that the document-level check
     * would never see.
     */
    const sidewaysScroll: LayoutReport['sidewaysScroll'] = [];
    for (const element of document.querySelectorAll('body *')) {
      if (!isVisible(element)) continue;
      /*
       * A single-line text field scrolls its own value by definition; that is
       * the control working, not the layout failing. So does a label cut with
       * an ellipsis (a row's tag chip); the library's chip row and the review
       * player's waveform are sideways-scrolling lines on purpose. Everything
       * else that can scroll sideways is a container that should have wrapped.
       */
      if (
        element.matches('input, textarea, select, .chips, .note-row__tag, .clip-scrubber')
      ) {
        continue;
      }
      if (element.scrollWidth > element.clientWidth + 1 && element.clientWidth > 0) {
        sidewaysScroll.push({
          selector: describe(element),
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
        });
      }
    }

    return {
      sidewaysScroll,
      documentOverflow:
        root.scrollWidth > viewportWidth
          ? { scrollWidth: root.scrollWidth, clientWidth: viewportWidth }
          : null,
      bleeding,
      chromeOffscreen,
      overlaps,
    };
  });
}

function assertClean(report: LayoutReport, where: string): void {
  expect(report.documentOverflow, `${where}: the document scrolls horizontally`).toBeNull();
  expect(report.bleeding, `${where}: elements extend past the viewport`).toEqual([]);
  expect(
    report.chromeOffscreen,
    `${where}: the tab bar is not fully on screen`,
  ).toEqual([]);
  expect(report.overlaps, `${where}: the tab bar is overlapped`).toEqual([]);
  expect(report.sidewaysScroll, `${where}: a container scrolls horizontally`).toEqual([]);
}

for (const theme of THEMES) {
  for (const viewport of VIEWPORTS) {
    test.describe(`${viewport.name} · ${theme}`, () => {
      test.use({ viewport: { width: viewport.width, height: viewport.height } });

      test('every screen fits the viewport and leaves the chrome alone', async ({
        page,
        api,
      }) => {
        stressTheFixture(api);
        await withoutServiceWorker(page);
        await useTheme(page, theme);

        for (const route of ROUTES) {
          await page.goto(route.path);
          await expect(page.locator('.app')).toBeVisible();
          // The note screens paint asynchronously (audio artifacts, peaks).
          await page.waitForLoadState('networkidle');
          await shoot(page, `${route.label}__${viewport.name}__${theme}`);
          assertClean(await inspect(page), `${route.label} @ ${viewport.name}/${theme}`);
        }
      });

      test('the filing row never displaces the tab bar', async ({ page, api }) => {
        await withoutServiceWorker(page);
        await useTheme(page, theme);
        api.captures.push(
          {
            id: 'cap-working',
            status: 'transcribing',
            created_at: new Date().toISOString(),
            version: 1,
            note_id: null,
          },
          {
            id: 'cap-broken',
            status: 'failed',
            created_at: new Date().toISOString(),
            version: 1,
            note_id: null,
            error:
              'The transcription service refused that recording because it was longer than the twenty minute limit for a single capture.',
          },
        );

        await page.goto('/');
        await expect(page.getByRole('region', { name: /recordings being filed/i })).toBeVisible();
        await shoot(page, `filing__${viewport.name}__${theme}`);
        assertClean(await inspect(page), `filing row @ ${viewport.name}/${theme}`);
      });
    });
  }
}

/*
 * The capture screen gets its own pass because it needs a live microphone, and
 * because its defect was a different shape: a canvas whose percentage height
 * never resolved, so it kept its intrinsic 150px inside a 96px panel and drew
 * its bars straight through the Cancel / Pause / Stop row.
 */
for (const theme of THEMES) {
  for (const viewport of VIEWPORTS) {
    test.describe(`capture · ${viewport.name} · ${theme}`, () => {
      test.use({ viewport: { width: viewport.width, height: viewport.height } });

      test('the live waveform stays inside its panel', async ({ page }) => {
        await withoutServiceWorker(page);
        await useTheme(page, theme);
        await page.goto('/capture');
        await expect(page.locator('.capture__state')).toHaveText('Recording');
        await expect(page.locator('canvas.waveform')).toBeVisible();

        const escape = await page.evaluate(() => {
          const panel = document.querySelector('.capture__waveform');
          const canvas = document.querySelector('canvas.waveform');
          const controls = document.querySelector('.capture__controls');
          if (!panel || !canvas) return null;
          const panelBox = panel.getBoundingClientRect();
          const canvasBox = canvas.getBoundingClientRect();
          const controlsBox = controls?.getBoundingClientRect();
          return {
            belowPanel: Math.round(canvasBox.bottom - panelBox.bottom),
            abovePanel: Math.round(panelBox.top - canvasBox.top),
            pastPanelRight: Math.round(canvasBox.right - panelBox.right),
            overControls: controlsBox
              ? Math.round(
                  Math.min(canvasBox.bottom, controlsBox.bottom) -
                    Math.max(canvasBox.top, controlsBox.top),
                )
              : -1,
          };
        });

        expect(escape, 'the capture screen rendered no waveform').not.toBeNull();
        expect(escape?.belowPanel, 'the waveform hangs below its panel').toBeLessThanOrEqual(0);
        expect(escape?.abovePanel, 'the waveform rises above its panel').toBeLessThanOrEqual(0);
        expect(escape?.pastPanelRight, 'the waveform runs past its panel').toBeLessThanOrEqual(0);
        expect(escape?.overControls, 'the waveform covers the capture controls').toBeLessThan(0);

        await shoot(page, `capture__${viewport.name}__${theme}`);
        assertClean(await inspect(page), `capture @ ${viewport.name}/${theme}`);

        /*
         * Review: the live canvas gives way to the recording's own waveform,
         * which is deliberately wider than the screen inside its own scroller
         * (`.clip-scrubber`, excused from the sideways-scroll check) and must
         * not push anything else sideways or under the controls.
         */
        // A second of audio, so the review draws a waveform rather than a hairline.
        await page.waitForTimeout(1_100);
        await page.getByRole('button', { name: 'Stop' }).click();
        await expect(page.locator('.capture__state')).toHaveText('Ready to send');
        await expect(page.getByRole('slider', { name: 'Playback position' })).toBeVisible();
        await shoot(page, `capture-review__${viewport.name}__${theme}`);
        assertClean(await inspect(page), `capture review @ ${viewport.name}/${theme}`);
      });
    });
  }
}

/**
 * The update prompt is the app's only toast. It used to be `position: fixed`
 * against the bottom of the viewport with a z-index above the tab bar, which put
 * it squarely on top of the record button — the one control the product exists
 * to offer.
 */
for (const theme of THEMES) {
  test(`the update prompt does not cover the tab bar · ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await withoutServiceWorker(page);
    await useTheme(page, theme);
    await page.goto('/');
    await expect(page.locator('.tab-bar')).toBeVisible();

    // The real prompt only appears when a waiting service worker exists, which
    // no test can conjure reliably. The markup is what is under test, so it is
    // mounted directly where the shell mounts it.
    await page.evaluate(() => {
      const prompt = document.createElement('div');
      prompt.className = 'update-prompt';
      prompt.innerHTML =
        '<span>A new version of Chintan is ready.</span>' +
        '<button type="button" class="update-prompt__action">Update</button>';
      document.querySelector('.app')?.append(prompt);
    });

    const collision = await page.evaluate(() => {
      const prompt = document.querySelector('.update-prompt')?.getBoundingClientRect();
      const bar = document.querySelector('.tab-bar')?.getBoundingClientRect();
      const record = document.querySelector('.record-button')?.getBoundingClientRect();
      if (!prompt || !bar || !record) return null;
      const over = (other: DOMRect): number =>
        Math.round(
          Math.min(prompt.bottom, other.bottom) - Math.max(prompt.top, other.top),
        );
      return { bar: over(bar), record: over(record), bottom: Math.round(prompt.bottom) };
    });

    expect(collision).not.toBeNull();
    expect(collision?.bar, 'the update prompt covers the tab bar').toBeLessThanOrEqual(0);
    expect(collision?.record, 'the update prompt covers the record button').toBeLessThanOrEqual(0);
    assertClean(await inspect(page), `update prompt @ 390x844/${theme}`);
  });
}

/**
 * A landscape phone with the keyboard open leaves roughly 300 CSS px of height.
 * A centred dialog in a layer that cannot scroll puts its confirm button below
 * the fold and out of reach, which turns a destructive confirmation into a trap.
 */
for (const height of [390, 300, 240]) {
  test(`the confirm dialog stays reachable at 320x${height}`, async ({ page }) => {
    await page.setViewportSize({ width: 320, height });
    await withoutServiceWorker(page);
    await page.goto('/');
    await expect(page.locator('.app')).toBeVisible();

    await page.evaluate(() => {
      const layer = document.createElement('div');
      layer.className = 'dialog-layer';
      layer.innerHTML =
        '<div class="dialog-scrim"></div>' +
        '<div class="dialog" role="dialog" aria-modal="true">' +
        '<h2 class="dialog__title">Discard this recording?</h2>' +
        '<p class="dialog__body">It has not been sent, and it is not saved anywhere else. ' +
        'This cannot be undone, and there is no copy of it on the server or on any other ' +
        'device you have signed in on.</p>' +
        '<div class="dialog__actions">' +
        '<button type="button" class="dialog__action">Cancel</button>' +
        '<button type="button" class="dialog__action dialog__action--destructive">Discard recording</button>' +
        '</div></div>';
      document.querySelector('.app')?.append(layer);
    });

    const reach = await page.evaluate(() => {
      const layer = document.querySelector('.dialog-layer');
      const confirm = document.querySelector('.dialog__action--destructive');
      if (!layer || !confirm) return null;
      // Scroll the layer as a user would before deciding the button is lost.
      layer.scrollTop = layer.scrollHeight;
      const box = confirm.getBoundingClientRect();
      const dialog = document.querySelector('.dialog')?.getBoundingClientRect();
      return {
        confirmBottom: Math.round(box.bottom),
        confirmTop: Math.round(box.top),
        dialogTop: Math.round(dialog?.top ?? 0),
        viewport: document.documentElement.clientHeight,
      };
    });

    expect(reach).not.toBeNull();
    expect(
      reach!.confirmBottom,
      'the confirm button sits below the fold with no way to scroll to it',
    ).toBeLessThanOrEqual(reach!.viewport + 1);
    expect(reach!.confirmTop, 'the confirm button sits above the fold').toBeGreaterThanOrEqual(-1);
  });
}

/**
 * The record button is 76px, and the library's last row is never under the
 * bar. The bar is in normal flow and cannot overlay anything, but the list used
 * to end 32px from the edge, hard against it; a thumb resting on Record covered
 * the lower half of the last row. Measured on the two phones the owner uses.
 */
for (const viewport of [
  { name: '320x568', width: 320, height: 568 },
  { name: '412x915', width: 412, height: 915 },
]) {
  test(`the record button is 76px and the last row clears the tab bar at ${viewport.name}`, async ({
    page,
    api,
  }) => {
    // Enough notes that the library scrolls at any phone height.
    for (let index = 0; index < 30; index += 1) {
      const id = `filler-${String(index)}`;
      api.notes[id] = {
        id,
        title: `Filler note ${String(index + 1)}`,
        body: 'Padding for the scroll test.',
        snippet: 'Padding for the scroll test.',
        tags: [],
        aliases: [],
        updated_at: new Date(Date.UTC(2026, 7, 1 + (index % 28))).toISOString(),
        version: 1,
        archived: false,
        captures: [],
      };
    }
    await withoutServiceWorker(page);
    await page.setViewportSize(viewport);
    await page.goto('/');
    await expect(page.locator('.note-row').first()).toBeVisible();

    const measured = await page.evaluate(() => {
      const main = document.querySelector('.app__main');
      if (!main) return null;
      main.scrollTop = main.scrollHeight;
      const rows = document.querySelectorAll('.note-row');
      const last = rows[rows.length - 1]?.getBoundingClientRect();
      const bar = document.querySelector('.tab-bar')?.getBoundingClientRect();
      const record = document.querySelector('.record-button')?.getBoundingClientRect();
      if (!last || !bar || !record) return null;
      return {
        scrolls: main.scrollHeight > main.clientHeight,
        lastBottom: Math.round(last.bottom),
        barTop: Math.round(bar.top),
        barBottom: Math.round(bar.bottom),
        viewport: document.documentElement.clientHeight,
        record: { width: Math.round(record.width), height: Math.round(record.height) },
      };
    });

    expect(measured, 'the library rendered no rows, bar or record button').not.toBeNull();
    expect(measured!.scrolls, 'the list must be long enough to scroll for this to mean anything').toBe(
      true,
    );
    expect(measured!.record.width).toBeGreaterThanOrEqual(76);
    expect(measured!.record.height).toBeGreaterThanOrEqual(76);
    // Scrolled to the very end, the last row sits above the bar with the
    // list's bottom padding (a bar's height) between them.
    expect(
      measured!.barTop - measured!.lastBottom,
      'the last row does not clear the tab bar',
    ).toBeGreaterThanOrEqual(measured!.barBottom - measured!.barTop - 1);
    expect(measured!.barBottom, 'the bar is not fully on screen').toBeLessThanOrEqual(
      measured!.viewport + 1,
    );
  });
}

/**
 * WCAG 2.2 §2.5.8 puts the floor at 24×24; the product's own token says 44×44
 * (`--layout-touch-target-min`) and every control in the app is meant to meet
 * it. v1 shipped a 24px download button, which is how that token came to exist.
 */
test('every control meets the 44px touch target on the narrowest phone', async ({
  page,
  api,
}) => {
  stressTheFixture(api);
  await withoutServiceWorker(page);
  await page.setViewportSize({ width: 320, height: 568 });

  const undersized: string[] = [];
  for (const path of ROUTES.map((route) => route.path)) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    undersized.push(
      ...(await page.evaluate(() => {
        const found: string[] = [];
        const controls = document.querySelectorAll(
          'a[href], button, input:not([type=hidden]), select, textarea, [role="button"]',
        );
        for (const control of controls) {
          const style = getComputedStyle(control);
          if (style.display === 'none' || style.visibility === 'hidden') continue;
          if (control.classList.contains('visually-hidden')) continue;
          const box = control.getBoundingClientRect();
          if (box.width === 0 && box.height === 0) continue;
          // The scrim is a backdrop, not a target.
          if (control.classList.contains('dialog-scrim')) continue;
          if (box.width < 43.5 || box.height < 43.5) {
            const classes =
              typeof control.className === 'string' ? control.className.trim() : '';
            found.push(
              `${location.pathname} ${control.tagName.toLowerCase()}.${classes} ` +
                `${Math.round(box.width)}x${Math.round(box.height)}`,
            );
          }
        }
        return found;
      })),
    );
  }

  expect(undersized, 'controls smaller than --layout-touch-target-min').toEqual([]);
});
