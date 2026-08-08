import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';

import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';
import { defineConfig } from 'vitest/config';

/**
 * `base` is what makes the bundle deployable to a GitHub Pages sub-path
 * (`https://<owner>.github.io/<repo>/<instance>/`). CI sets `VITE_BASE` per
 * instance; local dev and `vite preview` default to the root.
 *
 * It is normalised to a trailing slash because Vite resolves asset URLs by
 * string concatenation, and a missing slash silently produces `/instanceassets/…`.
 */
function resolveBase(raw: string | undefined): string {
  if (!raw || raw === '/') return '/';
  const withLeading = raw.startsWith('/') ? raw : `/${raw}`;
  return withLeading.endsWith('/') ? withLeading : `${withLeading}/`;
}

export default defineConfig(({ mode }) => ({
  base: resolveBase(process.env['VITE_BASE']),
  plugins: [
    react(),
    /*
     * injectManifest, not generateSW. The app needs a network-first shell, an
     * update flow that does NOT call skipWaiting on install, and a real
     * Background Sync handler; the generated recipes fight all three. See
     * src/sw.ts.
     */
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.ts',
      registerType: 'prompt',
      injectRegister: 'auto',
      // The dev server does not need a worker, and a stale one during
      // development is a debugging trap.
      devOptions: { enabled: false },
      injectManifest: {
        globPatterns: ['**/*.{js,css,html,png,svg,woff2}'],
      },
      manifest: {
        name: 'Chintan',
        short_name: 'Chintan',
        description: 'Speak a thought. It files itself.',
        start_url: '.',
        scope: '.',
        display: 'standalone',
        // Unlocked on purpose: a car mount is landscape, and the stated use
        // case is hands-free. v1 pinned portrait-primary.
        orientation: 'any',
        background_color: '#FBF9F4',
        theme_color: '#FBF9F4',
        icons: [
          { src: 'icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: 'icon-512.png', sizes: '512x512', type: 'image/png' },
          {
            src: 'icon-maskable-192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'maskable',
          },
          {
            src: 'icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
        shortcuts: [
          {
            name: 'Record a thought',
            short_name: 'Record',
            description: 'Start recording immediately',
            url: 'capture',
            icons: [{ src: 'icon-192.png', sizes: '192x192' }],
          },
        ],
      },
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: 'dist',
    // Content-hashed assets are a precondition for the Workbox precache in a
    // later phase: a deploy must never be able to strand an installed client.
    assetsDir: 'assets',
    sourcemap: mode !== 'production',
    target: 'es2022',
  },
  server: {
    port: 5173,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    // e2e/ is Playwright's; vitest must not try to run it.
    include: ['src/**/*.test.{ts,tsx}'],
    exclude: ['e2e/**', 'node_modules/**', 'dist/**'],
    restoreMocks: true,
  },
}));
