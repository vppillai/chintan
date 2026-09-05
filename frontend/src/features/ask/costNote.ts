/**
 * The one-line cost note under the Ask field, shown until it is dismissed.
 *
 * Asking costs money in a way searching does not — one model call over a
 * dozen notes, about a cent — and the person paying should hear that once,
 * in the place they are about to spend it. Once is the point: dismissed is
 * remembered per device in `localStorage`, which is what "the first time"
 * means on a phone whose tabs are killed daily.
 */

export const COST_NOTE_KEY = 'chintan.ask.cost-note-dismissed';

export const COST_NOTE = 'Each question is one model call, about a cent.';

export function costNoteDismissed(): boolean {
  try {
    return localStorage.getItem(COST_NOTE_KEY) === '1';
  } catch {
    // Storage blocked: the note shows again, which is the harmless way to be wrong.
    return false;
  }
}

export function dismissCostNote(): void {
  try {
    localStorage.setItem(COST_NOTE_KEY, '1');
  } catch {
    // Quota or private mode. The in-memory dismissal holds for this screen.
  }
}
