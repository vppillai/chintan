import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';

import react from '@vitejs/plugin-react';
import { chintanManifest } from './manifest.config.ts';
import { resolveIdentity } from './src/config/identity.ts';
import { VitePWA } from 'vite-plugin-pwa';
import { loadEnv, type HtmlTagDescriptor, type Plugin } from 'vite';
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

/**
 * The `VITE_*` environment as the build sees it: `.env` files for the mode,
 * overridden by the process environment, which is where the deploy script and
 * the Playwright config put their values.
 */
function viteEnv(mode: string): Record<string, string | undefined> {
  return { ...loadEnv(mode, process.cwd(), 'VITE_'), ...process.env };
}

/**
 * `<link rel="preconnect">` for the two origins every launch talks to.
 *
 * The API and Cognito origins are known at build time (the same VITE_* the
 * bundle bakes in), so the HTML can open those connections while the module
 * graph is still downloading rather than after React has mounted and asked
 * for the notes. On the capture shortcut over a cellular link that is a DNS
 * lookup, a TCP handshake and a TLS handshake — several hundred milliseconds —
 * taken off the first request. `crossorigin`, because the API is fetched in
 * CORS mode and a preconnect without it opens a connection the fetch cannot
 * use. Emitted only for origins that are actually configured: an unset
 * variable is not a hint worth giving.
 */
function preconnect(mode: string): Plugin {
  const env = viteEnv(mode);
  const origins = [env['VITE_API_URL'], env['VITE_COGNITO_DOMAIN']]
    .map((value) => {
      try {
        return value ? new URL(value).origin : null;
      } catch {
        return null;
      }
    })
    .filter((origin): origin is string => origin !== null);
  return {
    name: 'chintan:preconnect',
    transformIndexHtml: (): HtmlTagDescriptor[] =>
      [...new Set(origins)].map((href) => ({
        tag: 'link',
        attrs: { rel: 'preconnect', href, crossorigin: '' },
        injectTo: 'head-prepend',
      })),
  };
}

export default defineConfig(({ mode }) => {
  const base = resolveBase(process.env['VITE_BASE']);
  /*
   * The app's name and description, from the instance's YAML by way of the
   * deploy script's VITE_APP_* exports, with the product defaults for a build
   * nothing drives. Resolved once and handed to every consumer: the manifest
   * below, and — through `define` — both `%VITE_APP_NAME%` in index.html and
   * `import.meta.env.VITE_APP_NAME` in the bundle. Vite's `%ENV%` substitution
   * reads `define`d `import.meta.env.*` keys as well as the real environment,
   * which is what lets the default reach the title: without it an undriven
   * build would ship the placeholder text verbatim.
   */
  const identity = resolveIdentity(viteEnv(mode));

  return {
    base,
    define: {
      'import.meta.env.VITE_APP_NAME': JSON.stringify(identity.name),
      'import.meta.env.VITE_APP_SHORT_NAME': JSON.stringify(identity.shortName),
      'import.meta.env.VITE_APP_DESCRIPTION': JSON.stringify(identity.description),
    },
    plugins: [
      preconnect(mode),
      react(),
      /*
       * injectManifest, not generateSW. The app needs a network-first shell and
       * an update flow that does NOT call skipWaiting on install; the generated
       * recipes fight both. See src/sw.ts.
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
        manifest: chintanManifest(base, identity),
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
  };
});
