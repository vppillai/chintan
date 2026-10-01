import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import { Icon } from './Icon.tsx';
import { useRecordTarget } from './useRecordTarget.ts';

/**
 * Record while typing (R8, F3): a mic at the banner's right end, for the
 * moment the keyboard covers the tab bar and its disc.
 *
 * Rendered on the note screen whenever the note takes recordings, and shown
 * by CSS alone (`shell.css` `.banner-record`) only while one of the note's
 * fields has focus and `useKeyboardInset` has marked the keyboard up. The
 * banner is its own grid row, so the mic can never cover a character of the
 * note, and the row does not change height.
 *
 * A tap only. `pointerdown` is prevented so the field keeps focus, which
 * keeps the keyboard up and the button where the finger is until the click
 * lands. The editor's autosave flushes as the note screen unmounts
 * (`useNoteEditor`), and `/capture` comes back to the note at its place.
 */
export function BannerRecord() {
  const into = useRecordTarget();
  const navigate = useNavigate();
  if (into === null) return null;
  return (
    <button
      type="button"
      className="banner-record"
      aria-label="Record into this note"
      onPointerDown={(event) => {
        event.preventDefault();
      }}
      onClick={() => void navigate(ROUTES.captureInto(into))}
    >
      <span className="banner-record__disc">
        <Icon name="mic" size={20} strokeWidth={2} />
      </span>
    </button>
  );
}
