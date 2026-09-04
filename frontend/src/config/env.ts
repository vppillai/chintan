/**
 * Build-time configuration.
 *
 * Baked into the hashed bundle, never loaded from a separate `config.js` at
 * runtime. A separately fetched config file that the service worker precaches
 * can pin an installed client to a dead API endpoint permanently, with no way
 * to update it short of clearing site data. With the values in the bundle, a
 * new endpoint is a new bundle hash.
 */

export interface AppConfig {
  apiUrl: string;
  userPoolId: string;
  clientId: string;
  cognitoDomain: string;
  instance: string;
  /**
   * What is running, for a bug report to name.
   *
   * The git SHA, injected at build time — the only identifier that is not a
   * guess. Absent outside CI, where the honest answer is that this is a local
   * build rather than an empty string or `undefined` on screen.
   */
  version: string;
}

/** Shown wherever a build has no CI-injected version, which is every local one. */
export const LOCAL_VERSION = 'local build';

function required(value: string | undefined, name: string): string {
  if (value && value.length > 0) return value;
  // Loud in dev, and in production it means the deploy pipeline did not pass
  // the variable — which is a broken build, not a runtime condition to absorb.
  if (import.meta.env.DEV) {
    console.warn(`[config] ${name} is not set; API calls will fail.`);
  }
  return '';
}

function trimTrailingSlash(value: string): string {
  return value.endsWith('/') ? value.slice(0, -1) : value;
}

export const config: AppConfig = {
  apiUrl: trimTrailingSlash(required(import.meta.env.VITE_API_URL, 'VITE_API_URL')),
  userPoolId: required(import.meta.env.VITE_USER_POOL_ID, 'VITE_USER_POOL_ID'),
  clientId: required(import.meta.env.VITE_CLIENT_ID, 'VITE_CLIENT_ID'),
  cognitoDomain: trimTrailingSlash(
    required(import.meta.env.VITE_COGNITO_DOMAIN, 'VITE_COGNITO_DOMAIN'),
  ),
  instance: import.meta.env.VITE_INSTANCE ?? 'dev',
  version: import.meta.env.VITE_VERSION || LOCAL_VERSION,
};

export function isConfigured(candidate: AppConfig = config): boolean {
  return candidate.apiUrl.length > 0 && candidate.clientId.length > 0;
}
