import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';

/**
 * The capture surface. The sheet is locked shut here (§5.2).
 *
 * Audio capture, the AnalyserNode waveform, the wake lock, and the chunked
 * IndexedDB buffer are a later phase (§5.3). Deliberately, this screen does
 * not claim recording has started — it says the microphone is not wired yet,
 * because "nothing in the UI claims recording has started before getUserMedia
 * resolves" is a stated requirement and a placeholder that lies about it would
 * be worse than one that does not.
 */
export function CaptureScreen() {
  const navigate = useNavigate();

  return (
    <div className="capture">
      <h1 className="capture__title">Capture</h1>

      <p className="capture__timer numeric" aria-hidden="true">
        00:00
      </p>

      <div className="capture__waveform" aria-hidden="true">
        <span className="capture__waveform-placeholder" />
      </div>

      <p className="capture__status">
        The microphone is not connected in this build. Audio capture, the live waveform,
        and the progress card arrive with the capture phase.
      </p>

      <div className="capture__controls">
        <button
          type="button"
          className="capture__control"
          onClick={() => {
            void navigate(ROUTES.home);
          }}
        >
          <Icon name="stop" size={22} />
          <span>Cancel</span>
        </button>
      </div>
    </div>
  );
}
