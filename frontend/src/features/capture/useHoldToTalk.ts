import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';

import { hasBufferedAudio, isCaptureBusy, type CaptureModel } from './machine.ts';
import { HOLD_NOTICE_MS, MIN_TALK_MS, SLIDE_AWAY_PX } from './holdTiming.ts';
import { useCaptureStore } from './store.ts';

/**
 * Push-to-talk: hold to record, release to send, slide away to cancel.
 *
 * The capture screen is a place — you go there, speak, tap Send, come back.
 * For a thought that is one sentence long that is three screens of travel;
 * the walkie-talkie gesture every messaging app teaches is one. This hook is
 * the gesture, for the `/talk` screen's giant button: a press is a hold from
 * the first frame. The tab-bar disc used to share it behind a 350 ms delay
 * that told a hold from a tap; the owner wants the hold on the widget only
 * (feedback 2026-09-27), so the armed phase and the delay went with it.
 *
 * The recording itself is the store's, exactly as from the capture screen:
 * `start` opens the microphone into the target note, `stopAndSend` stops and
 * uploads, and the filing row or the note's filing banner takes it from
 * there. This hook only decides *when* to call which, from the pointer.
 *
 * A hold that yields too little audio is a slip — a thumb brushing the disc
 * on the way past — and is discarded with a hint rather than sent to be
 * transcribed into nothing. A pointer that slides well off the button before
 * release is the cancel gesture, shown as such while it is out there.
 *
 * Nothing here touches history: what the screen draws while holding is plain
 * DOM, so system Back still does what it did.
 */

// The timings live in `holdTiming.ts` so the e2e specs can import them; they
// are still this hook's, and everything that reads them reads them from here.
export { HOLD_NOTICE_MS, MIN_TALK_MS } from './holdTiming.ts';

export type HoldPhase =
  /** Nothing pressed. */
  | 'idle'
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
function holdSendable(model: CaptureModel): boolean {
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
}

export function useHoldToTalk({
  noteId,
}: {
  /** Where the recording goes; `null` is a new note. Read when the hold begins. */
  noteId: string | null;
}): HoldToTalk {
  const api = useApi();
  const [phase, setPhaseState] = useState<HoldPhase>('idle');
  const [away, setAwayState] = useState(false);
  // Mirrors of the state for the handlers, which run between renders.
  const phaseRef = useRef<HoldPhase>('idle');
  const awayRef = useRef(false);
  /** Set while a pointer is down on the button: the Space bar has no position to slide. */
  const pointerDown = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
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

  const press = useCallback(() => {
    if (phaseRef.current === 'holding') return;
    clearTimer();
    const store = useCaptureStore.getState();
    const { model } = store;
    /*
     * The last recording still leaving the device gets a word: on a slow
     * connection a long clip takes seconds, and a button that does nothing
     * for those seconds reads as broken. A microphone live on another
     * screen is already stated by the shell's indicator, and a take waiting
     * on the capture screen is that screen's to show, so for those the hold
     * simply stands down.
     */
    if (model.state === 'uploading' || model.state === 'stopping') {
      notice('busy');
      return;
    }
    if (isCaptureBusy(model) || hasBufferedAudio(model)) {
      setPhase('idle');
      return;
    }
    // A finished or failed capture nobody released, as the capture screen clears it.
    if (model.state !== 'idle') store.reset();
    setAway(false);
    setPhase('holding');
    void store.start(target.current);
  }, [clearTimer, notice, setAway, setPhase]);

  // A notice's timer outlives the press that raised it, so a cancel leaves it be.
  const cancel = useCallback(() => {
    pointerDown.current = false;
    if (phaseRef.current === 'holding') {
      void useCaptureStore.getState().discard();
      setPhase('idle');
    }
    setAway(false);
  }, [setAway, setPhase]);

  const release = useCallback(() => {
    pointerDown.current = false;
    if (phaseRef.current !== 'holding') return;
    const store = useCaptureStore.getState();
    const { model } = store;
    const wasAway = awayRef.current;
    setAway(false);

    if (model.state === 'failed') {
      // The microphone was refused or is missing. Nothing to discard, and
      // the screen's failure line says why.
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
  }, [api, notice, setAway, setPhase]);

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      // The primary button only: a right-click is the context menu's.
      if (event.button !== 0) return;
      pointerDown.current = true;
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
      if (!pointerDown.current || phaseRef.current !== 'holding') return;
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
    [setAway],
  );

  /*
   * The OS taking the page mid-hold — a call, the lock screen, an app switch —
   * ends the hold, and not every browser sends `pointercancel` for it. Left
   * alone the microphone stayed open until the next press, whose release sent
   * everything recorded meanwhile. It ends as a release, not a cancel: what
   * was said before the call is the message, and the rule above is that an
   * interruption yields a partial recording, never a discard — a slip under
   * `MIN_TALK_MS` still gets the hint. `visibilitychange`, not `blur`: the
   * permission prompt takes focus without hiding the page, and a hold must
   * survive the prompt it raised.
   */
  useEffect(() => {
    const onHidden = (): void => {
      if (document.visibilityState !== 'hidden') return;
      if (phaseRef.current === 'holding') release();
      else cancel();
    };
    document.addEventListener('visibilitychange', onHidden);
    return () => {
      document.removeEventListener('visibilitychange', onHidden);
    };
  }, [cancel, release]);

  // Android raises the context menu for the same hold; iOS starts text
  // selection from it. Neither belongs on a button whose only job is holding.
  const onContextMenu = useCallback((event: { preventDefault: () => void }) => {
    event.preventDefault();
  }, []);

  return {
    phase,
    away,
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp: release,
      // The browser took the pointer — a scroll, an app switch. Not a send.
      onPointerCancel: cancel,
      onContextMenu,
    },
    press,
    release,
    cancel,
  };
}
