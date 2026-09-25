import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';

import { hasBufferedAudio, isCaptureBusy, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';

/**
 * Push-to-talk: hold to record, release to send, slide away to cancel.
 *
 * The capture screen is a place — you go there, speak, tap Send, come back.
 * For a thought that is one sentence long that is three screens of travel;
 * the walkie-talkie gesture every messaging app teaches is one. This hook is
 * the gesture, shared by the tab-bar mic (where a plain tap still opens the
 * capture screen, so the hold has to be told apart from the tap) and the
 * `/talk` screen's giant button (where a press is a hold at once).
 *
 * The recording itself is the store's, exactly as from the capture screen:
 * `start` opens the microphone into the target note, `stopAndSend` stops and
 * uploads, and the filing row or the note's filing banner takes it from
 * there. This hook only decides *when* to call which, from the pointer.
 *
 * A hold that yields too little audio is a slip — a thumb brushing the mic
 * on the way to a tab — and is discarded with a hint rather than sent to be
 * transcribed into nothing. A pointer that slides well off the button before
 * release is the cancel gesture, shown as such while it is out there.
 *
 * Nothing here touches history: the overlay the tab bar draws while holding
 * is plain DOM, so system Back still does what it did.
 */

/** How long the tab-bar mic must be held before a press is a hold rather than a tap. */
export const HOLD_DELAY_MS = 350;
/** Fewer milliseconds of audio than this is a slip, not a message. */
export const MIN_TALK_MS = 600;
/** How far off the button the pointer may be before release means cancel. */
export const SLIDE_AWAY_PX = 80;
/** How long the too-short hint and the "Sent" confirmation stay up. */
export const HOLD_NOTICE_MS = 1_500;
/** Movement that turns an armed press into a scroll or a drag, as `useLongPress` draws it. */
const ARM_TOLERANCE_PX = 10;

export type HoldPhase =
  /** Nothing pressed. */
  | 'idle'
  /** Pressed, waiting out `holdDelayMs` to see whether it is a tap. */
  | 'armed'
  /** The microphone has been asked for or is live; release sends. */
  | 'holding'
  /** Released too soon: "Too short — hold to talk", briefly. */
  | 'hint'
  /** Released with a recording: "Sent", briefly. */
  | 'sent'
  /** Pressed while the last recording is still leaving: "Still sending…", briefly. */
  | 'busy';

/**
 * Whether a release sends. The microphone live, or a recording that settled
 * on its own with audio in it — a phone call ended the track, the headset
 * came out, the cap stopped it — while the finger was still down. That
 * audio is the message; the machine's rule is that an interruption yields a
 * partial recording, never a discard, and the gesture keeps to it.
 */
export function holdSendable(model: CaptureModel): boolean {
  return (
    model.state === 'recording' ||
    model.state === 'paused' ||
    ((model.state === 'stopping' || model.state === 'review') && model.bytes > 0)
  );
}

export interface HoldHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: () => void;
  onPointerCancel: () => void;
  onContextMenu: (event: { preventDefault: () => void }) => void;
}

export interface HoldToTalk {
  phase: HoldPhase;
  /** The pointer has slid off: release cancels instead of sending. */
  away: boolean;
  handlers: HoldHandlers;
  /** A press from something other than a pointer — the Space bar. */
  press: () => void;
  release: () => void;
  /** Abandons a hold without sending, as sliding away does. */
  cancel: () => void;
  /** True once after a hold: the click that follows the release is not a tap. */
  consumeClick: () => boolean;
}

export function useHoldToTalk({
  noteId,
  holdDelayMs,
}: {
  /** Where the recording goes; `null` is a new note. Read when the hold begins. */
  noteId: string | null;
  /** Zero for a button whose only job is holding; `HOLD_DELAY_MS` for one that also taps. */
  holdDelayMs: number;
}): HoldToTalk {
  const api = useApi();
  const [phase, setPhaseState] = useState<HoldPhase>('idle');
  const [away, setAwayState] = useState(false);
  // Mirrors of the state for the handlers, which run between renders.
  const phaseRef = useRef<HoldPhase>('idle');
  const awayRef = useRef(false);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const held = useRef(false);
  const target = useRef(noteId);
  useEffect(() => {
    target.current = noteId;
  }, [noteId]);

  const setPhase = useCallback((next: HoldPhase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);
  const setAway = useCallback((next: boolean) => {
    if (awayRef.current === next) return;
    awayRef.current = next;
    setAwayState(next);
  }, []);
  const clearTimer = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
  }, []);
  useEffect(() => clearTimer, [clearTimer]);

  /** Shows a notice for a moment, then rests. */
  const notice = useCallback(
    (kind: 'hint' | 'sent' | 'busy') => {
      setPhase(kind);
      clearTimer();
      timer.current = setTimeout(() => {
        timer.current = null;
        if (phaseRef.current === kind) setPhase('idle');
      }, HOLD_NOTICE_MS);
    },
    [clearTimer, setPhase],
  );

  const begin = useCallback(() => {
    const store = useCaptureStore.getState();
    const { model } = store;
    /*
     * The last recording still leaving the device gets a word, and the hold
     * is not a tap: on a slow connection a long clip takes seconds, and a
     * button that does nothing for those seconds reads as broken. A
     * microphone live on another screen is already stated by the shell's
     * indicator, and a take waiting on the capture screen is that screen's
     * to show — that is where the tap that follows this press goes, so for
     * those the hold simply stands down.
     */
    if (model.state === 'uploading' || model.state === 'stopping') {
      held.current = true;
      notice('busy');
      return;
    }
    if (isCaptureBusy(model) || hasBufferedAudio(model)) {
      setPhase('idle');
      return;
    }
    // A finished or failed capture nobody released, as the capture screen clears it.
    if (model.state !== 'idle') store.reset();
    held.current = true;
    setAway(false);
    setPhase('holding');
    void store.start(target.current);
  }, [notice, setAway, setPhase]);

  const press = useCallback(() => {
    if (phaseRef.current === 'armed' || phaseRef.current === 'holding') return;
    clearTimer();
    held.current = false;
    if (holdDelayMs <= 0) {
      begin();
      return;
    }
    setPhase('armed');
    timer.current = setTimeout(() => {
      timer.current = null;
      if (phaseRef.current === 'armed') begin();
    }, holdDelayMs);
  }, [begin, clearTimer, holdDelayMs, setPhase]);

  // The timer is the hold delay's only while armed; after that it is a
  // notice's, which outlives the press that raised it.
  const cancel = useCallback(() => {
    origin.current = null;
    if (phaseRef.current === 'armed') clearTimer();
    if (phaseRef.current === 'holding') void useCaptureStore.getState().discard();
    if (phaseRef.current === 'armed' || phaseRef.current === 'holding') setPhase('idle');
    setAway(false);
  }, [clearTimer, setAway, setPhase]);

  const release = useCallback(() => {
    origin.current = null;
    if (phaseRef.current === 'armed') {
      // A tap. The click that follows is the button's own.
      clearTimer();
      setPhase('idle');
      return;
    }
    if (phaseRef.current !== 'holding') return;
    const store = useCaptureStore.getState();
    const { model } = store;
    const wasAway = awayRef.current;
    setAway(false);

    if (model.state === 'failed') {
      /*
       * The microphone was refused or is missing. Nothing to discard, and
       * nothing here to say it with: the click that follows goes to the
       * capture screen, whose failure card explains and offers Try again.
       */
      held.current = false;
      setPhase('idle');
      return;
    }
    // A settled recording's clock has stopped; a running one's is read now.
    const elapsed =
      model.startedAt === null ? model.elapsedMs : model.accumulatedMs + Date.now() - model.startedAt;
    if (wasAway || !holdSendable(model) || elapsed < MIN_TALK_MS) {
      void store.discard();
      if (wasAway) setPhase('idle');
      else notice('hint');
      return;
    }
    void store.stopAndSend(api);
    notice('sent');
  }, [api, clearTimer, notice, setAway, setPhase]);

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      // The primary button only: a right-click is the context menu's.
      if (event.button !== 0) return;
      origin.current = { x: event.clientX, y: event.clientY };
      /*
       * The pointer stays this element's while held, so sliding off the disc
       * still reports movement and the release, whichever element it ends
       * on. jsdom has no pointer capture; the gesture works without it there.
       */
      try {
        event.currentTarget.setPointerCapture?.(event.pointerId);
      } catch {
        /* A pointer that is already gone. */
      }
      press();
    },
    [press],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const start = origin.current;
      if (!start) return;
      const travelled = Math.hypot(event.clientX - start.x, event.clientY - start.y);
      if (phaseRef.current === 'armed' && travelled > ARM_TOLERANCE_PX) {
        // A scroll or a drag that began on the mic, not a press: neither a
        // hold nor, when it ends, a tap.
        held.current = true;
        cancel();
        return;
      }
      if (phaseRef.current !== 'holding') return;
      /*
       * Away is measured from the button's edge, not the press point: on
       * the `/talk` disc a thumb can drift a hand's width and still be well
       * inside it, and a message must not be lost to that.
       */
      const box = event.currentTarget.getBoundingClientRect();
      const dx = Math.max(box.left - event.clientX, 0, event.clientX - box.right);
      const dy = Math.max(box.top - event.clientY, 0, event.clientY - box.bottom);
      setAway(Math.hypot(dx, dy) > SLIDE_AWAY_PX);
    },
    [cancel, setAway],
  );

  const onPointerCancel = useCallback(() => {
    // The browser took the pointer — a scroll, an app switch. Not a send.
    held.current = held.current || phaseRef.current === 'armed';
    cancel();
  }, [cancel]);

  const onContextMenu = useCallback((event: { preventDefault: () => void }) => {
    // Android raises the context menu for the same hold; iOS starts text
    // selection from it. Neither belongs on a button being held.
    if (phaseRef.current === 'armed' || phaseRef.current === 'holding' || held.current) {
      event.preventDefault();
    }
  }, []);

  const consumeClick = useCallback(() => {
    const was = held.current;
    held.current = false;
    return was;
  }, []);

  return {
    phase,
    away,
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp: release,
      onPointerCancel,
      onContextMenu,
    },
    press,
    release,
    cancel,
    consumeClick,
  };
}
