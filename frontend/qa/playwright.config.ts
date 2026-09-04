import process from 'node:process';
import { fileURLToPath } from 'node:url';

import { defineConfig, devices } from '@playwright/test';

/**
 * Exploratory QA against the LIVE deployment — not part of `bun run e2e`.
 *
 * `playwright.config.ts` at the frontend root only looks in `./e2e`, and vitest
 * only in `src/**`, so nothing here runs in CI. Run it by hand:
 *
 *   QA_USER=… QA_PASS=… bunx playwright test -c qa/playwright.config.ts
 *   QA_USER=… QA_PASS=… bunx playwright test -c qa/playwright.config.ts --project=mobile
 *
 * It signs in to the real Cognito managed-login page once per project and
 * persists the tokens (`qa/state/<project>.json`), then every other spec reuses
 * them. Screenshots land in `qa/shots/`.
 *
 * The microphone is Chromium's fake device fed from `qa/speech.wav`
 * (`espeak-ng … | ffmpeg`); without the file it falls back to the synthetic tone.
 */
const here = fileURLToPath(new URL('.', import.meta.url));
export const QA_BASE = process.env['QA_BASE'] ?? 'https://vppillai.github.io/chintan/dev/';
export const SHOTS = `${here}shots`;
const speech = `${here}speech.wav`;

const mediaArgs = [
  '--use-fake-ui-for-media-stream',
  '--use-fake-device-for-media-stream',
  `--use-file-for-fake-audio-capture=${speech}`,
  '--autoplay-policy=no-user-gesture-required',
];

const mobile = {
  ...devices['Pixel 7'],
  // A Pixel 8 Pro.
  viewport: { width: 412, height: 915 },
  deviceScaleFactor: 2.625,
  isMobile: true,
  hasTouch: true,
};

const desktop = {
  ...devices['Desktop Chrome'],
  viewport: { width: 1280, height: 800 },
};

export default defineConfig({
  testDir: './tests',
  outputDir: './results',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 180_000,
  expect: { timeout: 10_000 },
  reporter: [['list'], ['json', { outputFile: 'results/report.json' }]],
  use: {
    baseURL: QA_BASE,
    permissions: ['microphone'],
    trace: 'retain-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    launchOptions: { args: mediaArgs },
  },
  projects: [
    { name: 'setup-mobile', testMatch: /auth\.setup\.ts/, use: { ...mobile } },
    { name: 'setup-desktop', testMatch: /auth\.setup\.ts/, use: { ...desktop } },
    {
      name: 'mobile',
      testIgnore: /auth\.setup\.ts/,
      dependencies: ['setup-mobile'],
      use: { ...mobile, storageState: `${here}state/mobile.json` },
    },
    {
      name: 'desktop',
      testIgnore: /auth\.setup\.ts/,
      dependencies: ['setup-desktop'],
      use: { ...desktop, storageState: `${here}state/desktop.json` },
    },
  ],
});
