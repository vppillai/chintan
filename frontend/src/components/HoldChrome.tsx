import { useCallback, useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import { holdProgress, type HoldNotice } from '@/features/capture/holdGesture.ts';
// Static on purpose. Lazy, it moved 0.6 kB out of the main chunk and had
// rolldown split `jsx-runtime` into a chunk of its own, one more request
// every launch for the same bytes (review 2026-10-01, FE-2, measured).
import { Waveform } from '@/features/capture/Waveform.tsx';
import { formatElapsed } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import type { useHoldToTalk } from '@/features/capture/useHoldToTalk.ts';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { Icon } from './Icon.tsx';

/** What the clock pill and the status line say for each notice. */
const NOTICES: Record<HoldNotice, { pill: string; spoken: string }> = {
  sent: { pill: 'Sent', spoken: 'Sent' },
  short: { pill: 'Too short — hold to talk', spoken: 'Too short' },
  cancelled: { pill: 'Cancelled', spoken: 'Cancelled' },
  busy: { pill: 'Still sending the last one…', spoken: 'Still sending the last one…' },
  blocked: { pill: 'Microphone blocked', spoken: 'Microphone blocked' },
  unavailable: { pill: 'No microphone', spoken: 'No microphone' },
};

/*
 * The first-run coach: "Hold to talk · tap to record" above the disc on Home
 * for the first three launches, until the first hold that sends (owner
 * decision D4). A launch is a page load, counted once however often the bar
 * mounts. Storage that throws (a private window) shows nothing.
 */
const COACH_KEY = 'chintan.coach.ptt';
const COACH_LAUNCHES = 3;
let launchCounted = false;

function readCoach(): boolean {
  try {
    const stored = window.localStorage.getItem(COACH_KEY);
    if (stored === 'done') return false;
    const launches = Number(stored ?? '0') + (launchCounted ? 0 : 1);
    if (!launchCounted) window.localStorage.setItem(COACH_KEY, String(launches));
    launchCounted = true;
    return launches <= COACH_LAUNCHES;
  } catch {
    return false;
  }
}

function retireCoach(): void {
  try {
    window.localStorage.setItem(COACH_KEY, 'done');
  } catch {
    /* Storage denied: the coach simply shows again next launch. */
  }
}

/** Whether the coach is still owed, and the call that retires it for good: the first hold that sends. */
export function useCoach(): [boolean, () => void] {
  const [coach, setCoach] = useState(readCoach);
  const retire = useCallback(() => {
    retireCoach();
    setCoach(false);
  }, []);
  return [coach, retire];
}

/**
 * Gives the "‹ Slide to cancel" hint its resting left edge in the viewport,
 * which the CSS clamps its drift against. The hint is centred in its slot,
 * so how far it may travel left depends on its own width and the screen's,
 * which CSS cannot know; without the floor a 390 px phone clipped it at the
 * screen edge once the finger was some 80 px left of the disc (R8-P2).
 * `offsetLeft` ignores the drift's `translate`, so this reads the rest
 * position whenever it runs.
 */
function pinCancelHint(hint: HTMLElement | null): void {
  const bar = hint?.offsetParent;
  if (!hint || !bar) return;
  const left = bar.getBoundingClientRect().left + bar.clientLeft + hint.offsetLeft;
  hint.style.setProperty('--cancel-rest-left', `${String(left)}px`);
}

/**
 * Push-to-talk's chrome on the tab bar (R8, F5): the lock pill above the
 * disc, the clock pill on the bar's top edge, "‹ Slide to cancel" in the
 * Home slot, the live level in the You slot, and the status line that speaks
 * what the pills show. `children` is the disc itself, which sits in the
 * `.tab-bar__record` cell the lock is anchored to. The bar keeps the gesture
 * (`useHoldToTalk`) and the tabs; this draws what the hold shows.
 */
export function HoldChrome({
  hold,
  into,
  coach,
  children,
}: {
  hold: ReturnType<typeof useHoldToTalk>;
  /** The note the disc records into, when the bar stands on one. */
  into: string | null;
  /** Whether the first-run coach line is owed here and now. */
  coach: boolean;
  children: ReactNode;
}) {
  const model = useCaptureStore((state) => state.model);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);
  const reducedMotion = useReducedMotion();

  const { notice } = hold;
  const holding = hold.gesture.phase === 'holding';
  const progress = holdProgress(hold.gesture);
  // The recording's own target, not the route's.
  const clock = `${formatElapsed(model.elapsedMs)}${model.noteId !== null ? ' · Into this note' : ''}`;

  let pill: ReactNode = null;
  if (notice) pill = NOTICES[notice].pill;
  else if (holding) {
    pill = (
      <>
        <span className="tab-bar__dot" />
        {clock}
      </>
    );
  } else if (into !== null) pill = 'Into this note';
  else if (coach) pill = 'Hold to talk · tap to record';

  const spoken = notice ? NOTICES[notice].spoken : holding ? 'Recording' : '';

  /*
   * Measured again when the bin appears near the line: it widens the
   * centred hint, which moves its resting left edge by half the bin.
   */
  const near = holding && progress.cancel >= 0.6;
  const cancelHint = useRef<HTMLSpanElement>(null);
  useLayoutEffect(() => {
    pinCancelHint(cancelHint.current);
  }, [holding, near]);

  return (
    <>
      <div className="tab-bar__record">
        {holding && (
          <span
            className="tab-bar__lock"
            aria-hidden="true"
            data-leaning={progress.cancel > progress.lock ? 'cancel' : undefined}
          >
            <Icon name="lock-open" size={20} className="tab-bar__padlock" />
            <Icon name="chevrons-up" size={20} className="tab-bar__chevrons" />
          </span>
        )}
        {children}
      </div>
      {/* The sighted reading of the button's name and the hold's clock; the status line speaks. */}
      {pill !== null && (
        <span className="tab-bar__into" aria-hidden="true">
          {pill}
        </span>
      )}
      {holding && (
        <span
          ref={cancelHint}
          className="tab-bar__slot tab-bar__slot--start tab-bar__cancel"
          aria-hidden="true"
          data-near={near || undefined}
        >
          <Icon name="trash" size={20} className="tab-bar__cancel-glyph" />
          <span>‹ Slide to cancel</span>
        </span>
      )}
      {holding && (
        <span className="tab-bar__slot tab-bar__slot--end tab-bar__level" aria-hidden="true">
          <Waveform
            key={model.localId}
            read={read}
            active={model.state === 'recording'}
            reducedMotion={reducedMotion}
          />
        </span>
      )}
      <p className="visually-hidden" role="status" aria-live="polite">
        {spoken}
      </p>
    </>
  );
}
