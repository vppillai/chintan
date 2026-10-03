/**
 * The server's clock, as this device can best know it.
 *
 * Every filing rule that compares "now" with a timestamp the server wrote —
 * a capture's age, `isStuck`, `retryAccepted`, the poll's cadence — read the
 * device clock, so a phone twelve minutes fast called a fresh capture stuck
 * and a phone ten minutes slow withheld Retry past `retry_after`. The API
 * client reads the `Date` header of every response (`observeServerDate`)
 * and keeps the difference; `serverNow()` is the device clock corrected by
 * it, and is what those rules read. The last response wins, so a stale
 * reading is corrected by the next call. The header is stamped when the
 * server answers and read when the answer lands, so every reading says
 * the server is slightly behind by the response's travel time; that bias is
 * at most the floor: offsets under `SKEW_IGNORED_MS` are treated as none,
 * since the header is whole seconds and the response took time to arrive,
 * so a small offset is noise, not skew.
 *
 * Module state, not a provider: the clock is one fact about the device, and
 * a pure function in `filing/model.ts` has no hook to read a context from.
 */

/** Below this the header's one-second grain and the network's delay explain the offset. */
const SKEW_IGNORED_MS = 2_000;

let offsetMs = 0;

/** Reads the response's `Date` header and remembers how far the device clock is from the server's. */
export function observeServerDate(response: Response): void {
  const header = response.headers.get('date');
  if (!header) return;
  const at = Date.parse(header);
  if (!Number.isFinite(at)) return;
  const offset = at - Date.now();
  offsetMs = Math.abs(offset) < SKEW_IGNORED_MS ? 0 : offset;
}

/** The current instant on the server's clock, to the device's best knowledge. */
export function serverNow(): number {
  return Date.now() + offsetMs;
}

/** Forgets the offset. Tests only. */
export function resetServerClock(): void {
  offsetMs = 0;
}
