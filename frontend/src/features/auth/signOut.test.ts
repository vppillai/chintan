import { QueryClient } from '@tanstack/react-query';
import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { Session } from '@/api/session.ts';
import { createMemoryTokenStore, type TokenSet } from '@/api/tokens.ts';
import { COST_NOTE_KEY } from '@/features/ask/costNote.ts';
import { THREAD_KEY } from '@/features/ask/thread.ts';
import { saveCaptureRecord } from '@/features/capture/buffer.ts';
import { DISMISSED_KEY } from '@/features/capture/dismissed.ts';
import { TARGETED_KEY } from '@/features/capture/targeted.ts';
import { noteTabStorageKey } from '@/features/notes/NoteTabs.tsx';
import { openChintanDB, resetDatabaseHandle } from '@/offline/db.ts';
import { enqueue } from '@/offline/queue.ts';
import { THEME_STORAGE_KEY } from '@/theme/theme.ts';

import { PASSKEY_NUDGE_KEY } from './passkeys.ts';
import { hasUnsentWork, performSignOut, readUnsentWork } from './signOut.ts';

/*
 * A configured build. Under Vitest no `VITE_*` variables are set, so the
 * Cognito half of the sign-out — the revoke and the `/logout` redirect — is
 * skipped as it should be for a build with nowhere to send anyone. These
 * tests are about that half.
 */
vi.mock('@/config/env.ts', () => ({
  config: {
    apiUrl: 'https://api.test',
    userPoolId: 'us-west-2_test',
    clientId: 'client-abc',
    cognitoDomain: 'https://cognito.test',
    instance: 'dev',
    appName: 'Chintan',
    appDescription: 'Speak a thought. It files itself.',
  },
  isConfigured: () => true,
}));

/** Cognito's `/oauth2/revoke` answers an empty 200. */
const revokeOk = async (): Promise<Response> => new Response(null, { status: 200 });

const TOKENS: TokenSet = {
  idToken: 'id-1',
  accessToken: 'access-1',
  refreshToken: 'refresh-1',
  expiresAt: Date.now() + 3_600_000,
  tokenType: 'Bearer',
};

function harness(tokens: TokenSet = TOKENS) {
  const store = createMemoryTokenStore(tokens);
  const session = new Session(store, { refresh: () => Promise.reject(new Error('no')) });
  const queryClient = new QueryClient();
  const navigated: string[] = [];

  return {
    store,
    session,
    queryClient,
    navigated,
    navigate: (url: string) => navigated.push(url),
    fetchImpl: vi.fn<typeof fetch>(revokeOk),
  };
}

beforeEach(() => {
  // A fresh database per test, the same way the other storage suites do it.
  // `deleteDatabase` blocks on the handle `openChintanDB` is still holding.
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
  sessionStorage.clear();
  localStorage.clear();
});

describe('what signing out has to leave behind', () => {
  it('drops the token from memory and from storage', async () => {
    const h = harness();
    expect(h.session.isAuthenticated()).toBe(true);

    await performSignOut(h);

    expect(h.session.isAuthenticated()).toBe(false);
    expect(h.store.read()).toBeNull();
  });

  it('empties the cached note corpus', async () => {
    const h = harness();
    h.queryClient.setQueryData(['notes', { state: 'active' }], { items: [{ id: 'roof' }] });

    await performSignOut(h);

    expect(h.queryClient.getQueryData(['notes', { state: 'active' }])).toBeUndefined();
  });

  it('empties every IndexedDB store, so nothing of one person is left for the next', async () => {
    const h = harness();
    await saveCaptureRecord({
      localId: 'cap-1',
      serverCaptureId: null,
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 4_000,
      bytes: 1_024,
      chunkCount: 2,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: null,
    });
    await enqueue({ kind: 'updateNote', payload: { noteId: 'roof', body: {} } });

    await performSignOut(h);

    const db = await openChintanDB();
    expect(await db.count('captures')).toBe(0);
    expect(await db.count('mutations')).toBe(0);
    expect(await db.count('captureChunks')).toBe(0);
  });

  it('empties the app’s session storage — the Ask thread, the remembered note tabs — which the same tab carries to the next sign-in', async () => {
    const h = harness();
    sessionStorage.setItem(THREAD_KEY, JSON.stringify([{ key: 'k', question: 'what did I decide about the roof?' }]));
    sessionStorage.setItem(noteTabStorageKey('roof'), 'cleaned');
    sessionStorage.setItem('chintan.something.new', 'x');
    sessionStorage.setItem('unrelated', 'left alone');

    await performSignOut(h);

    expect(sessionStorage.getItem(THREAD_KEY)).toBeNull();
    expect(sessionStorage.getItem(noteTabStorageKey('roof'))).toBeNull();
    expect(sessionStorage.getItem('chintan.something.new')).toBeNull();
    expect(sessionStorage.getItem('unrelated')).toBe('left alone');
  });

  it('drops what in local storage names one person’s activity, and keeps the device’s own preferences', async () => {
    const h = harness();
    localStorage.setItem(COST_NOTE_KEY, '1');
    localStorage.setItem(DISMISSED_KEY, JSON.stringify(['cap-1']));
    localStorage.setItem(TARGETED_KEY, JSON.stringify(['cap-2']));
    localStorage.setItem(THEME_STORAGE_KEY, 'nocturne');
    localStorage.setItem(PASSKEY_NUDGE_KEY, 'not-now');

    await performSignOut(h);

    expect(localStorage.getItem(COST_NOTE_KEY)).toBeNull();
    expect(localStorage.getItem(DISMISSED_KEY)).toBeNull();
    expect(localStorage.getItem(TARGETED_KEY)).toBeNull();
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('nocturne');
    expect(localStorage.getItem(PASSKEY_NUDGE_KEY)).toBe('not-now');
  });
});

describe('the refresh token is revoked, not just forgotten', () => {
  it('posts the refresh token to Cognito’s revoke endpoint before leaving', async () => {
    // `/logout` ends the hosted UI's cookie and nothing else. A refresh token
    // copied off a lost or handed-over phone before the sign-out would go on
    // minting access tokens for thirty days; this is the call that stops it.
    const h = harness();

    await performSignOut(h);

    const [url, init] = h.fetchImpl.mock.calls[0] ?? [];
    expect(String(url)).toBe('https://cognito.test/oauth2/revoke');
    expect(init?.method).toBe('POST');
    expect((init?.headers as Record<string, string>)['Content-Type']).toBe(
      'application/x-www-form-urlencoded',
    );
    const body = new URLSearchParams(String(init?.body));
    expect(body.get('token')).toBe('refresh-1');
    expect(body.get('client_id')).toBe('client-abc');
    expect(h.navigated[0]).toContain('https://cognito.test/logout');
  });

  it('clears the device before the network is asked anything', async () => {
    const h = harness();
    let tokenAtRevoke: TokenSet | null = TOKENS;
    h.fetchImpl.mockImplementation(async () => {
      tokenAtRevoke = h.store.read();
      return revokeOk();
    });

    await performSignOut(h);

    expect(tokenAtRevoke).toBeNull();
  });

  it('still signs out when the revoke cannot be delivered', async () => {
    // Offline, or Cognito unreachable: the local half is the one that must
    // never wait on the network, and the token ages out on its own.
    const h = harness();
    h.fetchImpl.mockRejectedValue(new TypeError('Failed to fetch'));

    await expect(performSignOut(h)).resolves.toBeUndefined();

    expect(h.store.read()).toBeNull();
    expect(h.navigated[0]).toContain('/logout');
  });

  it('has nothing to revoke when the set holds no refresh token', async () => {
    const h = harness({ ...TOKENS, refreshToken: null });

    await performSignOut(h);

    expect(h.fetchImpl).not.toHaveBeenCalled();
    expect(h.navigated[0]).toContain('/logout');
  });
});

describe('unsent work is counted before anything is destroyed', () => {
  it('counts recordings the server has never acknowledged', async () => {
    await saveCaptureRecord({
      localId: 'cap-1',
      serverCaptureId: null,
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 4_000,
      bytes: 1_024,
      chunkCount: 2,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: null,
    });

    const work = await readUnsentWork();
    expect(work.captures).toBe(1);
    expect(hasUnsentWork(work)).toBe(true);
  });

  it('does not count a recording the server already has', async () => {
    await saveCaptureRecord({
      localId: 'cap-2',
      serverCaptureId: 'srv-2',
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 4_000,
      bytes: 1_024,
      chunkCount: 2,
      createdAt: Date.now(),
      uploadedAt: Date.now(),
      peaks: null,
    });

    expect((await readUnsentWork()).captures).toBe(0);
  });

  it('counts queued mutations', async () => {
    await enqueue({ kind: 'updateNote', payload: { noteId: 'roof', body: {} } });
    const work = await readUnsentWork();
    expect(work.queued).toBe(1);
    expect(hasUnsentWork(work)).toBe(true);
  });

  it('is nothing to warn about on a clean device', async () => {
    const work = await readUnsentWork();
    expect(work).toEqual({ captures: 0, queued: 0 });
    expect(hasUnsentWork(work)).toBe(false);
  });
});
