import { useCallback, useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { TargetChooser } from './TargetChooser.tsx';
import { Waveform } from './Waveform.tsx';
import {
  canRetryUpload,
  formatElapsed,
  hasBufferedAudio,
  isCaptureBusy,
  MAX_DURATION_MS,
  type CaptureModel,
} from './machine.ts';
import { useCaptureStore } from './store.ts';

/**
 * The capture surface. Full screen: the shell drops the tab bar here, so the
 * only way off is Stop, Cancel or system Back — and Back keeps the microphone
 * open, with the shell's recording indicator saying so on the next screen.
 *
 * Every claim this screen makes is one the machine can back: it says
 * "Starting" while `getUserMedia` is pending and only says "Recording" once
 * the stream is live, because a UI that announces a recording that has not
 * begun is how people lose the first sentence of a thought.
 */
export function CaptureScreen() {
  const navigate = useNavigate();
  const api = useApi();
  const [params] = useSearchParams();
  const reducedMotion = useReducedMotion();

  const model = useCaptureStore((state) => state.model);
  const start = useCaptureStore((state) => state.start);
  const setTarget = useCaptureStore((state) => state.setTarget);
  const pause = useCaptureStore((state) => state.pause);
  const resume = useCaptureStore((state) => state.resume);
  const stop = useCaptureStore((state) => state.stop);
  const discard = useCaptureStore((state) => state.discard);
  const send = useCaptureStore((state) => state.send);
  const reset = useCaptureStore((state) => state.reset);
  const amplitudes = useCaptureStore((state) => state.amplitudes);

  const noteId = params.get('note');

  /*
   * Opening /capture starts a recording. The route IS the intent; requiring a
   * second tap once here would put a dead screen between the user and the
   * microphone.
   *
   * The guard used to be `state === 'idle'`, which meant exactly one recording
   * per page load: a sent capture leaves the machine in the terminal `uploaded`
   * state and nothing moved it back, so every later tap of Record landed on a
   * screen showing "Sent" and the previous recording's clock, bounced home, and
   * never opened the microphone. Only a reload recovered.
   *
   * So: arriving with audio the server has not got yet keeps that screen — those
   * bytes exist in exactly one place and are the user's to send or discard.
   * Anything else — idle, a finished capture, a failure with nothing buffered —
   * is cleared and a fresh recording is minted.
   */
  useEffect(() => {
    const current = useCaptureStore.getState().model;
    // Unsent audio: leave it on screen with its Send / Discard controls.
    if (hasBufferedAudio(current)) return;
    // A live recording this screen is being re-entered on.
    if (isCaptureBusy(current)) return;
    if (current.state !== 'idle') reset();
    void start(noteId);
    // Intentionally not re-run on model changes: this arms the recording once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /*
   * A finished capture belongs to the library's filing row from here on, so the
   * machine is released as this screen goes away. Without it the terminal state
   * outlives the screen and wedges the next recording.
   */
  useEffect(
    () => () => {
      if (useCaptureStore.getState().model.state === 'uploaded') {
        useCaptureStore.getState().reset();
      }
    },
    [],
  );

  /*
   * Back to the library, *replacing* this entry rather than pushing over it.
   * Opening /capture starts a recording, so leaving a `/capture` entry behind
   * would turn the next Back press into a new recording nobody asked for.
   */
  const leave = useCallback(() => {
    void navigate(ROUTES.home, { replace: true });
  }, [navigate]);

  useEffect(() => {
    if (model.state !== 'uploaded') return;
    // Hand off to the filing row at the top of the library, which shows the
    // pipeline's progress from here and survives this screen unmounting.
    const timer = setTimeout(leave, 600);
    return () => {
      clearTimeout(timer);
    };
  }, [model.state, leave]);

  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);

  return (
    <div className="capture">
      <h1 className="visually-hidden">Recording</h1>

      <p className="capture__state" role="status" aria-live="polite">
        {statusLabel(model)}
      </p>

      {/*
        Where it will file, shown and changeable before you speak. Read from
        the machine, not from the URL: `?note=` seeds the target at `start`, and
        the chooser can move it until Send reads it.
      */}
      <TargetChooser
        noteId={model.noteId}
        onChoose={setTarget}
        disabled={
          model.state === 'idle' || model.state === 'uploading' || model.state === 'uploaded'
        }
      />

      <p
        className="capture__timer numeric"
        aria-label={`Elapsed ${formatElapsed(model.elapsedMs)}`}
      >
        {formatElapsed(model.elapsedMs)}
      </p>

      <div className="capture__waveform">
        <Waveform
          read={read}
          active={model.state === 'recording'}
          reducedMotion={reducedMotion}
          label={model.state === 'recording' ? 'Live audio level' : 'Audio level'}
        />
      </div>

      {(model.state === 'requesting' ||
        model.state === 'recording' ||
        model.state === 'paused') && (
        <p className="capture__hint">
          Say &ldquo;add this to my roof note&rdquo; or pick the note above.
        </p>
      )}

      {model.nearDurationLimit && model.state === 'recording' && (
        <p className="capture__warning" role="status">
          Approaching the {Math.round(MAX_DURATION_MS / 60_000)}-minute limit.
        </p>
      )}

      {model.capReached && (
        <p className="capture__warning">
          {model.capReached === 'duration'
            ? 'The recording reached the length limit and was stopped. Nothing was lost.'
            : 'The recording reached the size limit and was stopped. Nothing was lost.'}
        </p>
      )}

      {model.micReturned && model.state === 'recording' && (
        <p className="capture__notice" role="status">
          Resumed — the microphone is back.
        </p>
      )}

      {model.interrupted && model.state !== 'recording' && (
        <p className="capture__warning">
          The recording was interrupted. What was captured before that is here.
        </p>
      )}

      {model.failure && (
        <p className="capture__error" role="alert">
          {model.failure.message}
        </p>
      )}

      <Controls
        model={model}
        onPause={pause}
        onResume={resume}
        onStop={() => void stop()}
        onDiscard={() => {
          void discard().then(leave);
        }}
        onSend={() => void send(api)}
        onLeave={leave}
      />
    </div>
  );
}

function statusLabel(model: CaptureModel): string {
  switch (model.state) {
    case 'requesting':
      // Not "Recording". Nothing claims a recording before getUserMedia
      // resolves.
      return 'Starting the microphone…';
    case 'recording':
      return 'Recording';
    case 'paused':
      // The OS lent the microphone to a call or another app. Said plainly, and
      // undone by itself when the track unmutes — see the machine.
      return model.micTaken ? 'Paused — the microphone was taken by another app' : 'Paused';
    case 'stopping':
      return 'Finishing…';
    case 'review':
      return 'Ready to send';
    case 'uploading':
      return 'Sending…';
    case 'uploaded':
      return 'Sent';
    case 'failed':
      return 'Something went wrong';
    default:
      return '';
  }
}

interface ControlsProps {
  model: CaptureModel;
  onPause: () => void;
  onResume: () => void;
  onStop: () => void;
  onDiscard: () => void;
  onSend: () => void;
  onLeave: () => void;
}

/** Pause, cancel, and stop are distinct controls — never one toggle. */
function Controls({
  model,
  onPause,
  onResume,
  onStop,
  onDiscard,
  onSend,
  onLeave,
}: ControlsProps) {
  if (model.state === 'requesting') {
    return (
      <div className="capture__controls">
        <button type="button" className="capture__control" onClick={onDiscard}>
          <span>Cancel</span>
        </button>
      </div>
    );
  }

  if (model.state === 'recording' || model.state === 'paused') {
    return (
      <div className="capture__controls">
        <button type="button" className="capture__control" onClick={onDiscard}>
          <span>Cancel</span>
        </button>
        <button
          type="button"
          className="capture__control"
          onClick={model.state === 'paused' ? onResume : onPause}
        >
          <span>{model.state === 'paused' ? 'Resume' : 'Pause'}</span>
        </button>
        <button type="button" className="capture__control capture__control--primary" onClick={onStop}>
          <Icon name="stop" size={20} />
          <span>Stop</span>
        </button>
      </div>
    );
  }

  if (model.state === 'review') {
    return (
      <div className="capture__controls">
        <button type="button" className="capture__control" onClick={onDiscard}>
          <span>Discard</span>
        </button>
        <button type="button" className="capture__control capture__control--primary" onClick={onSend}>
          <span>Send</span>
        </button>
      </div>
    );
  }

  if (model.state === 'uploading') {
    return (
      <p className="capture__progress" role="status" aria-live="polite">
        Sending <span className="numeric">{Math.round(model.uploadProgress * 100)}</span>%
      </p>
    );
  }

  if (model.state === 'failed') {
    return (
      <div className="capture__controls">
        <button type="button" className="capture__control" onClick={onDiscard}>
          <span>{canRetryUpload(model) ? 'Discard' : 'Close'}</span>
        </button>
        {canRetryUpload(model) && (
          <button
            type="button"
            className="capture__control capture__control--primary"
            onClick={onSend}
          >
            <span>Try again</span>
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="capture__controls">
      <button type="button" className="capture__control" onClick={onLeave}>
        <span>Done</span>
      </button>
    </div>
  );
}
