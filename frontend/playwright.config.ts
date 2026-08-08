import process from 'node:process';

import { defineConfig, devices } from '@playwright/test';

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
    // The capture specs need a microphone without a permission dialog.
    permissions: ['microphone'],
    launchOptions: {
      args: [
        '--use-fake-ui-for-media-stream',
        // Feeds MediaRecorder a synthetic tone, so a recording produces real
        // encoded bytes rather than silence.
        '--use-fake-device-for-media-stream',
        '--autoplay-policy=no-user-gesture-required',
      ],
    },
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  webServer: {
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
      VITE_COGNITO_DOMAIN: 'http://127.0.0.1:4173/cognito',
      VITE_INSTANCE: 'e2e',
    },
  },
});
