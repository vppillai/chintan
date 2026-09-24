import { useCallback } from 'react';

import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { Waveform } from './Waveform.tsx';
import { formatElapsed } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { holdSendable, type HoldPhase } from './useHoldToTalk.ts';

/**
 * What the tab-bar mic shows while it is held: the live level, the clock and
 * the one instruction, in a card just above the bar — over the current
 * screen, not instead of it, so the note or the library stays where it was
 * and system Back is untouched. After a hold too short to keep, the same card
 * says "Hold to talk" for a moment; pressed while the last recording is still
 * leaving, "Still sending the last one…".
 *
 * The instruction is spoken from one live region that is always mounted and
 * changes its text, never from the card: a screen reader reads what a live
 * region *changes to*, not what it mounts with (the lesson `FilingItem`'s
 * receipt row records). The cards are for the eye and hidden from the tree.
 *
 * `pointer-events: none`: the pointer is the button's for the whole hold, and
 * nothing here is for tapping.
 */
export function HoldOverlay({ phase, away }: { phase: HoldPhase; away: boolean }) {
  const model = useCaptureStore((state) => state.model);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const reducedMotion = useReducedMotion();
  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);

  const instruction =
    phase === 'holding'
      ? away
        ? 'Release to cancel'
        : holdSendable(model)
          ? 'Release to send · slide away to cancel'
          : 'Starting the microphone…'
      : phase === 'hint'
        ? 'Hold to talk'
        : phase === 'busy'
          ? 'Still sending the last one…'
          : '';

  return (
    <>
      {phase === 'holding' && (
        <div className="hold-overlay" data-away={away || undefined} aria-hidden="true">
          <div className="hold-overlay__level">
            <Waveform
              key={model.localId}
              read={read}
              active={model.state === 'recording'}
              reducedMotion={reducedMotion}
            />
          </div>
          <p className="hold-overlay__timer numeric">{formatElapsed(model.elapsedMs)}</p>
          <p className="hold-overlay__hint">{instruction}</p>
        </div>
      )}
      {(phase === 'hint' || phase === 'busy') && (
        <p className="hold-overlay hold-overlay--hint" aria-hidden="true">
          {instruction}
        </p>
      )}
      <p className="visually-hidden" role="status">
        {instruction}
      </p>
    </>
  );
}
