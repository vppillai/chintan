import process from 'node:process';

import { defineConfig, devices } from '@playwright/test';

/** Where the sub-path build is served; `scope.spec.ts` sets it as its baseURL. */
export const SCOPED_BASE_URL = 'http://127.0.0.1:4174/chintan/dev/';

/**
 * End-to-end configuration.
 *
 * The app is served from a production build, not the dev server: the service
 * worker, the precache manifest, and the hashed asset paths only exist in a
 * real build, and three of the flows under test depend on them.
 *
 * The API is stubbed by route interception rather than by running the Go
 * backend, so these specs cover the frontend contract — what the client sends
 * and how it behaves on each reply — and stay runnable without AWS.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: Boolean(process.env['CI']),
  retries: process.env['CI'] ? 1 : 0,
  ...(process.env['CI'] ? { workers: 2 } : {}),
  reporter: process.env['CI'] ? [['github'], ['list']] : [['list']],
  timeout: 30_000,
  expect: { timeout: 7_500 },

  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // The capture specs need a microphone without a permission dialog.
        permissions: ['microphone'],
        launchOptions: {
          args: [
            '--use-fake-ui-for-media-stream',
            // Feeds MediaRecorder a synthetic tone, so a recording produces
            // real encoded bytes rather than silence.
            '--use-fake-device-for-media-stream',
            '--autoplay-policy=no-user-gesture-required',
          ],
        },
      },
    },
    /*
     * WebKit, for everything that does not need a microphone.
     *
     * This is an iOS product and the suite was Chromium-only, which is a false
     * comfort: WebKit's IndexedDB, media element, `history.state` and layout
     * engine are the ones the owner actually uses. Playwright's WebKit has no
     * fake media device and no `microphone` permission, so the capture and
     * offline-recording specs stay on Chromium; sign-in, the library, the
     * archive, playback, accessibility and one layout viewport run here too.
     *
     * The layout matrix is eleven viewports by two themes; one phone in one
     * theme is enough to catch a WebKit-only regression, and the rest is
     * Chromium's to sweep.
     *
     * The service worker is blocked here. Playwright's route interception does
     * not see requests that pass through a worker's `fetch` handler outside
     * Chromium, so once `sw.js` had installed and claimed the page every API
     * call went to the real preview server and came back 404 — the note screen
     * read "Note not found" for a note the stub was holding. The worker's own
     * behaviour is Chromium's to prove, in offline.spec.ts.
     */
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'], serviceWorkers: 'block' },
      testMatch: /(auth|archive|playback|a11y|manifest|layout)\.spec\.ts/,
      grepInvert:
        /layout\.spec\.ts.*(320x568|375x667|393x873|412x915|844x390|768x1024|1024x768|1280x800|1440x900|1920x1080|2560x1080|nocturne|capture ·|record button)/,
    },
  ],

  webServer: [
    {
      // `--host 127.0.0.1` is load-bearing: vite preview otherwise binds
      // `localhost`, which resolves to ::1 here, and Playwright's readiness probe
      // on 127.0.0.1 never connects — the run then sits until the timeout.
      command: 'bun run build && bun run preview -- --port 4173 --strictPort --host 127.0.0.1',
      url: 'http://127.0.0.1:4173',
      reuseExistingServer: !process.env['CI'],
      timeout: 120_000,
      env: {
        VITE_API_URL: 'http://127.0.0.1:4173/api',
        VITE_CLIENT_ID: 'e2e-client',
        VITE_USER_POOL_ID: 'e2e-pool',
        /*
         * A different origin, as the real hosted UI is.
         *
         * It was a path on the app's own origin, which is not merely unrealistic:
         * `sw.ts` claims every same-origin navigation, so the service worker
         * intercepted the redirect to `/cognito/logout`, served the SPA shell for
         * it, and the app landed on its own Not Found screen instead of Cognito.
         * Playwright fulfils the route without DNS, so nothing is ever resolved.
         */
        VITE_COGNITO_DOMAIN: 'https://cognito.e2e.test',
        VITE_INSTANCE: 'e2e',
        // Injected in CI from the git SHA. Pinned here so the footnote spec can
        // assert the value actually reaches the screen.
        VITE_VERSION: 'e2e-abc1234',
      },
    },
    /*
     * The same app built for a sub-path, as every real instance is
     * (`https://<owner>.github.io/chintan/<instance>/`).
     *
     * The root build above cannot show a scope bug: with `base: '/'` there is
     * no slash to lose. `scope.spec.ts` runs against this one and asserts that
     * every URL the app writes stays under `/chintan/dev/` — the manifest's
     * scope and the worker's — and that a filtered library reloads offline.
     */
    {
      command:
        'bun run build -- --outDir dist-scoped && bun run preview -- --outDir dist-scoped --port 4174 --strictPort --host 127.0.0.1',
      url: SCOPED_BASE_URL,
      reuseExistingServer: !process.env['CI'],
      timeout: 120_000,
      env: {
        VITE_BASE: '/chintan/dev/',
        VITE_API_URL: 'http://127.0.0.1:4174/api',
        VITE_CLIENT_ID: 'e2e-client',
        VITE_USER_POOL_ID: 'e2e-pool',
        VITE_COGNITO_DOMAIN: 'https://cognito.e2e.test',
        VITE_INSTANCE: 'e2e',
        VITE_VERSION: 'e2e-abc1234',
      },
    },
  ],
});
