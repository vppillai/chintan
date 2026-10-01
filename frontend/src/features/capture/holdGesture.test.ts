import { describe, expect, it } from 'vitest';

import {
  holdProgress,
  holdReducer,
  IDLE_GESTURE,
  type Gesture,
  type GestureEvent,
  type HoldEffect,
  type HoldSource,
  type MicPermission,
} from './holdGesture.ts';
import {
  CANCEL_DX_PX,
  DISCARD_CONFIRM_AFTER_MS,
  DISCARD_CONFIRM_MS,
  HOLD_ARM_MS,
  LOCK_DY_PX,
  MIN_TALK_MS,
  TAP_SLOP_PX,
} from './holdTiming.ts';
import { INITIAL_CAPTURE, type CaptureModel } from './machine.ts';

/** A recording running since `since`, as the store's model has it. */
const recording = (since = 0): CaptureModel => ({
  ...INITIAL_CAPTURE,
  state: 'recording',
  localId: 'cap-1',
  startedAt: since,
});
const requesting: CaptureModel = { ...INITIAL_CAPTURE, state: 'requesting', localId: 'cap-1' };

const X = 200;
const Y = 800;

/** Runs events from rest, collecting every effect. */
function play(events: GestureEvent[], from: Gesture = IDLE_GESTURE) {
  let gesture = from;
  const effects: HoldEffect[] = [];
  for (const event of events) {
    const next = holdReducer(gesture, event);
    gesture = next.gesture;
    effects.push(...next.effects);
  }
  return { gesture, effects, types: effects.map((effect) => effect.type) };
}

const down = (
  options: {
    source?: HoldSource;
    model?: CaptureModel;
    permission?: MicPermission;
    now?: number;
  } = {},
): GestureEvent => ({
  type: 'down',
  x: X,
  y: Y,
  now: options.now ?? 0,
  source: options.source ?? 'pointer',
  model: options.model ?? INITIAL_CAPTURE,
  permission: options.permission ?? 'granted',
});
const armTick: GestureEvent = { type: 'tick', now: HOLD_ARM_MS };
const move = (dx: number, dy: number): GestureEvent => ({ type: 'move', x: X + dx, y: Y + dy });
const up = (now: number, model: CaptureModel = recording(HOLD_ARM_MS)): GestureEvent => ({
  type: 'up',
  now,
  model,
});

/** Pressed and armed into a hold, the microphone live from the arm. */
function held(): Gesture {
  return play([down(), armTick]).gesture;
}

describe('the hold gesture', () => {
  it('is a tap when released before the arm: nothing records, and the click is left to open /capture', () => {
    const { gesture, types } = play([down(), { type: 'tick', now: HOLD_ARM_MS - 1 }, up(HOLD_ARM_MS - 1)]);
    expect(gesture.phase).toBe('idle');
    expect(types).toEqual([]);
  });

  it('arms at the arm time and starts recording', () => {
    const { gesture, types } = play([down(), armTick]);
    expect(gesture.phase).toBe('holding');
    expect(types).toEqual(['start']);
  });

  it('arms at once on a slide past the slop, as a fast slide is no tap', () => {
    expect(play([down(), move(-(TAP_SLOP_PX - 1), 0)]).gesture.phase).toBe('armed');
    const { gesture, types } = play([down(), move(-TAP_SLOP_PX, 0)]);
    expect(gesture.phase).toBe('holding');
    expect(types).toEqual(['start']);
  });

  it('cancels at 110 px left of the press point, not at 109, and swallows the click after', () => {
    expect(play([move(-(CANCEL_DX_PX - 1), 0)], held()).gesture.phase).toBe('holding');
    const { gesture, effects } = play([move(-CANCEL_DX_PX, 0)], held());
    expect(gesture.phase).toBe('cancelled');
    expect(effects).toEqual([
      { type: 'discard' },
      { type: 'cancelFeedback' },
      { type: 'notice', notice: 'cancelled' },
    ]);
    // The rest of the drag is ignored until the finger lifts: no send.
    const after = play([move(0, -LOCK_DY_PX * 2), up(5_000)], gesture);
    expect(after.gesture.phase).toBe('idle');
    expect(after.types).toEqual(['suppressClick']);
  });

  it('measures from the press point, so a press near the screen edge can still cancel', () => {
    // The F6 bug: measured from the disc's edge, cancel needed 80 px past a
    // disc as wide as the phone. From the press point it is 110 px of travel.
    const progress = holdProgress(play([move(-55, 0)], held()).gesture);
    expect(progress.cancel).toBeCloseTo(0.5);
    expect(progress.lock).toBe(0);
  });

  it('locks at 72 px up, and the finger lifting does not send', () => {
    expect(play([move(0, -(LOCK_DY_PX - 1))], held()).gesture.phase).toBe('holding');
    const locked = play([move(0, -LOCK_DY_PX)], held());
    expect(locked.gesture.phase).toBe('locked');
    expect(locked.types).toEqual(['lockFeedback']);
    const lifted = play([up(5_000)], locked.gesture);
    expect(lifted.gesture.phase).toBe('locked');
    expect(lifted.types).toEqual(['suppressClick']);
  });

  it('lets the first line crossed win on a diagonal', () => {
    // Up first, then left: locked, and the later leftward slide does nothing.
    const lockedFirst = play([move(-40, -LOCK_DY_PX), move(-CANCEL_DX_PX - 20, -LOCK_DY_PX)], held());
    expect(lockedFirst.gesture.phase).toBe('locked');
    expect(lockedFirst.types).not.toContain('discard');
    // Left first, then up: cancelled, and the later lock-direction move does nothing.
    const cancelledFirst = play([move(-CANCEL_DX_PX, -30), move(-CANCEL_DX_PX, -LOCK_DY_PX - 20)], held());
    expect(cancelledFirst.gesture.phase).toBe('cancelled');
    expect(cancelledFirst.types).not.toContain('lockFeedback');
  });

  it('sends a release with enough audio', () => {
    const { gesture, effects } = play([up(HOLD_ARM_MS + MIN_TALK_MS)], held());
    expect(gesture.phase).toBe('idle');
    expect(effects).toEqual([
      { type: 'suppressClick' },
      { type: 'send' },
      { type: 'notice', notice: 'sent' },
    ]);
  });

  it('calls a release under 600 ms of audio too short, and discards it', () => {
    const { types, effects } = play([up(HOLD_ARM_MS + MIN_TALK_MS - 1)], held());
    expect(types).toEqual(['suppressClick', 'discard', 'notice', 'errorFeedback']);
    expect(effects).toContainEqual({ type: 'notice', notice: 'short' });
  });

  it('locks a release during the permission prompt instead of calling it too short', () => {
    // The first press on a fresh install: the prompt needs the finger to lift.
    for (const permission of ['prompt', 'unknown'] as const) {
      const { gesture, types } = play([
        down({ permission }),
        armTick,
        up(HOLD_ARM_MS + 50, requesting),
      ]);
      expect(gesture.phase).toBe('locked');
      expect(types).toEqual(['start', 'suppressClick', 'lockFeedback']);
    }
    // Already granted: a release during a slow start is a slip.
    const granted = play([down({ permission: 'granted' }), armTick, up(HOLD_ARM_MS + 50, requesting)]);
    expect(granted.gesture.phase).toBe('idle');
    expect(granted.effects).toContainEqual({ type: 'notice', notice: 'short' });
  });

  it('treats an interruption as a release: it sends, and never discards a real recording', () => {
    const { types } = play(
      [{ type: 'interrupt', now: HOLD_ARM_MS + MIN_TALK_MS, model: recording(HOLD_ARM_MS) }],
      held(),
    );
    expect(types).toEqual(['suppressClick', 'send', 'notice']);
  });

  it('opens review when a locked take stops on its own, never sending or discarding it', () => {
    const locked = play([move(0, -LOCK_DY_PX)], held()).gesture;
    const review: CaptureModel = { ...recording(), state: 'review', startedAt: null, bytes: 900 };
    const { gesture, types } = play([{ type: 'modelChanged', model: review }], locked);
    expect(gesture.phase).toBe('idle');
    expect(types).toEqual(['openCapture']);
  });

  it('asks once before discarding a locked take of ten seconds or more', () => {
    const locked = play([move(0, -LOCK_DY_PX)], held()).gesture;
    // 9.9 s: gone on the first tap.
    const short = play([{ type: 'discard', now: 9_900, model: recording(0) }], locked);
    expect(short.gesture.phase).toBe('idle');
    expect(short.types).toContain('discard');
    // 10 s: the first tap arms "Discard?", the second throws it away.
    const first = play(
      [{ type: 'discard', now: DISCARD_CONFIRM_AFTER_MS, model: recording(0) }],
      locked,
    );
    expect(first.gesture.phase).toBe('locked');
    expect(first.gesture.discardArmedUntil).toBe(DISCARD_CONFIRM_AFTER_MS + DISCARD_CONFIRM_MS);
    expect(first.types).toEqual([]);
    const second = play(
      [{ type: 'discard', now: DISCARD_CONFIRM_AFTER_MS + 1_000, model: recording(0) }],
      first.gesture,
    );
    expect(second.gesture.phase).toBe('idle');
    expect(second.types).toContain('discard');
    // Left alone, "Discard?" lapses and the next tap asks again.
    const lapsed = play(
      [{ type: 'tick', now: DISCARD_CONFIRM_AFTER_MS + DISCARD_CONFIRM_MS }],
      first.gesture,
    );
    expect(lapsed.gesture.discardArmedUntil).toBeNull();
  });

  it('sends from the locked disc, and Stop opens review', () => {
    const locked = play([move(0, -LOCK_DY_PX)], held()).gesture;
    expect(play([{ type: 'send', now: 5_000, model: recording(0) }], locked).types).toEqual([
      'send',
      'notice',
    ]);
    expect(play([{ type: 'stop' }], locked).types).toEqual(['stop']);
  });

  it('stands down while the last recording is leaving, and says so', () => {
    const uploading: CaptureModel = { ...recording(), state: 'uploading', bytes: 900 };
    const { gesture, effects } = play([down({ model: uploading }), armTick]);
    expect(gesture.phase).toBe('standdown');
    expect(effects).toEqual([{ type: 'notice', notice: 'busy' }]);
  });

  it('stands down quietly for a live recording elsewhere, leaving the click to show it', () => {
    const { types } = play([down({ model: recording() }), armTick, up(2_000)]);
    // No start, no notice, and no click suppression: the click opens /capture.
    expect(types).toEqual([]);
  });

  it('starts nothing with the microphone blocked, and says so', () => {
    const { gesture, effects } = play([down({ permission: 'denied' }), armTick]);
    expect(gesture.phase).toBe('standdown');
    expect(effects).toContainEqual({ type: 'notice', notice: 'blocked' });
    expect(effects).not.toContainEqual({ type: 'start' });
  });

  it('taps from Space, but a quick R does nothing', () => {
    expect(play([down({ source: 'space' }), up(100)]).types).toEqual(['openCapture']);
    expect(play([down({ source: 'key' }), up(100)]).types).toEqual([]);
  });

  it('cancels on Escape while holding', () => {
    const { gesture, types } = play([{ type: 'escape', now: 1_000, model: recording() }], held());
    expect(gesture.phase).toBe('cancelled');
    expect(types).toEqual(['discard', 'cancelFeedback', 'notice']);
  });

  it('swallows the click after an Escape inside the arm, so it does not open /capture', () => {
    const { gesture, types } = play([
      down(),
      { type: 'escape', now: 100, model: INITIAL_CAPTURE },
      up(150, INITIAL_CAPTURE),
    ]);
    expect(gesture.phase).toBe('idle');
    expect(types).toEqual(['suppressClick']);
  });

  it('ignores a second press while one is down', () => {
    const first = held();
    expect(holdReducer(first, down({ now: 500 })).gesture).toBe(first);
  });
});
