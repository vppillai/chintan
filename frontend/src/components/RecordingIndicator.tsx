import { matchPath, useLocation, useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { isCaptureBusy } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';

/**
 * Says, on every screen, that the microphone is open.
 *
 * `isCaptureBusy` was written and documented — "the sheet must stay shut while
 * any of this is in flight" — and then called from nowhere. Pressing system
 * Back while recording, which a pocket or a steering wheel does by accident,
 * landed the user on the home screen showing the ordinary Record button while
 * the recorder kept running, kept writing chunks, kept the wake lock, and kept
 * counting toward the twenty-minute cap. A user who believed they had stopped
 * could record, transcribe and pay for twenty minutes of pocket noise.
 *
 * Deliberately a row that returns you to the capture screen rather than a stop
 * button, and deliberately not an auto-stop on leaving: a recording surviving
 * navigation is the design (see `store.ts`), and silently ending someone's
 * dictation because they glanced at another screen would lose the thing the
 * product exists to keep.
 */
export function RecordingIndicator() {
  const navigate = useNavigate();
  const location = useLocation();
  const model = useCaptureStore((state) => state.model);

  // On /capture the screen itself is the indicator.
  if (location.pathname === ROUTES.capture) return null;
  if (!isCaptureBusy(model)) return null;
  // On the library an upload is already shown as the filing row at the top,
  // with its percentage, and `/talk` draws the same row under its button; a
  // second line saying the same thing is noise. Every other screen still
  // says so, because nothing else on them does.
  if (
    model.state === 'uploading' &&
    (location.pathname === ROUTES.home || location.pathname === ROUTES.talk)
  ) {
    return null;
  }
  // A recording sent into a note returns to that note, whose filing banner
  // draws the upload on every tab; this row above the bar was a second word
  // for the same thing, and tapping it went to a capture screen that only
  // bounced back. Only for the note it is going into: an upload aimed at a
  // different note than the one open has no banner there, so it is still said.
  if (
    model.state === 'uploading' &&
    model.noteId !== null &&
    matchPath(ROUTES.notePattern, location.pathname)?.params.id === model.noteId
  ) {
    return null;
  }

  return (
    <button
      type="button"
      className="recording-indicator"
      onClick={() => void navigate(ROUTES.capture)}
    >
      <span className="recording-indicator__dot" aria-hidden="true" />
      <span>{label(model.state)} — tap to return</span>
    </button>
  );
}

function label(state: string): string {
  switch (state) {
    case 'requesting':
      return 'Starting the microphone';
    case 'paused':
      return 'Recording paused';
    case 'stopping':
      return 'Finishing the recording';
    case 'uploading':
      return 'Sending a recording';
    default:
      return 'Recording';
  }
}
