import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target, seated in the middle of the tab bar on every screen — in
 * flow, not floating, so it can never overlay a note row.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
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
      onClick={() => {
        void navigate(into ? ROUTES.captureInto(noteId) : ROUTES.capture);
      }}
    >
      <Icon name="mic" size={30} className="record-button__icon" />
      <span className="visually-hidden">{into ? 'Record into this note' : 'Record'}</span>
    </button>
  );
}
