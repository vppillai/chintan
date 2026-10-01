import type { HoldToTalk } from '@/features/capture/useHoldToTalk.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target, seated in the middle of the tab bar on every screen —
 * in flow, not floating, so it can never overlay a note row. The disc names
 * itself under its glyph (owner feedback 2026-09-26), and the caption is
 * `aria-hidden` because the button's `aria-label` already says it.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
 *
 * Tapped, it opens the capture screen, which records at once: hands-free,
 * with Pause, Stop, review and the target chooser. Held, it is push-to-talk,
 * WhatsApp's way (R8, F5): release sends, slide up locks, slide left
 * cancels. The gesture is `useHoldToTalk`'s, owned by the tab bar because
 * the bar's other slots change with it; this is the disc that wears it.
 * A locked hold carries on on the capture screen. (The owner once asked for the hold on a separate
 * PTT screen only, feedback 2026-09-27; having tried that screen, he asked
 * for it here and for the screen to go, feedback 2026-09-30.)
 *
 * On a note it records into that note (`noteId`) and is named for it — the
 * same button, so the thumb never has to choose between two record controls.
 * The sighted caption is the tab bar's (`.tab-bar__into`), beside the disc
 * rather than inside it, so the disc's own box stays the disc.
 */
export function RecordButton({
  noteId = null,
  hold,
}: {
  noteId?: string | null;
  hold: HoldToTalk;
}) {
  // The hint a mouse user can hover for; a touch screen has no hover to show it.
  const fine =
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(hover: hover) and (pointer: fine)').matches;

  return (
    <button
      type="button"
      className="record-button"
      aria-label={noteId !== null ? 'Record into this note' : 'Record'}
      aria-description="Hold to talk and release to send. While holding, slide up to lock or left to cancel."
      aria-keyshortcuts="R"
      title={fine ? 'Click to record · hold to talk (or hold R)' : undefined}
      {...hold.handlers}
    >
      <Icon
        name="mic"
        size={24}
        strokeWidth={2.25}
        className="record-button__icon"
      />
      <span className="record-button__label" aria-hidden="true">
        Record
      </span>
    </button>
  );
}
