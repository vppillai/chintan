#!/usr/bin/env bun
/**
 * The size budget for what a launch downloads: the main chunk
 * (`dist/assets/index-*.js`) plus every chunk `dist/index.html` preloads
 * (`<link rel="modulepreload">`), which the browser fetches alongside it
 * before the app can run. The budget is the owner's (decision D2: a number
 * with a ratchet). Rounds hand-recorded the bundle size in the backlog and CI
 * noticed nothing when it grew 35 kB in one round; now CI's "Bundle budget"
 * step (`bun run check-bundle`, after the build) fails above it.
 *
 * The preloaded chunks count because the measure is the launch, not a file
 * name: the bundler is free to fold a shared chunk into the main one or split
 * one out, and a budget that read only `index-*.js` once held 538 kB of a
 * 620 kB launch. The ratchet is by hand: the check prints the measured size
 * and the headroom on every run, and a reviewer lowers BUDGET_BYTES to the
 * measured size plus 10 kB in a PR that lands under it. It never fails for
 * being under. Raise it only with the owner's say-so, in the change that
 * explains what grew.
 */

import { readFile, readdir, stat } from 'node:fs/promises';
import process from 'node:process';

/** Raw bytes, in Vite's unit (1 kB = 1000 bytes). */
const BUDGET_BYTES = 625_000;

const dist = new URL('../dist/', import.meta.url);
const assets = new URL('assets/', dist);
const main = (await readdir(assets)).find((name) => /^index-.*\.js$/.test(name));
if (!main) {
  console.error('check-bundle: no dist/assets/index-*.js — run the build first.');
  process.exit(1);
}
const html = await readFile(new URL('index.html', dist), 'utf8');
const preloaded = [...html.matchAll(/rel="modulepreload"[^>]*href="[^"]*\/assets\/([^"]+\.js)"/g)].map(
  (match) => match[1],
);

const kB = (bytes) => `${(bytes / 1000).toFixed(2)} kB`;
const sizes = await Promise.all(
  [main, ...preloaded].map(async (name) => ({ name, size: (await stat(new URL(name, assets))).size })),
);
const total = sizes.reduce((sum, file) => sum + file.size, 0);
const parts =
  sizes.map((file) => `${file.name} ${kB(file.size)}`).join(' + ') +
  (preloaded.length === 0 ? ', 0 preloaded' : '');
// The measured size and the headroom, every run, so a reviewer can see when a
// PR lands well under and lower the budget (the ratchet; see the header).
const measured = `check-bundle: a launch fetches ${kB(total)} (${parts}) against the ${kB(BUDGET_BYTES)} budget (D2)`;
if (total > BUDGET_BYTES) {
  console.error(
    `${measured}: ${kB(total - BUDGET_BYTES)} over. ` +
      'Lazy-load what grew, or raise BUDGET_BYTES with the owner.',
  );
  process.exit(1);
}
console.log(`${measured}: ${kB(BUDGET_BYTES - total)} of headroom.`);
