import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { ROUTES } from '@/app/routes.ts';

import { cancelFeedback, errorFeedback, lockFeedback } from './feedback.ts';
import {
  holdReducer,
  IDLE_GESTURE,
  type Gesture,
  type GestureEvent,
  type HoldEffect,
  type HoldNotice,
  type MicPermission,
} from './holdGesture.ts';
import {
  BLOCKED_NOTICE_MS,
  CLICK_SUPPRESS_MS,
  DISCARD_CONFIRM_MS,
  HOLD_ARM_MS,
  HOLD_NOTICE_MS,
} from './holdTiming.ts';
import { useCaptureStore } from './store.ts';

/**
 * Push-to-talk on the tab bar's record disc (R8, F5–F7).
 *
 * The gesture's rules are `holdGesture.ts`; this hook wires them to the
 * browser. It turns pointer, key, timer, focus and page-visibility events
 * into reducer events, and runs what comes back against the capture store,
 * the router and the haptics. The recording itself is the store's, exactly
 * as from the capture screen, so the filing row and the note's banner take
 * it from there.
 *
 * Nothing here touches history: the bar draws the hold in place, so system
 * Back still does what it did, and a locked recording survives a route
 * change because the bar is the shell's.
 */

export interface HoldHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
  onLostPointerCapture: (event: ReactPointerEvent<HTMLElement>) => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  onKeyUp: (event: ReactKeyboardEvent<HTMLElement>) => void;
  onContextMenu: (event: { preventDefault: () => void }) => void;
  onClick: () => void;
}

export interface HoldToTalk {
  gesture: Gesture;
  /** The notice in the clock pill, if one is up. */
  notice: HoldNotice | null;
  handlers: HoldHandlers;
  /** The locked bar's buttons. */
  send: () => void;
  stop: () => void;
  discard: () => void;
}

/**
 * The microphone permission, kept live. Read at the press, so a refused
 * microphone stands the hold down and a first press that raises the prompt
 * locks instead of failing as too short. Firefox throws on the query; that
 * is `unknown`, which is treated as the prompt.
 */
function usePermission(): RefObject<MicPermission> {
  const permission = useRef<MicPermission>('unknown');
  useEffect(() => {
    let status: PermissionStatus | null = null;
    let gone = false;
    const update = (): void => {
      if (status) permission.current = status.state;
    };
    try {
      navigator.permissions
        ?.query({ name: 'microphone' as PermissionName })
        .then((answer) => {
          if (gone) return;
          status = answer;
          update();
          answer.addEventListener('change', update);
        })
        .catch(() => {
          /* Not a queryable permission here. */
        });
    } catch {
      /* As above, thrown rather than rejected. */
    }
    return () => {
      gone = true;
      status?.removeEventListener('change', update);
    };
  }, []);
  return permission;
}

/** Whether a key press belongs to a field rather than to the app. */
function inField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || target.closest('input, textarea, select') !== null;
}

export function useHoldToTalk({
  noteId,
  onSent,
}: {
  /** Where a new recording goes; `null` is a new note. Read when the hold begins. */
  noteId: string | null;
  /** A hold that sent, for the first-run coach to retire itself. */
  onSent?: () => void;
}): HoldToTalk {
  const api = useApi();
  const navigate = useNavigate();
  const permission = usePermission();
  const [gesture, setGesture] = useState<Gesture>(IDLE_GESTURE);
  const [notice, setNotice] = useState<HoldNotice | null>(null);
  const current = useRef<Gesture>(IDLE_GESTURE);
  const pointerId = useRef<number | null>(null);
  const suppressUntil = useRef(0);
  const timers = useRef({
    arm: undefined as ReturnType<typeof setTimeout> | undefined,
    confirm: undefined as ReturnType<typeof setTimeout> | undefined,
    notice: undefined as ReturnType<typeof setTimeout> | undefined,
  });
  const target = useRef(noteId);
  useEffect(() => {
    target.current = noteId;
  }, [noteId]);

  useEffect(() => {
    const pending = timers.current;
    return () => {
      clearTimeout(pending.arm);
      clearTimeout(pending.confirm);
      clearTimeout(pending.notice);
    };
  }, []);

  const openCapture = useCallback(() => {
    // The recording's own note when there is one, else the bar's target.
    const { model } = useCaptureStore.getState();
    const into = model.state === 'idle' ? target.current : model.noteId;
    void navigate(into ? ROUTES.captureInto(into) : ROUTES.capture);
  }, [navigate]);

  // Assigned below; effects that dispatch read it through this ref.
  const dispatchRef = useRef<(event: GestureEvent) => void>(() => {});

  const run = useCallback(
    (effect: HoldEffect) => {
      const store = useCaptureStore.getState();
      switch (effect.type) {
        case 'start':
          // A finished or failed capture nobody released, as the capture screen clears it.
          if (store.model.state !== 'idle') store.reset();
          void store.start(target.current);
          return;
        case 'discard':
          void store.discard();
          return;
        case 'send':
          void store.stopAndSend(api);
          return;
        case 'stop':
          void store.stop();
          openCapture();
          return;
        case 'openCapture':
          openCapture();
          return;
        case 'notice': {
          if (effect.notice === 'sent') onSent?.();
          setNotice(effect.notice);
          clearTimeout(timers.current.notice);
          const shown = effect.notice;
          timers.current.notice = setTimeout(
            () => {
              setNotice((now) => (now === shown ? null : now));
            },
            shown === 'blocked' ? BLOCKED_NOTICE_MS : HOLD_NOTICE_MS,
          );
          return;
        }
        case 'lockFeedback':
          lockFeedback();
          // A keyboard lock has no finger on the disc; Send is the next key.
          if (current.current.source !== 'pointer') {
            document.querySelector<HTMLButtonElement>('.record-button')?.focus();
          }
          return;
        case 'cancelFeedback':
          cancelFeedback();
          return;
        case 'errorFeedback':
          errorFeedback();
          return;
        case 'suppressClick':
          suppressUntil.current = Date.now() + CLICK_SUPPRESS_MS;
          return;
      }
    },
    [api, openCapture, onSent],
  );

  const dispatch = useCallback(
    (event: GestureEvent) => {
      const before = current.current;
      const { gesture: after, effects } = holdReducer(before, event);
      current.current = after;
      if (after !== before) setGesture(after);
      if (after.phase === 'armed' && before.phase !== 'armed') {
        clearTimeout(timers.current.arm);
        timers.current.arm = setTimeout(() => {
          dispatchRef.current({ type: 'tick', now: Date.now() });
        }, HOLD_ARM_MS);
      }
      if (after.discardArmedUntil !== null && after.discardArmedUntil !== before.discardArmedUntil) {
        clearTimeout(timers.current.confirm);
        timers.current.confirm = setTimeout(() => {
          dispatchRef.current({ type: 'tick', now: Date.now() });
        }, DISCARD_CONFIRM_MS);
      }
      for (const effect of effects) run(effect);
    },
    [run],
  );
  useEffect(() => {
    dispatchRef.current = dispatch;
  }, [dispatch]);

  const model = () => useCaptureStore.getState().model;

  const press = useCallback(
    (source: Gesture['source'], x = 0, y = 0) => {
      dispatch({
        type: 'down',
        x,
        y,
        now: Date.now(),
        source,
        model: model(),
        permission: permission.current,
      });
    },
    [dispatch, permission],
  );
  const up = useCallback(() => {
    dispatch({ type: 'up', now: Date.now(), model: model() });
  }, [dispatch]);
  const interrupt = useCallback(() => {
    pointerId.current = null;
    dispatch({ type: 'interrupt', now: Date.now(), model: model() });
  }, [dispatch]);

  // The store's word reaches a hold in progress: a failure, or a locked take
  // the call or the cap stopped.
  useEffect(
    () =>
      useCaptureStore.subscribe((state, previous) => {
        if (state.model === previous.model) return;
        const { phase } = current.current;
        if (phase === 'holding' || phase === 'locked') {
          dispatch({ type: 'modelChanged', model: state.model });
        }
      }),
    [dispatch],
  );

  /*
   * R held from anywhere is push-to-talk on a keyboard, and Escape cancels.
   * Not from a field, and not with a modifier, so typing and the browser's
   * own shortcuts keep their keys. Space is the focused disc's alone: on the
   * page it scrolls the content.
   *
   * The window losing focus mid key-hold takes the keyup with it (Alt-Tab, a
   * notification), so it counts as a release: what was said is sent, or a
   * slip gets the hint. It used to discard. A pointer hold ignores blur:
   * the permission prompt takes focus without the finger leaving the disc.
   *
   * A hidden page (a call, the lock screen, an app switch) ends a hold as a
   * release too, not a discard. A locked recording carries on, as it does
   * on the capture screen.
   */
  useEffect(() => {
    const isR = (event: KeyboardEvent): boolean =>
      event.key.toLowerCase() === 'r' &&
      !event.altKey &&
      !event.ctrlKey &&
      !event.metaKey &&
      !event.shiftKey;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape' && current.current.phase !== 'idle') {
        dispatch({ type: 'escape', now: Date.now(), model: model() });
        return;
      }
      if (!isR(event) || event.repeat || inField(event.target)) return;
      press('key');
    };
    const onKeyUp = (event: KeyboardEvent): void => {
      if (event.key.toLowerCase() !== 'r' || current.current.source !== 'key') return;
      if (current.current.pressing) up();
    };
    const onBlur = (): void => {
      const { source, pressing } = current.current;
      if (pressing && source !== 'pointer') interrupt();
    };
    const onHidden = (): void => {
      if (document.visibilityState === 'hidden' && current.current.pressing) interrupt();
    };
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('keyup', onKeyUp);
    window.addEventListener('blur', onBlur);
    document.addEventListener('visibilitychange', onHidden);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('keyup', onKeyUp);
      window.removeEventListener('blur', onBlur);
      document.removeEventListener('visibilitychange', onHidden);
    };
  }, [dispatch, press, up, interrupt]);

  const handlers: HoldHandlers = {
    onPointerDown: (event) => {
      // The click after a hold is swallowed only until the next press.
      suppressUntil.current = 0;
      // The primary button of the first finger only; a second finger is ignored.
      if (event.button !== 0 || pointerId.current !== null) return;
      if (current.current.phase !== 'idle') return;
      pointerId.current = event.pointerId;
      /*
       * The pointer stays the disc's while held, so the slide off it still
       * reports movement and the release, wherever it ends. jsdom has no
       * pointer capture; the gesture works without it there.
       */
      try {
        event.currentTarget.setPointerCapture?.(event.pointerId);
      } catch {
        /* A pointer that is already gone. */
      }
      press('pointer', event.clientX, event.clientY);
    },
    onPointerMove: (event) => {
      if (event.pointerId !== pointerId.current) return;
      dispatch({ type: 'move', x: event.clientX, y: event.clientY });
    },
    onPointerUp: (event) => {
      if (event.pointerId !== pointerId.current) return;
      pointerId.current = null;
      up();
    },
    // The browser took the pointer. A release, never a discard.
    onPointerCancel: (event) => {
      if (event.pointerId === pointerId.current) interrupt();
    },
    onLostPointerCapture: (event) => {
      if (event.pointerId === pointerId.current) interrupt();
    },
    /*
     * Space on the focused disc holds as a finger does, with the same arm:
     * a quick press is a tap. Its default is stopped so the button's own
     * click does not follow the release. While locked, Space is the plain
     * button press it always was, which is Send.
     */
    onKeyDown: (event) => {
      if (event.key !== ' ' || current.current.phase === 'locked') return;
      event.preventDefault();
      if (!event.repeat) press('space');
    },
    onKeyUp: (event) => {
      if (event.key !== ' ' || current.current.source !== 'space') return;
      event.preventDefault();
      if (current.current.pressing) up();
    },
    // Android raises the context menu for a long press, and iOS starts text
    // selection from it. Neither belongs on a button that is held.
    onContextMenu: (event) => {
      event.preventDefault();
    },
    /*
     * A tap is the click, not the pointerup: TalkBack, VoiceOver and Enter
     * send only a click. The click Chromium sends after a long press is
     * swallowed (QA B-1, 2026-09-27).
     */
    onClick: () => {
      if (Date.now() < suppressUntil.current) return;
      if (current.current.phase === 'locked') {
        dispatch({ type: 'send', now: Date.now(), model: model() });
        return;
      }
      if (current.current.phase !== 'idle') return;
      void navigate(target.current ? ROUTES.captureInto(target.current) : ROUTES.capture);
    },
  };

  return {
    gesture,
    notice,
    handlers,
    send: () => {
      dispatch({ type: 'send', now: Date.now(), model: model() });
    },
    stop: () => {
      dispatch({ type: 'stop' });
    },
    discard: () => {
      dispatch({ type: 'discard', now: Date.now(), model: model() });
    },
  };
}
