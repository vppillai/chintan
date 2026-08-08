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
 * Kept out of `src/` deliberately: it carries the theme colours as literals —
 * a manifest cannot reference a CSS custom property — and `src/` is where the
 * token lint forbids exactly that.
 *
 * **The values below are hardcoded, and `config/instances/*.yaml` has a `pwa:`
 * block that looks as though it sets them.** It does not: nothing in
 * `scripts/` forwards it as a `VITE_*` variable, so it has never reached a
 * build. The two disagree today — the config says `theme_color: "#1B4332"` and
 * `description: Voice brain-dump notes`, neither of which is what ships. This
 * is the parity audit's L14, still true. Wiring it up means exporting the block
 * from the build script and reading it here; until someone does, this file is
 * the only source and the YAML is decoration.
 */

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

export function chintanManifest(base: string) {
  const at = (path: string): string => basedPath(base, path);

  return {
    name: 'Chintan',
    short_name: 'Chintan',
    description: 'Speak a thought. It files itself.',
    /*
     * Absolute, not ".". A relative `start_url` resolves against the manifest's
     * own URL, so launching the installed icon depended on where the manifest
     * happened to be fetched from — and `scope` decides which navigations stay
     * in the installed app at all. Neither is worth leaving to inference.
     */
    start_url: at(''),
    scope: at(''),
    display: 'standalone' as const,
    background_color: '#FBF9F4',
    theme_color: '#FBF9F4',
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
