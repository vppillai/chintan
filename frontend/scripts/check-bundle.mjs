#!/usr/bin/env bun
/**
 * The main chunk's size budget (review 2026-10-01, FE-2; the number is owner
 * decision D2). Three rounds hand-recorded the bundle size in the backlog and
 * CI noticed nothing when it grew 35 kB in one round; now CI's "Bundle
 * budget" step (`bun run check-bundle`, after the build) fails above it.
 * Raise BUDGET_BYTES only with the owner's say-so, in the change that
 * explains what grew.
 */

import { readdir, stat } from 'node:fs/promises';
import process from 'node:process';

/** Raw bytes of `dist/assets/index-*.js`, in Vite's unit (1 kB = 1000 bytes). */
const BUDGET_BYTES = 530_000;

const assets = new URL('../dist/assets/', import.meta.url);
const main = (await readdir(assets)).find((name) => /^index-.*\.js$/.test(name));
if (!main) {
  console.error('check-bundle: no dist/assets/index-*.js — run the build first.');
  process.exit(1);
}

const { size } = await stat(new URL(main, assets));
const kB = (bytes) => `${(bytes / 1000).toFixed(2)} kB`;
if (size > BUDGET_BYTES) {
  console.error(
    `check-bundle: ${main} is ${kB(size)}, over the ${kB(BUDGET_BYTES)} budget (D2). ` +
      'Lazy-load what grew, or raise BUDGET_BYTES with the owner.',
  );
  process.exit(1);
}
console.log(`check-bundle: ${main} is ${kB(size)}, within the ${kB(BUDGET_BYTES)} budget`);
