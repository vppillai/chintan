import { afterEach, describe, expect, it } from 'vitest';

import { PENDING_TTL_MS, setPending, takePending } from './useTabNavigation.ts';

/*
 * The pending half of a multi-entry tab move (`goHome`, `goTab` from deep in
 * the stack): set at one entry, taken once the POP has landed on another.
 * `history.go(-n)` is a silent no-op when the router's index overstates the
 * real stack, so a pending taken at the entry it was set from — the next
 * reload's initial POP — must be dropped, not acted on (FE-14).
 */
describe('the pending tab move', () => {
  afterEach(() => {
    sessionStorage.clear();
    takePending('reset');
  });

  it('is taken once at another entry, and is gone after', () => {
    setPending('/settings', 'from-key', 1_000);
    expect(takePending('landed-key', 2_000)).toBe('/settings');
    expect(takePending('landed-key', 2_000)).toBeNull();
    expect(sessionStorage.getItem('chintan.nav.pending')).toBeNull();
  });

  it('is dropped at the entry it was set from: the Back never moved', () => {
    setPending('/settings', 'from-key', 1_000);
    expect(takePending('from-key', 2_000)).toBeNull();
    // Dropped for good, not left for the next POP.
    expect(takePending('landed-key', 3_000)).toBeNull();
  });

  it('expires', () => {
    setPending('/settings', 'from-key', 1_000);
    expect(takePending('landed-key', 1_000 + PENDING_TTL_MS + 1)).toBeNull();
  });

  it('survives a reload through sessionStorage', () => {
    setPending('/?view=archived', 'from-key', 1_000);
    // What a reload leaves: the store, not the module's memory.
    const stored = sessionStorage.getItem('chintan.nav.pending');
    takePending('from-key', 1_000);
    sessionStorage.setItem('chintan.nav.pending', stored ?? '');
    expect(takePending('landed-key', 2_000)).toBe('/?view=archived');
  });
});
