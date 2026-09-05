import { afterEach, describe, expect, it, vi } from 'vitest';

import { DEFAULT_APP_DESCRIPTION, DEFAULT_APP_NAME, resolveIdentity } from './identity.ts';

/**
 * The identity is read once, at module load, so each case stubs the variables
 * and imports a fresh copy of `env.ts` rather than reading the shared one.
 */
async function loadConfig() {
  vi.resetModules();
  const { config } = await import('./env.ts');
  return config;
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('the app identity in config', () => {
  it('is what the build was given', async () => {
    vi.stubEnv('VITE_APP_NAME', 'Chintan (staging)');
    vi.stubEnv('VITE_APP_DESCRIPTION', 'A sentence chosen by the instance.');

    const config = await loadConfig();

    expect(config.appName).toBe('Chintan (staging)');
    expect(config.appDescription).toBe('A sentence chosen by the instance.');
  });

  it('falls back to the product defaults, and says so, when the build was not driven', async () => {
    /*
     * CI's compile check and the Playwright run build with no instance config
     * behind them. They must still produce a presentable app, and a developer
     * must be able to see in the console that the name is a default rather
     * than a deploy's.
     */
    vi.stubEnv('VITE_APP_NAME', '');
    vi.stubEnv('VITE_APP_DESCRIPTION', '   ');
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});

    const config = await loadConfig();

    expect(config.appName).toBe(DEFAULT_APP_NAME);
    expect(config.appDescription).toBe(DEFAULT_APP_DESCRIPTION);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('VITE_APP_NAME is not set'));
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining('VITE_APP_DESCRIPTION is not set'),
    );
  });
});

describe('resolveIdentity, as vite.config.ts uses it', () => {
  it('derives the short name from the name unless one is given', () => {
    expect(resolveIdentity({ VITE_APP_NAME: 'Chintan (staging)' }).shortName).toBe(
      'Chintan (staging)',
    );
    expect(
      resolveIdentity({ VITE_APP_NAME: 'Chintan (staging)', VITE_APP_SHORT_NAME: 'Chintan stg' })
        .shortName,
    ).toBe('Chintan stg');
  });

  it('treats blank as unset, so a stray space in a workflow cannot blank the title', () => {
    expect(resolveIdentity({ VITE_APP_NAME: ' ', VITE_APP_DESCRIPTION: '' })).toEqual({
      name: DEFAULT_APP_NAME,
      shortName: DEFAULT_APP_NAME,
      description: DEFAULT_APP_DESCRIPTION,
    });
  });
});
