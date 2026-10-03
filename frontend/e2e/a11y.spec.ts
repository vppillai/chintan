import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';

import { HOLD_ARM_MS, LOCK_DY_PX } from '../src/features/capture/holdTiming.ts';

import { expect, noteAction, seedChecklist, seedStuckCapture, startSignedOut, test, type ApiState } from './fixtures.ts';

/**
 * Accessibility, in both themes.
 *
 * The contrast rule is included deliberately: it is the one that catches a
 * palette whose muted text slips below AA.
 */

const ROUTES = [
  '/',
  '/?view=archived',
  '/?q=roof',
  '/settings',
  '/settings?passkey=invalid_session',
  '/about',
  '/notes/roof-repair',
  '/notes/roof-repair?tab=recordings',
  '/notes/roof-repair?tab=cleaned',
] as const;
const THEMES = ['ink', 'nocturne'] as const;

/** The axe sweep of the screen as it stands, failing on anything critical or serious. */
async function expectNoSeriousViolations(page: Page): Promise<void> {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze();

  const serious = results.violations.filter(
    (violation) => violation.impact === 'critical' || violation.impact === 'serious',
  );

  expect(
    serious,
    serious
      .map(
        (violation) =>
          `${violation.id} (${violation.impact}): ${violation.nodes
            .map((node) => node.target.join(' '))
            .join(', ')}`,
      )
      .join('\n'),
  ).toEqual([]);
}

for (const theme of THEMES) {
  for (const route of ROUTES) {
    test(`${route} has no critical axe violations in ${theme}`, async ({ page }) => {
      await page.addInitScript((value) => {
        localStorage.setItem('chintan.theme', value);
      }, theme);

      await page.goto(route);
      await expect(page.locator('main')).toBeVisible();
      // Let the screen's requests land so the scan sees the real screen.
      await page.waitForLoadState('networkidle');
      await expectNoSeriousViolations(page);
    });
  }

  /*
   * The Items tab with a grip's menu open: the grip is a drag handle that is
   * also the row's menu button, the Done heading is a disclosure with two
   * text buttons beside it — the controls the checklist reorder work added.
   */
  test(`the Items tab with a grip menu open has no critical axe violations in ${theme}`, async ({
    page,
    api,
  }) => {
    seedChecklist(api);
    await page.addInitScript((value) => {
      localStorage.setItem('chintan.theme', value);
    }, theme);

    await page.goto('/notes/shopping');
    await expect(page.getByRole('heading', { name: 'Done (1)' })).toBeVisible();
    await page.getByRole('button', { name: 'Move Milk' }).click();
    await expect(page.getByRole('menu')).toBeVisible();
    await expectNoSeriousViolations(page);
  });
}

/*
 * Home with every filing tier in the tray (F9): moving, asking which note,
 * failed, and two receipts folded. The glyphs, the hairlines and the faint
 * "· 2 min" are on the notice surface, which is what the contrast rule reads.
 */
for (const theme of THEMES) {
  test(`Home with every filing tier has no critical axe violations in ${theme}`, async ({
    page,
    api,
  }) => {
    const now = new Date().toISOString();
    const row = { created_at: now, version: 1, note_id: null } as const;
    api.captures.push(
      { ...row, id: 'cap-moving', status: 'transcribing' },
      { ...row, id: 'cap-needs', status: 'needs_target', excerpt: 'Call the plumber.' },
      { ...row, id: 'cap-failed', status: 'failed', error: 'Couldn’t transcribe this' },
      { ...row, id: 'cap-a', status: 'appended', appended_at: now, note_id: 'roof-repair' },
      { ...row, id: 'cap-b', status: 'appended', appended_at: now, note_id: 'reading-list' },
    );
    await page.addInitScript((value) => {
      localStorage.setItem('chintan.theme', value);
    }, theme);

    await page.goto('/');
    const filing = page.getByRole('region', { name: 'Filing' });
    await expect(filing.getByText(/filed into/)).toBeVisible();
    await expect(filing.getByText('Couldn’t transcribe this')).toBeVisible();
    await page.waitForLoadState('networkidle');
    await expectNoSeriousViolations(page);
  });
}

/**
 * A heading to land on, on every screen. The note screen had none: its title
 * is an input (axe `page-has-heading-one`, the one finding of the QA pass's
 * scans), so the label is now the page's h1 as well.
 */
for (const route of ROUTES) {
  test(`${route} has exactly one h1`, async ({ page }) => {
    await page.goto(route);
    await expect(page.locator('main')).toBeVisible();
    await expect(page.locator('h1')).toHaveCount(1);
  });
}

/**
 * WCAG 2.5.8: every control at least 24 by 24 CSS pixels. The selection
 * checkboxes were 20 and the plain playback slider 16 tall. The recordings'
 * checkbox is the app's one selection box now: the library's went with note
 * multi-select (owner, 2026-09-29).
 */
test('the recording checkbox and the playback slider are at least 24 px, inside a 44 px target', async ({ page, api }) => {
  // The plain slider: a capture with no peaks to draw. Seeded before the first
  // visit, because the library prefetches the first page's bodies into the
  // device cache while idle, and the note screen mounts from the device's copy
  // before the server answers — a copy taken before the capture existed would
  // open no recording, and the newest recording opens itself only on mount.
  api.notes['reading-list']!.captures = [
    {
      id: 'cap-plain',
      status: 'appended',
      created_at: '2026-01-01T00:00:00.000Z',
      version: 1,
      note_id: 'reading-list',
      duration_ms: 5_000,
      has_peaks: false,
      has_segments: false,
    },
  ];

  await page.goto('/');
  await expect(page.getByRole('button', { name: /reading list/i })).toBeVisible();
  await page.goto('/notes/reading-list?tab=recordings');
  const slider = await page.getByRole('slider', { name: 'Playback position' }).boundingBox();
  expect(slider?.height ?? 0).toBeGreaterThanOrEqual(24);

  await page.getByRole('button', { name: /more for recording/i }).first().click();
  await page.getByRole('menuitem', { name: 'Select' }).click();
  await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toBeVisible();
  const checkbox = await page.getByRole('checkbox').first().boundingBox();
  expect(checkbox?.width ?? 0).toBeGreaterThanOrEqual(24);
  expect(checkbox?.height ?? 0).toBeGreaterThanOrEqual(24);
  // And the thumb has a 44 px target around it. That target is the box's
  // wrapper, not the label: the label spans the row, so it would pass at any
  // wrapper size and pin nothing.
  const target = await page.locator('.recording__check').first().boundingBox();
  expect(target?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(target?.height ?? 0).toBeGreaterThanOrEqual(44);
});

/**
 * The parked skip link used to leak its shadow: translated 70 px up, its box
 * ended above the viewport but its 28 px shadow did not, and every desktop
 * screenshot carried a grey sliver in the top-left corner (QA D19).
 */
test('the skip link casts no shadow until it is shown', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  const skip = page.getByRole('link', { name: /skip to content/i });

  const parked = await skip.evaluate((element) => {
    const { boxShadow } = getComputedStyle(element);
    return { boxShadow, bottom: element.getBoundingClientRect().bottom };
  });
  expect(parked.boxShadow).toBe('none');
  expect(parked.bottom).toBeLessThanOrEqual(0);

  await page.keyboard.press('Tab');
  await expect(skip).toBeFocused();
  expect(await skip.evaluate((element) => getComputedStyle(element).boxShadow)).not.toBe('none');
});

test('the library is fully traversable by keyboard', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // Tab until a note row has focus, proving rows are real buttons rather than
  // clickable divs, which would leave the library keyboard-unreachable.
  let reached = false;
  for (let step = 0; step < 25 && !reached; step += 1) {
    await page.keyboard.press('Tab');
    reached = await page.evaluate(() =>
      Boolean(document.activeElement?.classList.contains('note-row')),
    );
  }
  expect(reached).toBe(true);

  // Enter opens it.
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/notes\/[a-z-]+$/);
});

test('the skip link moves focus to main', async ({ page }) => {
  await page.goto('/');
  // Let the library settle first, for the same reason as the focus-ring test
  // below: a Tab pressed while the shell is still mounting lands on <body>.
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: /skip to content/i });
  await expect(skip).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#main$/);
});

test('every interactive element shows a visible focus ring', async ({ page }) => {
  await page.goto('/');
  // Let the library settle first: a Tab that lands on a control the next
  // render replaces leaves focus on <body>, which has no ring to measure.
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');

  const outline = await page.evaluate(() => {
    const element = document.activeElement;
    if (!element || element === document.body) return null;
    const styles = getComputedStyle(element);
    return { tag: element.tagName, width: styles.outlineWidth, style: styles.outlineStyle };
  });

  expect(outline).not.toBeNull();
  expect(outline?.style).not.toBe('none');
  expect(parseFloat(outline?.width ?? '0')).toBeGreaterThan(0);
});

test('Back from a note returns to the library, not out of the app', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page).toHaveURL(/\/notes\/roof-repair$/);

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { name: 'Notes' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Record', exact: true })).toBeVisible();
});

test('a deep link seeds the library beneath it, so Back stays in the app', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
});

/**
 * The running build, at the foot of the You screen.
 *
 * Injected from the git SHA at build time; `playwright.config.ts` pins a known
 * value so this can assert the injection actually reaches the screen rather
 * than that some text is present.
 */
test('the You screen names the build it is running, legibly', async ({ page }) => {
  await page.goto('/settings');

  const footnote = page.locator('.version-footnote');
  await expect(footnote).toContainText('e2e-abc1234');

  // "Faded" is not a licence to be invisible: --color-faint is the palette's
  // quietest text and tokens.css documents it as meeting AA.
  const readable = await footnote.evaluate((element) => {
    const style = getComputedStyle(element);
    return { color: style.color, opacity: Number(style.opacity) };
  });
  expect(readable.opacity).toBeGreaterThan(0.5);
  expect(readable.color).not.toBe('rgba(0, 0, 0, 0)');

  // It exists to be copied into a bug report, so it must be selectable text.
  await expect(footnote.locator('code')).toHaveText('e2e-abc1234');
});

/*
 * The surfaces rounds 7–8 added, which the route sweep above never reaches:
 * each is opened the way a person opens it and scanned as it stands, in both
 * themes (review 2026-10-01, FE-12). The capture and hold surfaces record
 * through the fake microphone, which only Chromium has.
 */
interface Surface {
  name: string;
  open: (page: Page, api: ApiState) => Promise<void>;
  needsMicrophone?: true;
  /** Still there after the scan: the state scanned was the state named. */
  after?: (page: Page) => Promise<void>;
}

async function holdTheDisc(page: Page): Promise<{ x: number; y: number }> {
  await page.goto('/');
  const box = (await page.locator('.record-button').boundingBox())!;
  const at = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  await page.mouse.move(at.x, at.y);
  await page.mouse.down();
  await expect(page.getByRole('navigation', { name: 'Main' })).toHaveAttribute('data-hold', 'holding');
  return at;
}

const SURFACES: readonly Surface[] = [
  {
    name: '/capture recording',
    needsMicrophone: true,
    open: async (page) => {
      await page.goto('/capture');
      await expect(page.locator('.capture__state')).toHaveText('Recording');
    },
  },
  {
    name: '/capture paused',
    needsMicrophone: true,
    open: async (page) => {
      await page.goto('/capture');
      await page.getByRole('button', { name: 'Pause' }).click();
      await expect(page.locator('.capture__state')).toHaveText('Paused');
    },
  },
  {
    name: '/capture review',
    needsMicrophone: true,
    open: async (page) => {
      await page.goto('/capture');
      // A Stop inside the first encoder frame can finish with nothing to
      // review; the clock moving means a chunk is on its way.
      await expect(page.locator('.capture__timer')).not.toHaveText('00:00');
      await page.getByRole('button', { name: 'Stop' }).click();
      await expect(page.locator('.capture__state')).toHaveText('Ready to send');
    },
  },
  {
    // The screen's resting state: it asks for the microphone on arrival, so
    // the one way to see it with nothing running is to be refused.
    name: '/capture with the microphone refused',
    open: async (page) => {
      await page.addInitScript(() => {
        navigator.mediaDevices.getUserMedia = () =>
          Promise.reject(new DOMException('Permission denied', 'NotAllowedError'));
      });
      await page.goto('/capture');
      await expect(page.locator('.capture__state')).toHaveText('Something went wrong');
    },
  },
  {
    name: 'the tab bar while the disc is held',
    needsMicrophone: true,
    open: async (page) => {
      await holdTheDisc(page);
    },
  },
  {
    name: 'the tab bar with the held finger at the lock line',
    needsMicrophone: true,
    open: async (page) => {
      const { x, y } = await holdTheDisc(page);
      await page.mouse.move(x, y - LOCK_DY_PX + 8, { steps: 8 });
      // The bar's lock progress, which HoldChrome draws from (TabBar `--hold-lock`).
      await expect
        .poll(() =>
          page
            .getByRole('navigation', { name: 'Main' })
            .evaluate((bar) => Number(bar.style.getPropertyValue('--hold-lock'))),
        )
        .toBeGreaterThan(0.5);
    },
  },
  {
    name: 'a locked take on the recording screen',
    needsMicrophone: true,
    open: async (page) => {
      const { x, y } = await holdTheDisc(page);
      await page.waitForTimeout(HOLD_ARM_MS + 100);
      await page.mouse.move(x, y - LOCK_DY_PX - 8, { steps: 8 });
      await page.mouse.up();
      await expect(page).toHaveURL(/\/capture$/);
      await expect(page.locator('.capture__state')).toHaveText('Recording');
    },
  },
  {
    name: 'a tick’s Undo toast',
    open: async (page, api) => {
      seedChecklist(api);
      await page.goto('/notes/shopping');
      await page.getByRole('checkbox', { name: 'Milk' }).click();
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
    after: async (page) => {
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
  },
  {
    name: 'Delete done’s Undo toast',
    open: async (page, api) => {
      seedChecklist(api);
      await page.goto('/notes/shopping');
      await page.getByRole('button', { name: 'Delete done' }).click();
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
    after: async (page) => {
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
  },
  {
    name: 'Tidy up list’s Undo toast',
    open: async (page, api) => {
      seedChecklist(api);
      await page.goto('/notes/shopping');
      await noteAction(page, 'Tidy up list');
      await expect(page.getByText(/^List tidied/)).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
    after: async (page) => {
      await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
    },
  },
  {
    name: 'the confirm dialog',
    open: async (page) => {
      await page.goto('/notes/old-fence');
      await noteAction(page, 'Delete forever');
      await expect(page.getByRole('dialog').getByRole('button', { name: 'Cancel' })).toBeFocused();
    },
  },
  {
    name: 'the Move sheet',
    open: async (page) => {
      await page.goto('/notes/roof-repair?tab=recordings');
      await page.getByRole('button', { name: /more for recording from/i }).click();
      await page.getByRole('menuitem', { name: 'Move to…' }).click();
      await expect(page.getByRole('dialog', { name: /move this recording to/i })).toBeVisible();
    },
  },
  {
    name: 'the Details sheet',
    open: async (page) => {
      await page.goto('/notes/roof-repair');
      await noteAction(page, 'Details');
      await expect(page.getByRole('button', { name: 'Close details' })).toBeVisible();
    },
  },
  {
    name: 'the Share sheet',
    open: async (page) => {
      await page.goto('/notes/roof-repair');
      await noteAction(page, 'Share');
      await expect(page.getByRole('button', { name: 'Copy note' })).toBeVisible();
    },
  },
  {
    // With the cleaned view's own group under the note's pair.
    name: 'the Share sheet of a cleaned note',
    open: async (page) => {
      await page.goto('/notes/roof-repair?tab=cleaned');
      await page.getByRole('button', { name: 'Generate' }).click();
      await expect(page.locator('.cleaned__body').getByRole('heading', { name: 'Summary' })).toBeVisible({
        timeout: 10_000,
      });
      await noteAction(page, 'Share');
      await expect(page.getByRole('button', { name: 'Copy cleaned view' })).toBeVisible();
    },
  },
  {
    name: 'the Share sheet of a checklist',
    open: async (page, api) => {
      seedChecklist(api);
      await page.goto('/notes/shopping');
      await noteAction(page, 'Share');
      await expect(page.getByText(/Items copy as/)).toBeVisible();
    },
  },
  {
    name: 'Home with the New note menu open',
    open: async (page) => {
      await page.goto('/');
      await page.getByRole('button', { name: 'New note' }).click();
      await expect(page.getByRole('menuitem', { name: 'Checklist' })).toBeVisible();
    },
  },
  {
    name: 'the find bar with a match',
    open: async (page) => {
      await page.goto('/notes/roof-repair');
      await page.getByRole('button', { name: 'Find in note' }).click();
      await page.getByRole('searchbox', { name: 'Find in note' }).fill('tiles');
      await expect(page.locator('.find-match').first()).toBeVisible();
    },
  },
  {
    name: 'the banner mic while typing under a keyboard',
    open: async (page) => {
      await page.goto('/notes/roof-repair');
      await page.evaluate(() => {
        document.documentElement.style.setProperty('--keyboard-inset', '400px');
        document.documentElement.setAttribute('data-keyboard', '');
      });
      await page.getByRole('textbox', { name: 'Note body' }).focus();
      await expect(page.locator('.app__banner').getByRole('button', { name: 'Record into this note' })).toBeVisible();
    },
  },
  {
    name: 'the recordings selection bar',
    open: async (page) => {
      await page.goto('/notes/roof-repair?tab=recordings');
      await page.getByRole('button', { name: /more for recording from/i }).click();
      await page.getByRole('menuitem', { name: 'Select' }).click();
      await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toBeVisible();
    },
  },
  {
    name: 'a stuck recording on the note’s banner',
    open: async (page, api) => {
      seedStuckCapture(api);
      await page.goto('/notes/roof-repair');
      await expect(
        page.getByRole('region', { name: 'Filing a recording' }).getByRole('button', { name: 'Retry' }),
      ).toBeVisible();
    },
  },
  {
    name: 'a stuck recording on the Recordings tab',
    open: async (page, api) => {
      seedStuckCapture(api);
      await page.goto('/notes/roof-repair?tab=recordings');
      await expect(page.getByText('Still not done after 16 min')).toBeVisible();
    },
  },
  {
    name: 'a stuck recording in Home’s tray',
    open: async (page, api) => {
      seedStuckCapture(api);
      await page.goto('/');
      await expect(
        page.getByRole('region', { name: 'Filing' }).getByRole('button', { name: 'Retry' }),
      ).toBeVisible();
    },
  },
  {
    name: 'the signed-out screen',
    open: async (page) => {
      await startSignedOut(page);
      await page.goto('/');
      await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    },
  },
  {
    // A screen whose code never arrives is the one render fault a stub can
    // make: the About chunk is refused, and the route's boundary draws instead.
    name: 'the route error screen',
    open: async (page) => {
      await page.route(/AboutScreen[^/]*\.js$/, (route) => route.abort());
      await page.goto('/about');
      await expect(page.getByRole('heading', { name: 'This screen could not be drawn' })).toBeVisible();
    },
  },
];

for (const theme of THEMES) {
  for (const surface of SURFACES) {
    test(`${surface.name} has no critical axe violations in ${theme}`, async ({ page, api, browserName }) => {
      test.skip(Boolean(surface.needsMicrophone) && browserName !== 'chromium', 'needs the fake microphone');
      await page.addInitScript((value) => {
        localStorage.setItem('chintan.theme', value);
      }, theme);
      await surface.open(page, api);
      await expectNoSeriousViolations(page);
      await surface.after?.(page);
    });
  }
}
