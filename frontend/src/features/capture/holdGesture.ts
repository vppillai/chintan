import {
  CANCEL_DX_PX,
  DISCARD_CONFIRM_AFTER_MS,
  DISCARD_CONFIRM_MS,
  HOLD_ARM_MS,
  LOCK_DY_PX,
  MIN_TALK_MS,
  TAP_SLOP_PX,
} from './holdTiming.ts';
import { hasBufferedAudio, isCaptureBusy, type CaptureModel } from './machine.ts';

/**
 * The record disc's gesture, as a pure reducer: WhatsApp's hold-to-talk.
 *
 * A tap opens the capture screen. A press held past `HOLD_ARM_MS` records;
 * letting go sends. Sliding up `LOCK_DY_PX` locks the recording so the finger
 * can lift (hands-free, in place, with Send, Stop and Discard on the bar);
 * sliding left `CANCEL_DX_PX` throws it away. Both are measured from the
 * press point, so they fit the narrowest phone.
 *
 * The reducer only decides. `useHoldToTalk` feeds it pointer, key, timer and
 * capture-store events and runs the effects it returns, which is what lets
 * every transition be tested here with a fake clock and no DOM.
 */

/** What the browser says about the microphone; `unknown` where it will not say (Firefox). */
export type MicPermission = 'granted' | 'prompt' | 'denied' | 'unknown';

/**
 * What pressed: a pointer on the disc, Space on the focused disc, or R from
 * anywhere. Only a pointer can slide, and only Space taps: a quick R press
 * must never start or open anything by accident.
 */
export type HoldSource = 'pointer' | 'space' | 'key';

export type GesturePhase =
  /** Nothing pressed. */
  | 'idle'
  /** Pressed, not yet a hold: the release may still be a tap. No microphone yet. */
  | 'armed'
  /** Recording under the finger; release sends. */
  | 'holding'
  /** Slid past the cancel line. Nothing more happens until the finger lifts. */
  | 'cancelled'
  /** A press that will not record (busy, or blocked). Waits for the release. */
  | 'standdown'
  /** Recording hands-free, finger up. Send, Stop and Discard are on the bar. */
  | 'locked';

export type HoldNotice = 'sent' | 'short' | 'cancelled' | 'busy' | 'blocked' | 'unavailable';

export type HoldEffect =
  | { type: 'start' }
  | { type: 'discard' }
  | { type: 'send' }
  /** Stop and review: the capture screen with the player, Send and Discard. */
  | { type: 'stop' }
  /** To the capture screen, which shows or starts the recording. */
  | { type: 'openCapture' }
  | { type: 'notice'; notice: HoldNotice }
  | { type: 'lockFeedback' }
  | { type: 'cancelFeedback' }
  | { type: 'errorFeedback' }
  /** The click a browser sends after a long press must not count as a tap. */
  | { type: 'suppressClick' };

/** Why a press stands down, decided when it is pressed and applied when it arms. */
type StandDown =
  /** The last recording is still leaving: say so. */
  | 'busy'
  /** Another recording is live or waiting: the release is a tap, to show it. */
  | 'quiet'
  /** The microphone is refused: say so. */
  | 'blocked';

export interface Gesture {
  phase: GesturePhase;
  source: HoldSource;
  /** Where the press began; the thresholds are measured from here. */
  originX: number;
  originY: number;
  /** When the press began, for the arm. */
  pressedAt: number;
  /** The latest offset from the press point. */
  dx: number;
  dy: number;
  /** A finger or key is down. A second press is ignored until it lifts. */
  pressing: boolean;
  /** The press armed into something a click must not repeat. */
  suppress: boolean;
  permission: MicPermission;
  standDown: StandDown | null;
  /** While set and in the future, a second Discard throws the take away. */
  discardArmedUntil: number | null;
}

export const IDLE_GESTURE: Gesture = {
  phase: 'idle',
  source: 'pointer',
  originX: 0,
  originY: 0,
  pressedAt: 0,
  dx: 0,
  dy: 0,
  pressing: false,
  suppress: false,
  permission: 'unknown',
  standDown: null,
  discardArmedUntil: null,
};

export type GestureEvent =
  | {
      type: 'down';
      x: number;
      y: number;
      now: number;
      source: HoldSource;
      model: CaptureModel;
      permission: MicPermission;
    }
  /** A timer fired: the arm, or the end of "Discard?". */
  | { type: 'tick'; now: number }
  | { type: 'move'; x: number; y: number }
  | { type: 'up'; now: number; model: CaptureModel }
  /** The browser or the OS took the press: pointercancel, a hidden page, a lost keyup. */
  | { type: 'interrupt'; now: number; model: CaptureModel }
  | { type: 'modelChanged'; model: CaptureModel }
  /** The locked bar's buttons, and Escape. */
  | { type: 'send'; now: number; model: CaptureModel }
  | { type: 'stop' }
  | { type: 'discard'; now: number; model: CaptureModel }
  | { type: 'escape'; now: number; model: CaptureModel };

export interface GestureStep {
  gesture: Gesture;
  effects: HoldEffect[];
}

/** 0..1 toward each line, for the hint's fade and the padlock's travel. */
export function holdProgress(gesture: Gesture): { cancel: number; lock: number } {
  return {
    cancel: clamp01(-gesture.dx / CANCEL_DX_PX),
    lock: clamp01(-gesture.dy / LOCK_DY_PX),
  };
}

function clamp01(value: number): number {
  return Math.min(1, Math.max(0, value));
}

/** The recording's length now; a settled recording's clock has stopped. */
export function elapsedNow(model: CaptureModel, now: number): number {
  return model.startedAt === null ? model.elapsedMs : model.accumulatedMs + now - model.startedAt;
}

/**
 * Whether a release sends: the microphone live, or a recording that settled
 * on its own with audio in it (a call ended the track, the cap stopped it)
 * while the finger was down. That audio is the message; an interruption
 * yields a partial recording, never a discard.
 */
function sendable(model: CaptureModel): boolean {
  return (
    model.state === 'recording' ||
    model.state === 'paused' ||
    ((model.state === 'stopping' || model.state === 'review') && model.bytes > 0)
  );
}

function step(gesture: Gesture, ...effects: HoldEffect[]): GestureStep {
  return { gesture, effects };
}

/** Back to rest, keeping only whether a finger is still down. */
function rest(gesture: Gesture): Gesture {
  return { ...IDLE_GESTURE, pressing: gesture.pressing, suppress: gesture.suppress };
}

function notice(kind: HoldNotice): HoldEffect {
  return { type: 'notice', notice: kind };
}

/** The microphone failed: refused, missing, or the recorder broke. Nothing to discard. */
function failed(gesture: Gesture, model: CaptureModel): GestureStep {
  return step(
    rest(gesture),
    notice(model.failure?.kind === 'permission-denied' ? 'blocked' : 'unavailable'),
  );
}

/** Sends what was recorded, or calls it a slip. Shared by release and the locked Send. */
function finish(gesture: Gesture, model: CaptureModel, now: number): GestureStep {
  if (model.state === 'failed') return failed(gesture, model);
  if (sendable(model) && elapsedNow(model, now) >= MIN_TALK_MS) {
    return step(rest(gesture), { type: 'send' }, notice('sent'));
  }
  return step(rest(gesture), { type: 'discard' }, notice('short'), { type: 'errorFeedback' });
}

/** Armed into a hold: the microphone opens, or the press stands down. */
function arm(gesture: Gesture): GestureStep {
  if (gesture.standDown === 'quiet') {
    // The release is a tap, so the click is left alone: the capture screen
    // is where the other recording is shown.
    return step({ ...gesture, phase: 'standdown' });
  }
  const armed = { ...gesture, suppress: true };
  if (gesture.standDown === 'busy') return step({ ...armed, phase: 'standdown' }, notice('busy'));
  if (gesture.standDown === 'blocked') {
    return step({ ...armed, phase: 'standdown' }, notice('blocked'), { type: 'errorFeedback' });
  }
  return step({ ...armed, phase: 'holding' }, { type: 'start' });
}

function standDownFor(model: CaptureModel, permission: MicPermission): StandDown | null {
  if (model.state === 'uploading' || model.state === 'stopping') return 'busy';
  if (isCaptureBusy(model) || hasBufferedAudio(model)) return 'quiet';
  if (permission === 'denied') return 'blocked';
  return null;
}

/** A finger or key let go, or was taken. `tap` is false for an interruption. */
function lift(gesture: Gesture, model: CaptureModel, now: number, tap: boolean): GestureStep {
  const lifted = { ...gesture, pressing: false, suppress: false };
  const effects: HoldEffect[] = gesture.suppress ? [{ type: 'suppressClick' }] : [];
  const out = (result: GestureStep): GestureStep => ({
    gesture: result.gesture,
    effects: [...effects, ...result.effects],
  });

  switch (gesture.phase) {
    case 'armed':
      // A tap. A pointer's tap is the click that follows; Space's default
      // was stopped, so it opens the screen itself; R never taps.
      return out(
        tap && gesture.source === 'space'
          ? step(rest(lifted), { type: 'openCapture' })
          : step(rest(lifted)),
      );
    case 'holding':
      /*
       * The first press on a fresh install raises the permission prompt, and
       * the finger has to lift to answer it. Calling that "Too short" made
       * the first press always fail; it locks instead, so the recording goes
       * on hands-free once allowed. With the microphone already granted, a
       * release during a slow start is the slip it looks like.
       */
      if (model.state === 'requesting' && gesture.permission !== 'granted') {
        return out(step({ ...lifted, phase: 'locked', dx: 0, dy: 0 }, { type: 'lockFeedback' }));
      }
      return out(finish(lifted, model, now));
    case 'locked':
      // The lift after the slide up: the recording carries on.
      return out(step(lifted));
    case 'idle':
    case 'cancelled':
    case 'standdown':
      return out(step(rest(lifted)));
  }
}

function move(gesture: Gesture, x: number, y: number): GestureStep {
  const dx = x - gesture.originX;
  const dy = y - gesture.originY;
  if (gesture.phase === 'armed') {
    // A fast slide is a hold from the first frame, not a tap that moved.
    if (Math.hypot(dx, dy) < TAP_SLOP_PX) return step(gesture);
    const armed = arm(gesture);
    if (armed.gesture.phase !== 'holding') return armed;
    const next = move(armed.gesture, x, y);
    return { gesture: next.gesture, effects: [...armed.effects, ...next.effects] };
  }
  if (gesture.phase !== 'holding' || gesture.source !== 'pointer') return step(gesture);
  const held = {
    ...gesture,
    dx: Math.min(0, Math.max(-CANCEL_DX_PX, dx)),
    dy: Math.min(0, Math.max(-LOCK_DY_PX, dy)),
  };
  // Lock is checked first: if one move crosses both lines, keeping the audio
  // is the mistake that costs nothing.
  if (dy <= -LOCK_DY_PX) {
    return step({ ...held, phase: 'locked', dx: 0, dy: 0 }, { type: 'lockFeedback' });
  }
  if (dx <= -CANCEL_DX_PX) {
    return step(
      { ...held, phase: 'cancelled' },
      { type: 'discard' },
      { type: 'cancelFeedback' },
      notice('cancelled'),
    );
  }
  return step(held);
}

function discard(gesture: Gesture, model: CaptureModel, now: number): GestureStep {
  const armed = gesture.discardArmedUntil !== null && now < gesture.discardArmedUntil;
  if (armed || elapsedNow(model, now) < DISCARD_CONFIRM_AFTER_MS) {
    return step(rest(gesture), { type: 'discard' }, notice('cancelled'));
  }
  // Ten seconds of speech is worth one more tap before it is gone.
  return step({ ...gesture, discardArmedUntil: now + DISCARD_CONFIRM_MS });
}

export function holdReducer(gesture: Gesture, event: GestureEvent): GestureStep {
  switch (event.type) {
    case 'down':
      if (gesture.phase !== 'idle' || gesture.pressing) return step(gesture);
      return step({
        ...IDLE_GESTURE,
        phase: 'armed',
        source: event.source,
        originX: event.x,
        originY: event.y,
        pressedAt: event.now,
        pressing: true,
        permission: event.permission,
        standDown: standDownFor(event.model, event.permission),
      });

    case 'tick':
      if (gesture.phase === 'armed' && event.now - gesture.pressedAt >= HOLD_ARM_MS) {
        return arm(gesture);
      }
      if (
        gesture.phase === 'locked' &&
        gesture.discardArmedUntil !== null &&
        event.now >= gesture.discardArmedUntil
      ) {
        return step({ ...gesture, discardArmedUntil: null });
      }
      return step(gesture);

    case 'move':
      return move(gesture, event.x, event.y);

    case 'up':
      return lift(gesture, event.model, event.now, true);

    case 'interrupt':
      return lift(gesture, event.model, event.now, false);

    case 'modelChanged': {
      const { model } = event;
      if (gesture.phase !== 'holding' && gesture.phase !== 'locked') return step(gesture);
      if (model.state === 'failed') return failed(gesture, model);
      if (gesture.phase === 'locked') {
        // The call ended the track or the cap stopped it: the person decides
        // on the review screen. Never an automatic send, never a discard.
        if (model.state === 'review') return step(rest(gesture), { type: 'openCapture' });
        // Discarded or sent from somewhere else.
        if (model.state === 'idle' || model.state === 'uploading' || model.state === 'uploaded') {
          return step(rest(gesture));
        }
      }
      return step(gesture);
    }

    case 'send':
      if (gesture.phase !== 'locked') return step(gesture);
      return finish(gesture, event.model, event.now);

    case 'stop':
      if (gesture.phase !== 'locked') return step(gesture);
      return step(rest(gesture), { type: 'stop' });

    case 'discard':
      if (gesture.phase !== 'locked') return step(gesture);
      return discard(gesture, event.model, event.now);

    case 'escape':
      if (gesture.phase === 'locked') return discard(gesture, event.model, event.now);
      if (gesture.phase === 'holding') {
        return step(
          { ...gesture, phase: 'cancelled' },
          { type: 'discard' },
          { type: 'cancelFeedback' },
          notice('cancelled'),
        );
      }
      if (gesture.phase === 'armed') return step({ ...gesture, phase: 'standdown' });
      return step(gesture);
  }
}
