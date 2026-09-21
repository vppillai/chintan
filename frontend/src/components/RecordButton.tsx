import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';

/**
 * The record target, seated in the middle of the tab bar on every screen — in
 * flow, not floating, so it can never overlay a note row.
 *
 * It is the only element in the app allowed to wear `--color-accent`.
 *
 * On a note it records into that note (`noteId`), and says so on a small
 * caption above the disc — the same button, so the thumb never has to choose
 * between two record controls. The note bar used to carry its own "Record
 * into this" 30 px above this mic, which recorded to a new note: two controls
 * with different glyphs recording to different places from one screen.
 */
export function RecordButton({ noteId = null }: { noteId?: string | null }) {
  const navigate = useNavigate();
  const into = noteId !== null;

  return (
    <button
      type="button"
      className="record-button"
      data-into={into || undefined}
      onClick={() => {
        void navigate(into ? ROUTES.captureInto(noteId) : ROUTES.capture);
      }}
    >
      <Icon name="mic" size={30} className="record-button__icon" />
      {/*
        The whole name in one hidden span: accessible-name algorithms differ
        on the space between two inline spans, and "RecordInto this note" is
        what one of them says. The caption is the sighted reading of it.
      */}
      <span className="visually-hidden">{into ? 'Record into this note' : 'Record'}</span>
      {into && (
        <span className="record-button__into" aria-hidden="true">
          Into this note
        </span>
      )}
    </button>
  );
}
