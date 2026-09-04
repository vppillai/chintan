/**
 * Theme preferences.
 *
 * `ink` and `nocturne` are explicit choices. `system` defers to
 * `prefers-color-scheme`, resolving to `ink` under light and `nocturne` under
 * dark. The preference — not the resolved value — is what lands on
 * `<html data-theme>`, so the CSS media query in tokens.css does the resolving
 * and the page cannot flash the wrong theme while JS boots.
 */

export const THEME_PREFERENCES = ['ink', 'nocturne', 'system'] as const;

export type ThemePreference = (typeof THEME_PREFERENCES)[number];

/** The two themes that actually exist. `system` always resolves to one of these. */
export type ResolvedTheme = 'ink' | 'nocturne';

export const DEFAULT_THEME: ThemePreference = 'ink';

export const THEME_STORAGE_KEY = 'chintan.theme';

export const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)';

export const THEME_LABELS: Record<ThemePreference, string> = {
  ink: 'Ink & Paper',
  nocturne: 'Nocturne',
  system: 'Follow system',
};

export function isThemePreference(value: unknown): value is ThemePreference {
  return (
    typeof value === 'string' &&
    (THEME_PREFERENCES as readonly string[]).includes(value)
  );
}

export function resolveTheme(
  preference: ThemePreference,
  systemPrefersDark: boolean,
): ResolvedTheme {
  if (preference === 'system') return systemPrefersDark ? 'nocturne' : 'ink';
  return preference;
}

/**
 * Reads the stored preference. Any unreadable or unrecognised value falls back
 * to the default rather than throwing — Safari private mode denies access to
 * localStorage entirely, and a theme is not worth a white screen over.
 */
export function readStoredTheme(storage: Storage | undefined): ThemePreference {
  if (!storage) return DEFAULT_THEME;
  try {
    const raw = storage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(raw) ? raw : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

export function writeStoredTheme(
  storage: Storage | undefined,
  preference: ThemePreference,
): void {
  if (!storage) return;
  try {
    storage.setItem(THEME_STORAGE_KEY, preference);
  } catch {
    /* Storage denied or full. The in-memory preference still applies. */
  }
}
