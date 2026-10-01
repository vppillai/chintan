#!/usr/bin/env bun
/**
 * The main chunk's size budget (review 2026-10-01, FE-2; the number is owner
 * decision D2). Three rounds hand-recorded the bundle size in the backlog and
 * CI noticed nothing when it grew 35 kB in one round; now CI's "Bundle
 * budget" step (`bun run check-bundle`, after the build) fails above it.
 *
 * 540 kB is D2's recommended default — 540 with a ratchet — taken on the
 * owner's behalf when round 9's query hygiene and a11y descriptions put the
 * chunk 190 bytes over the first 530 kB (PR9-9a); the owner may set another
 * number. The ratchet is by hand: the check prints the measured size and the
 * headroom on every run, and a reviewer lowers BUDGET_BYTES in a PR that
 * lands well under it. It never fails for being under. Raise it only with
 * the owner's say-so, in the change that explains what grew.
 */

import { readdir, stat } from 'node:fs/promises';
import process from 'node:process';

/** Raw bytes of `dist/assets/index-*.js`, in Vite's unit (1 kB = 1000 bytes). */
const BUDGET_BYTES = 540_000;

const assets = new URL('../dist/assets/', import.meta.url);
const main = (await readdir(assets)).find((name) => /^index-.*\.js$/.test(name));
if (!main) {
  console.error('check-bundle: no dist/assets/index-*.js — run the build first.');
  process.exit(1);
}

const { size } = await stat(new URL(main, assets));
const kB = (bytes) => `${(bytes / 1000).toFixed(2)} kB`;
// The measured size and the headroom, every run, so a reviewer can see when a
// PR lands well under and lower the budget (the ratchet; see the header).
const measured = `check-bundle: ${main} is ${kB(size)} against the ${kB(BUDGET_BYTES)} budget (D2)`;
if (size > BUDGET_BYTES) {
  console.error(
    `${measured}: ${kB(size - BUDGET_BYTES)} over. ` +
      'Lazy-load what grew, or raise BUDGET_BYTES with the owner.',
  );
  process.exit(1);
}
console.log(`${measured}: ${kB(BUDGET_BYTES - size)} of headroom.`);
