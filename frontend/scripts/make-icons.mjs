#!/usr/bin/env node
/**
 * Renders the PWA icon PNGs from the SVGs beside them in `public/`.
 *
 *   public/icon.svg           the launcher artwork: Ink & Paper ground, the
 *                             Bindu C in ink, the bindu in the accent, the
 *                             glyph at 78 % of the tile (R5-BR-L1)
 *   public/icon-maskable.svg  the same at 60 %: Android crops a maskable icon
 *                             to an arbitrary shape and guarantees only the
 *                             central 80 % circle, so the ground bleeds to
 *                             every edge and the glyph stays inside it
 *
 * The design record is `docs/design/branding/bindu-*.svg`. The PNGs are
 * committed because the manifest names them and iOS reads only the PNG; this
 * script exists so they are regenerated from the SVGs rather than edited.
 * Chromium draws them — the Playwright browser the e2e suite already installs
 * — because it rasterises the SVG exactly as the browser shows the favicon,
 * and a PNG encoder by hand (which is what this file used to be, painting a
 * microphone pixel by pixel) is not worth owning for five files.
 *
 *   cd frontend && node scripts/make-icons.mjs
 */
import { chromium } from '@playwright/test';

const PUBLIC = new URL('../public/', import.meta.url);

const TARGETS = [
  ['icon.svg', 'icon-192.png', 192],
  ['icon.svg', 'icon-512.png', 512],
  ['icon.svg', 'apple-touch-icon.png', 180],
  ['icon-maskable.svg', 'icon-maskable-192.png', 192],
  ['icon-maskable.svg', 'icon-maskable-512.png', 512],
];

const browser = await chromium.launch();
const page = await browser.newPage({ deviceScaleFactor: 1 });
for (const [source, target, size] of TARGETS) {
  // An SVG document with a viewBox and no width fills the viewport, so the
  // viewport is the tile.
  await page.setViewportSize({ width: size, height: size });
  await page.goto(new URL(source, PUBLIC).href);
  await page.screenshot({ path: new URL(target, PUBLIC).pathname, type: 'png' });
  console.log(`wrote public/${target} (${String(size)}x${String(size)}) from ${source}`);
}
await browser.close();
