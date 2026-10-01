import { useId } from 'react';
import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';
import { useRecordTarget } from './useRecordTarget.ts';

/**
 * Record while typing (R8, F3): a mic at the banner's right end, for the
 * moment the keyboard covers the tab bar and its disc.
 *
 * Rendered on the note screen whenever the note takes recordings, and shown
 * by CSS alone (`shell.css` `.banner-record`) only while `useKeyboardInset`
 * has marked the keyboard up and one of the note's fields as focused
 * (`data-keyboard` and `data-editing` on `<html>`). The
 * banner is its own grid row, so the mic can never cover a character of the
 * note, and the row does not change height.
 *
 * Named as the tab bar's disc is, "Record into this note" — it is the same
 * act — and described as the one that is here while typing, since when a
 * control appears is a state, not part of its name (review 2026-10-01,
 * FE-22). `shell.css` also keeps it shown
 * while it has focus itself. That cannot carry a focus move from the field
 * to it (Chromium hides it as the field blurs, before it can take focus),
 * and nothing in the tab order sits between the two; it is there so a
 * focus that does land on it is never left on a hidden control.
 *
 * A tap only. `pointerdown` is prevented so the field keeps focus, which
 * keeps the keyboard up and the button where the finger is until the click
 * lands. The editor's autosave flushes as the note screen unmounts
 * (`useNoteEditor`), and `/capture` comes back to the note at its place.
 */
export function BannerRecord() {
  const into = useRecordTarget();
  const navigate = useNavigate();
  const hintId = useId();
  if (into === null) return null;
  return (
    <button
      type="button"
      className="banner-record"
      aria-label="Record into this note"
      aria-describedby={hintId}
      onPointerDown={(event) => {
        event.preventDefault();
      }}
      onClick={() => void navigate(ROUTES.captureInto(into))}
    >
      <span className="banner-record__disc">
        <Icon name="mic" size={20} strokeWidth={2} />
      </span>
      <span id={hintId} className="visually-hidden">
        Shown while you type.
      </span>
    </button>
  );
}
