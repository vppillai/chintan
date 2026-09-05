/**
 * Captures whose destination a person chose, remembered on the device.
 *
 * A recording made *into* a note — "Record into this", `?note=`, the target
 * chooser — is already visible in that note's Recordings tab, from "Uploading"
 * through to the finished row (N3). The library's filing row listed it as
 * well, so ten recordings into one note left Home a wall of "Filed · Open the
 * note · Dismiss" receipts for things the user had watched land somewhere
 * else. Home shows only captures the router had to place.
 *
 * The server says which is which (`CaptureWire.targeted`). This set is the
 * fallback while backends that predate the field are in service: the uploader
 * writes the id here the moment `POST /v1/captures` answers for a request
 * that named a note, and the filing row hides anything in it. Per device, in
 * `localStorage`, capped like the dismissed set — the poll only ever shows
 * the newest twenty captures, so nothing older than a couple of hundred ids
 * can come back.
 */

export const TARGETED_KEY = 'chintan.filing.targeted';

/** Plenty of headroom over the twenty rows the poll can show. */
export const TARGETED_LIMIT = 200;

function read(): string[] {
  try {
    const raw = localStorage.getItem(TARGETED_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    // Storage blocked or corrupt: nothing is remembered, which only means a
    // receipt shows on Home that need not have. Never a reason to fail the library.
    return [];
  }
}

function write(ids: readonly string[]): void {
  try {
    localStorage.setItem(TARGETED_KEY, JSON.stringify(ids.slice(-TARGETED_LIMIT)));
  } catch {
    // Quota or private mode. The server's own flag still hides the row where
    // the backend sends it.
  }
}

/** Every server capture id this device sent with a chosen note, oldest first. */
export function loadTargeted(): ReadonlySet<string> {
  return new Set(read());
}

/** Records that `captureId` was sent into a note. Newest last, so the cap drops the oldest. */
export function rememberTargeted(captureId: string): void {
  const next = read().filter((id) => id !== captureId);
  next.push(captureId);
  write(next);
}

/** Whether the library's filing row should leave this capture to its note. */
export function isTargeted(
  capture: { id: string; targeted?: boolean },
  remembered: ReadonlySet<string>,
): boolean {
  return capture.targeted === true || remembered.has(capture.id);
}
