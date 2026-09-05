/**
 * What the app calls itself, and the one sentence under that name.
 *
 * The instance's YAML (`config/instances/<instance>.yaml`) is the single source
 * of these: `scripts/ci-build-site.sh` exports them as `VITE_APP_NAME`,
 * `VITE_APP_SHORT_NAME` and `VITE_APP_DESCRIPTION`, and everything that shows
 * the name — the document title, the PWA manifest, the shell's wordmark, the
 * heading of About — derives from those at build time. Nothing here is a
 * runtime setting.
 *
 * The defaults exist for builds that no instance config drives: CI's compile
 * check, the Playwright run and a developer's `bun run dev`. They are the
 * product's own name and tagline rather than placeholders, so an undriven
 * build is still a presentable one, but they are not what a deploy ships.
 *
 * Imported by `vite.config.ts` as well as by the bundle, which is why this
 * module is plain TypeScript with no `import.meta.env` in it: under Node,
 * inside the config, there is no such object.
 */

export const DEFAULT_APP_NAME = 'Chintan';
export const DEFAULT_APP_DESCRIPTION = 'Speak a thought. It files itself.';

export interface AppIdentity {
  /** The document title, the manifest `name`, the wordmark, the About heading. */
  name: string;
  /**
   * The label under an installed icon. Launchers show about twelve characters
   * before truncating, which `scripts/list-instances.sh` enforces on the YAML.
   */
  shortName: string;
  /** `<meta name="description">`, the manifest `description`, the About lede. */
  description: string;
}

/** A value from the environment, or the default when it is unset or blank. */
function chosen(value: string | undefined, fallback: string): string {
  const trimmed = value?.trim() ?? '';
  return trimmed.length > 0 ? trimmed : fallback;
}

/**
 * Resolves the identity from a `VITE_*` environment, applying the defaults.
 *
 * Used by `vite.config.ts` to feed the manifest and to `define` the same three
 * variables for `index.html`'s `%VITE_APP_NAME%` substitution and the bundle's
 * `import.meta.env`, so every consumer sees one answer — including the
 * fallback, which would otherwise leave a literal `%VITE_APP_NAME%` in the
 * title of an undriven build.
 */
export function resolveIdentity(env: Record<string, string | undefined>): AppIdentity {
  const name = chosen(env['VITE_APP_NAME'], DEFAULT_APP_NAME);
  return {
    name,
    shortName: chosen(env['VITE_APP_SHORT_NAME'], name),
    description: chosen(env['VITE_APP_DESCRIPTION'], DEFAULT_APP_DESCRIPTION),
  };
}
