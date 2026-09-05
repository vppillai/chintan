/**
 * The web app manifest, built from the deploy's base path.
 *
 * Everything in a manifest that names a URL — `start_url`, `scope`, every icon
 * `src`, every shortcut — used to be a bare relative path, and relative
 * resolution in a manifest depends on which URL the manifest was fetched from
 * and on trailing slashes. That is fine at the root and wrong the moment the
 * app is served from `/<repo>/<instance>/`, which is how every instance of this
 * app is served.
 *
 * `VITE_BASE` is known at build time and already produces correct asset URLs
 * for the bundle, so the manifest uses the same value and states every path
 * outright. There is nothing left to resolve.
 *
 * The name, short name and description come from the instance's YAML through
 * `VITE_APP_*` (see `src/config/identity.ts`); `vite.config.ts` resolves them
 * once and passes them in, so the manifest, the document title and the shell
 * cannot disagree. The colours do not come from the YAML, on purpose: they are
 * the design system's, below.
 *
 * Kept out of `src/` deliberately: it carries a colour as a literal — a
 * manifest cannot reference a CSS custom property — and `src/` is where the
 * token lint forbids exactly that.
 */

import type { AppIdentity } from './src/config/identity.ts';

/**
 * `--color-ground` from `src/styles/tokens.css`, Ink & Paper. The manifest's
 * `background_color` paints the splash behind the icon and `theme_color` the
 * window chrome before the stylesheet has loaded, so both must be the same
 * ground the first frame then paints — and a manifest is static JSON, so it
 * cannot follow the user's theme or read the token. One constant, here, kept in
 * step with the token by hand; not per instance, because an instance is not a
 * brand.
 */
const GROUND = '#fbf9f4';

export interface ManifestIcon {
  src: string;
  sizes: string;
  type?: string;
  purpose?: string;
}

/** Joins the base to a bundled asset, with exactly one slash between them. */
export function basedPath(base: string, path: string): string {
  const prefix = base.endsWith('/') ? base : `${base}/`;
  return `${prefix}${path.replace(/^\//, '')}`;
}

export function chintanManifest(base: string, identity: AppIdentity) {
  const at = (path: string): string => basedPath(base, path);

  return {
    name: identity.name,
    short_name: identity.shortName,
    description: identity.description,
    /*
     * Absolute, not ".". A relative `start_url` resolves against the manifest's
     * own URL, so launching the installed icon depended on where the manifest
     * happened to be fetched from — and `scope` decides which navigations stay
     * in the installed app at all. Neither is worth leaving to inference.
     */
    start_url: at(''),
    scope: at(''),
    display: 'standalone' as const,
    background_color: GROUND,
    theme_color: GROUND,
    // Unlocked on purpose: a car mount is landscape, and the stated use case is
    // hands-free. v1 pinned portrait-primary.
    orientation: 'any' as const,
    icons: [
      { src: at('icon-192.png'), sizes: '192x192', type: 'image/png' },
      { src: at('icon-512.png'), sizes: '512x512', type: 'image/png' },
      {
        src: at('icon-maskable-192.png'),
        sizes: '192x192',
        type: 'image/png',
        purpose: 'maskable' as const,
      },
      {
        src: at('icon-maskable-512.png'),
        sizes: '512x512',
        type: 'image/png',
        purpose: 'maskable' as const,
      },
    ] satisfies ManifestIcon[],
    shortcuts: [
      {
        name: 'Record a thought',
        short_name: 'Record',
        description: 'Start recording immediately',
        url: at('capture'),
        icons: [{ src: at('icon-192.png'), sizes: '192x192' }],
      },
    ],
  };
}
