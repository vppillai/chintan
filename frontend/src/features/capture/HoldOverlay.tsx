import { useCallback } from 'react';

import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { Waveform } from './Waveform.tsx';
import { formatElapsed } from './machine.ts';
import { useCaptureStore } from './store.ts';
import type { HoldPhase } from './useHoldToTalk.ts';

/**
 * What the tab-bar mic shows while it is held: the live level, the clock and
 * the one instruction, in a card just above the bar — over the current
 * screen, not instead of it, so the note or the library stays where it was
 * and system Back is untouched. After a hold too short to keep, the same card
 * says "Hold to talk" for a moment.
 *
 * `pointer-events: none`: the pointer is the button's for the whole hold, and
 * nothing here is for tapping.
 */
export function HoldOverlay({ phase, away }: { phase: HoldPhase; away: boolean }) {
  const model = useCaptureStore((state) => state.model);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const reducedMotion = useReducedMotion();
  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);

  if (phase === 'hint') {
    return (
      <p className="hold-overlay hold-overlay--hint" role="status">
        Hold to talk
      </p>
    );
  }
  if (phase !== 'holding') return null;

  const live = model.state === 'recording' || model.state === 'paused';
  return (
    <div className="hold-overlay" data-away={away || undefined}>
      <div className="hold-overlay__level" aria-hidden="true">
        <Waveform
          key={model.localId}
          read={read}
          active={model.state === 'recording'}
          reducedMotion={reducedMotion}
        />
      </div>
      <p className="hold-overlay__timer numeric" aria-label={`Elapsed ${formatElapsed(model.elapsedMs)}`}>
        {formatElapsed(model.elapsedMs)}
      </p>
      {/* The instruction is the live region; the clock and the canvas are not. */}
      <p className="hold-overlay__hint" role="status">
        {away
          ? 'Release to cancel'
          : live
            ? 'Release to send · slide away to cancel'
            : 'Starting the microphone…'}
      </p>
    </div>
  );
}
