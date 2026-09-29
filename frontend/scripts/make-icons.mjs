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
 *
 * `--cognito` renders the hosted sign-in page's assets instead: the mark on a
 * transparent ground from `infrastructure/branding/cognito-logo-{light,dark}.svg`
 * at 192 px, once as a PNG (FORM_LOGO) and once as the same PNG inside a
 * one-image ICO wrapper (FAVICON_ICO — Cognito takes only ICO or SVG for the
 * favicon, and the ICO container costs a 22-byte header). It prints the four
 * `Assets` entries in the template's own YAML shape, because
 * `AWS::Cognito::ManagedLoginBranding` takes the bytes inline and nothing
 * else — no S3 reference — so `infrastructure/template.yaml` has to carry
 * them and this is how they are regenerated rather than edited.
 *
 *   cd frontend && node scripts/make-icons.mjs --cognito
 */
import { chromium } from '@playwright/test';

const PUBLIC = new URL('../public/', import.meta.url);
const BRANDING = new URL('../../infrastructure/branding/', import.meta.url);

const TARGETS = [
  ['icon.svg', 'icon-192.png', 192],
  ['icon.svg', 'icon-512.png', 512],
  ['icon.svg', 'apple-touch-icon.png', 180],
  ['icon-maskable.svg', 'icon-maskable-192.png', 192],
  ['icon-maskable.svg', 'icon-maskable-512.png', 512],
];

/** Cognito reads a ColorMode per asset, so the variants are one SVG each. */
const COGNITO = [
  ['cognito-logo-light.svg', 'LIGHT'],
  ['cognito-logo-dark.svg', 'DARK'],
];
const COGNITO_SIZE = 192;

/**
 * One PNG in the ICO container: a 6-byte ICONDIR (reserved, type 1 = icon,
 * one image) and a 16-byte ICONDIRENTRY (width, height, no palette, reserved,
 * one colour plane, 32 bits per pixel, the PNG's byte length, and the PNG's
 * offset, which is this header's own length). Every ICO reader since Vista
 * accepts PNG-compressed entries, and Cognito's favicon is served to browsers.
 */
function wrapIco(png, size) {
  const header = Buffer.alloc(22);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(1, 4);
  // A 256 px edge is written as 0; the size here is 192, so the byte is literal.
  header[6] = size;
  header[7] = size;
  header.writeUInt16LE(1, 10);
  header.writeUInt16LE(32, 12);
  header.writeUInt32LE(png.length, 14);
  header.writeUInt32LE(header.length, 18);
  return Buffer.concat([header, png]);
}

const browser = await chromium.launch();
const page = await browser.newPage({ deviceScaleFactor: 1 });

if (process.argv.includes('--cognito')) {
  await page.setViewportSize({ width: COGNITO_SIZE, height: COGNITO_SIZE });
  const rendered = [];
  for (const [source, colorMode] of COGNITO) {
    await page.goto(new URL(source, BRANDING).href);
    // omitBackground drops Chromium's white page background, which is what
    // makes the ground transparent instead of a white tile on Nocturne.
    rendered.push([colorMode, await page.screenshot({ type: 'png', omitBackground: true })]);
  }
  // FORM_LOGO light, dark, then FAVICON_ICO light, dark — the template's order.
  const entries = [
    ...rendered.map(([colorMode, png]) => ['FORM_LOGO', colorMode, 'PNG', png]),
    ...rendered.map(([colorMode, png]) => ['FAVICON_ICO', colorMode, 'ICO', wrapIco(png, COGNITO_SIZE)]),
  ];
  console.log('      Assets:');
  for (const [category, colorMode, extension, bytes] of entries) {
    console.log(`        - Category: ${category}`);
    console.log(`          ColorMode: ${colorMode}`);
    console.log(`          Extension: ${extension}`);
    console.log(`          Bytes: ${bytes.toString('base64')}`);
  }
} else {
  for (const [source, target, size] of TARGETS) {
    // An SVG document with a viewBox and no width fills the viewport, so the
    // viewport is the tile.
    await page.setViewportSize({ width: size, height: size });
    await page.goto(new URL(source, PUBLIC).href);
    await page.screenshot({ path: new URL(target, PUBLIC).pathname, type: 'png' });
    console.log(`wrote public/${target} (${String(size)}x${String(size)}) from ${source}`);
  }
}
await browser.close();
