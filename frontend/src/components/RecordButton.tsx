import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target, seated in the middle of the tab bar on every screen —
 * in flow, not floating, so it can never overlay a note row. Tapped, it
 * opens the capture screen; that is all it does. The disc names itself under
 * its glyph (owner feedback 2026-09-26), and the caption is `aria-hidden`
 * because the button's `aria-label` already says it.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
 *
 * It held to talk for a while (#85, "PTT" from #114). The owner asked for
 * push-to-talk on the widget only, not in the main app (feedback
 * 2026-09-27), so the hold lives on `/talk` alone — reached from You and the
 * manifest shortcut — and this disc is a plain Record control again.
 *
 * On a note it records into that note (`noteId`) and is named for it — the
 * same button, so the thumb never has to choose between two record controls.
 * The note bar used to carry its own "Record into this" 30 px above this
 * mic, which recorded to a new note: two controls with different glyphs
 * recording to different places from one screen. The sighted caption is the
 * tab bar's (`.tab-bar__into`), beside the disc rather than inside it, so
 * the disc's own box stays the disc.
 */
export function RecordButton({ noteId = null }: { noteId?: string | null }) {
  const navigate = useNavigate();
  const into = noteId !== null;

  return (
    <button
      type="button"
      className="record-button"
      aria-label={into ? 'Record into this note' : 'Record'}
      onClick={() => {
        void navigate(into ? ROUTES.captureInto(noteId) : ROUTES.capture);
      }}
    >
      <Icon name="mic" size={24} strokeWidth={2.25} className="record-button__icon" />
      <span className="record-button__label" aria-hidden="true">
        Record
      </span>
    </button>
  );
}
