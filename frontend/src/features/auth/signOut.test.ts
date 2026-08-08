import { QueryClient } from '@tanstack/react-query';
import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
import { Session } from '@/api/session.ts';
import { createMemoryTokenStore, type TokenSet } from '@/api/tokens.ts';
import { saveCaptureRecord } from '@/features/capture/buffer.ts';
import { openChintanDB, resetDatabaseHandle } from '@/offline/db.ts';
import { enqueue } from '@/offline/queue.ts';

import { hasUnsentWork, performSignOut, readUnsentWork } from './signOut.ts';

const TOKENS: TokenSet = {
  idToken: 'id-1',
  accessToken: 'access-1',
  refreshToken: 'refresh-1',
  expiresAt: Date.now() + 3_600_000,
  tokenType: 'Bearer',
};

function harness(overrides: { webauthnDisable?: () => Promise<void> } = {}) {
  const store = createMemoryTokenStore(TOKENS);
  const session = new Session(store, { refresh: () => Promise.reject(new Error('no')) });
  const queryClient = new QueryClient();
  const navigated: string[] = [];
  const webauthnDisable = vi.fn(overrides.webauthnDisable ?? (async () => {}));

  return {
    store,
    session,
    queryClient,
    navigated,
    webauthnDisable,
    api: { webauthnDisable } as unknown as ChintanApi,
    navigate: (url: string) => navigated.push(url),
  };
}

beforeEach(() => {
  // A fresh database per test, the same way the other storage suites do it.
  // `deleteDatabase` blocks on the handle `openChintanDB` is still holding.
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
});

describe('what signing out has to leave behind', () => {
  it('drops the token from memory and from storage', async () => {
    const h = harness();
    expect(h.session.isAuthenticated()).toBe(true);

    await performSignOut({ ...h, revokeBiometric: false });

    expect(h.session.isAuthenticated()).toBe(false);
    expect(h.store.read()).toBeNull();
  });

  it('empties the cached note corpus', async () => {
    const h = harness();
    h.queryClient.setQueryData(['notes', { state: 'active' }], { items: [{ id: 'roof' }] });

    await performSignOut({ ...h, revokeBiometric: false });

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

    await performSignOut({ ...h, revokeBiometric: false });

    const db = await openChintanDB();
    expect(await db.count('captures')).toBe(0);
    expect(await db.count('mutations')).toBe(0);
    expect(await db.count('captureChunks')).toBe(0);
  });
});

describe('the biometric credential', () => {
  it('is destroyed when the device is enrolled', async () => {
    /*
     * Biometric unlock vaults a Cognito refresh token server-side. Left
     * enrolled, the next person to pick up the phone taps unlock and is signed
     * straight back in — the sign-out would be cosmetic.
     */
    const h = harness();
    await performSignOut({ ...h, revokeBiometric: true });
    expect(h.webauthnDisable).toHaveBeenCalledTimes(1);
  });

  it('is left alone when nothing is enrolled', async () => {
    const h = harness();
    await performSignOut({ ...h, revokeBiometric: false });
    expect(h.webauthnDisable).not.toHaveBeenCalled();
  });

  it('does not block the sign-out when the server cannot be reached', async () => {
    // Being offline is not a reason to leave a live session on a phone that is
    // being handed over. The user is told instead.
    const h = harness({
      webauthnDisable: async () => {
        throw new Error('offline');
      },
    });

    const result = await performSignOut({ ...h, revokeBiometric: true });

    expect(result.biometricLeftBehind).toBe(true);
    expect(h.session.isAuthenticated()).toBe(false);
  });

  it('is revoked while the token still exists', async () => {
    // Order matters: it is an authenticated call.
    const seen: (boolean | string)[] = [];
    const h = harness({
      webauthnDisable: async () => {
        seen.push('revoke');
      },
    });
    const realClear = h.session.clear.bind(h.session);
    h.session.clear = () => {
      seen.push('clear');
      realClear();
    };

    await performSignOut({ ...h, revokeBiometric: true });

    expect(seen).toEqual(['revoke', 'clear']);
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
