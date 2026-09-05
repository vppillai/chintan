import {
  createContext,
  useCallback,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from 'react';

import {
  DARK_MEDIA_QUERY,
  DEFAULT_THEME,
  readStoredTheme,
  resolveTheme,
  writeStoredTheme,
  type ResolvedTheme,
  type ThemePreference,
} from './theme.ts';

export interface ThemeContextValue {
  /** What the user chose: 'ink' | 'nocturne' | 'system'. */
  preference: ThemePreference;
  /** What 'system' currently resolves to. Never 'system'. */
  resolved: ResolvedTheme;
  setPreference: (next: ThemePreference) => void;
}

export const ThemeContext = createContext<ThemeContextValue | null>(null);

function darkQuery(): MediaQueryList | null {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return null;
  }
  return window.matchMedia(DARK_MEDIA_QUERY);
}

/**
 * `prefers-color-scheme` is an external store, so it is read as one. This
 * subscribes unconditionally rather than only while the preference is
 * 'system': the user can switch to 'system' at any moment, and a lazily
 * attached listener renders one frame of the wrong resolved theme.
 */
function subscribeToSystemTheme(onChange: () => void): () => void {
  const query = darkQuery();
  if (!query) return () => {};
  query.addEventListener('change', onChange);
  return () => {
    query.removeEventListener('change', onChange);
  };
}

function systemPrefersDark(): boolean {
  return darkQuery()?.matches ?? false;
}

/**
 * The browser's chrome follows the theme too. `index.html` carries one
 * `theme-color` per colour scheme so the address bar is right before the
 * stylesheet loads, but those are media queries, and an explicit Nocturne on
 * a light-mode device would leave the chrome pale. Once the theme attribute
 * is on the root, `--color-ground` resolves to the ground actually painted,
 * and both tags are set to it; a stylesheet not yet parsed gives an empty
 * value, and the media-selected defaults stand until the next run.
 */
function paintBrowserChrome(root: HTMLElement): void {
  const ground = getComputedStyle(root).getPropertyValue('--color-ground').trim();
  if (!ground) return;
  for (const meta of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) {
    meta.content = ground;
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<ThemePreference>(() =>
    readStoredTheme(typeof window === 'undefined' ? undefined : window.localStorage),
  );

  const prefersDark = useSyncExternalStore(
    subscribeToSystemTheme,
    systemPrefersDark,
    () => false,
  );

  const resolved = resolveTheme(preference, prefersDark);

  // The *preference* goes on <html>, not the resolved value: tokens.css
  // resolves 'system' with a media query, so the paint is already correct
  // before this effect runs. `data-resolved-theme` is published alongside it
  // for anything that needs the answer in JS.
  useEffect(() => {
    const root = document.documentElement;
    root.setAttribute('data-theme', preference);
    root.setAttribute('data-resolved-theme', resolved);
    paintBrowserChrome(root);
  }, [preference, resolved]);

  const setPreference = useCallback((next: ThemePreference) => {
    setPreferenceState(next);
    writeStoredTheme(
      typeof window === 'undefined' ? undefined : window.localStorage,
      next,
    );
  }, []);

  const value = useMemo<ThemeContextValue>(
    () => ({ preference, resolved, setPreference }),
    [preference, resolved, setPreference],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export { DEFAULT_THEME };
