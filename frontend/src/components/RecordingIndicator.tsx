import { useLocation, useNavigate } from 'react-router';

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
