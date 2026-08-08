import '@testing-library/jest-dom/vitest';
// jsdom has no IndexedDB. The capture buffer and the offline queue are storage
// code, and testing them against a mock of the API rather than a real one would
// test the mock — fake-indexeddb is a real implementation of the spec.
import 'fake-indexeddb/auto';

import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';

/**
 * jsdom supplies its own `AbortController`/`AbortSignal` but no `Request`, so
 * under this environment the global `Request` is the runtime's native one and
 * rejects a jsdom signal with "signal is not of type AbortSignal". React
 * Router's data router constructs exactly that pair on every navigation, which
 * would make the entire routing suite unrunnable.
 *
 * Bridge them: build the Request without the signal, then shadow the native
 * `signal` getter with the jsdom one. Nothing in the app reads `Request` — this
 * exists solely so react-router can navigate under test.
 */
function bridgeAbortSignal(): void {
  const NativeRequest = globalThis.Request;
  if (!NativeRequest) return;
  const nativeSignal = new NativeRequest('http://localhost/').signal;
  if (nativeSignal instanceof AbortSignal) return; // Already compatible.

  class BridgedRequest extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      const signal = init?.signal;
      const rest: RequestInit = { ...init };
      delete rest.signal;
      super(input, rest);
      if (signal) {
        Object.defineProperty(this, 'signal', { value: signal, configurable: true });
      }
    }
  }

  vi.stubGlobal('Request', BridgedRequest);
}

bridgeAbortSignal();

/**
 * jsdom implements no media queries at all, so `window.matchMedia` is
 * undefined and anything that reads `prefers-color-scheme` throws. This stub
 * is controllable: `setSystemPrefersDark` drives it, which is how the theme
 * hook's `system` mode is tested.
 */
type Listener = (event: MediaQueryListEvent) => void;

const listeners = new Map<string, Set<Listener>>();
let prefersDark = false;

export function setSystemPrefersDark(next: boolean): void {
  prefersDark = next;
  for (const [query, set] of listeners) {
    const matches = matchesQuery(query);
    for (const listener of set) {
      listener({ matches, media: query } as MediaQueryListEvent);
    }
  }
}

function matchesQuery(query: string): boolean {
  if (query.includes('prefers-color-scheme: dark')) return prefersDark;
  if (query.includes('prefers-color-scheme: light')) return !prefersDark;
  return false;
}

function installMatchMedia(): void {
  vi.stubGlobal('matchMedia', (query: string): MediaQueryList => {
    if (!listeners.has(query)) listeners.set(query, new Set());
    const set = listeners.get(query);
    return {
      get matches() {
        return matchesQuery(query);
      },
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: Listener) => {
        set?.add(listener);
      },
      removeEventListener: (_type: string, listener: Listener) => {
        set?.delete(listener);
      },
      addListener: (listener: Listener) => {
        set?.add(listener);
      },
      removeListener: (listener: Listener) => {
        set?.delete(listener);
      },
      dispatchEvent: () => false,
    } as MediaQueryList;
  });
}

beforeEach(() => {
  listeners.clear();
  prefersDark = false;
  window.localStorage.clear();
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.removeAttribute('data-resolved-theme');
  installMatchMedia();
});

afterEach(() => {
  cleanup();
});
