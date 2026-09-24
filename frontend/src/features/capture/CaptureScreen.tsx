import { useCallback, useEffect, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { ROUTES } from '@/app/routes.ts';
import { Icon, type IconName } from '@/components/Icon.tsx';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

import { ReviewPlayer } from './ReviewPlayer.tsx';
import { TargetChooser } from './TargetChooser.tsx';
import { Waveform } from './Waveform.tsx';
import {
  canRetryUpload,
  formatElapsed,
  hasBufferedAudio,
  isCaptureBusy,
  isLive,
  MAX_DURATION_MS,
  type CaptureFailure,
  type CaptureModel,
} from './machine.ts';
import { useCaptureStore } from './store.ts';

/**
 * The capture surface. Full screen: the shell drops the tab bar here, so the
 * only way off is Stop, Send, Cancel or system Back — and Back keeps the
 * microphone open, with the shell's recording indicator saying so on the next
 * screen.
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
  const stopAndSend = useCaptureStore((state) => state.stopAndSend);
  const discard = useCaptureStore((state) => state.discard);
  const rerecord = useCaptureStore((state) => state.rerecord);
  const send = useCaptureStore((state) => state.send);
  const reset = useCaptureStore((state) => state.reset);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const clip = useCaptureStore((state) => state.clip);
  const envelope = useCaptureStore((state) => state.envelope);

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
   * Leaves, *replacing* this entry rather than pushing over it. Opening
   * /capture starts a recording, so leaving a `/capture` entry behind would
   * turn the next Back press into a new recording nobody asked for. Where to
   * is `captureReturnPath`'s call: the note the recording was aimed at, or
   * the library.
   */
  const leave = useCallback(
    (to: string) => {
      void navigate(to, { replace: true });
    },
    [navigate],
  );

  /*
   * Only reachable by coming back to this screen mid-upload (the indicator on
   * another screen, or Record tapped again): Send itself leaves at once. Once
   * the upload lands there is nothing left here to look at.
   */
  useEffect(() => {
    if (model.state !== 'uploaded') return;
    const to = captureReturnPath(model.noteId);
    const timer = setTimeout(() => {
      leave(to);
    }, 600);
    return () => {
      clearTimeout(timer);
    };
  }, [model.state, model.noteId, leave]);

  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);

  /*
   * Send does not wait. The upload lives in the store, outside this tree, and
   * the filing row shows it from "Uploading… 40%" through to "Filed" — so the
   * only thing keeping the user on a screen that said "Sending" was the
   * screen. The promise is deliberately not awaited: its outcome is reported
   * through the machine, which the row reads. The target is read before the
   * hand-off: `send` is what the machine's `noteId` is for, and where the user
   * goes next is decided by the same fact.
   *
   * From the recording screen Send is Stop and Send in one tap: the store
   * stops the recorder and sends the moment its last chunk has landed, and
   * this screen leaves just the same — the filing row picks the upload up.
   */
  const handOff = useCallback(() => {
    const { state, noteId } = useCaptureStore.getState().model;
    const to = captureReturnPath(noteId);
    void (state === 'recording' || state === 'paused' ? stopAndSend(api) : send(api));
    leave(to);
  }, [send, stopAndSend, api, leave]);

  // The envelope is read once per review, not per render: the recorder has
  // finished, so it will not change, and the player redraws on its own clock.
  const reviewEnvelope = useMemo(
    () => (model.state === 'review' ? envelope() : []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [model.state, model.localId],
  );

  return (
    <div className="capture">
      <h1 className="visually-hidden">Recording</h1>

      {/*
        Live is signalled, not just spelt: a pulsing accent dot before the
        word, and the clock in accent, for as long as the microphone is open.
        The eyebrow was muted grey in every state, so "Recording" and
        "Paused" read the same at arm's length.
      */}
      <p
        className="capture__state"
        role="status"
        aria-live="polite"
        data-live={isLive(model) || undefined}
      >
        {isLive(model) && <span className="capture__state-dot" aria-hidden="true" />}
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
          model.state === 'idle' ||
          model.state === 'uploading' ||
          model.state === 'uploaded' ||
          model.state === 'failed'
        }
        // Nothing but the microphone request goes out until the stream is
        // live; the list is for the pill, and the pill can wait a second.
        fetchList={model.state !== 'idle' && model.state !== 'requesting'}
      />

      {model.state === 'failed' && model.failure ? (
        /*
          The failure takes the place of the clock and the level box. Left
          in place they said `00:00` over an empty panel under a live "Into"
          pill — a dead screen dressed as a working one, with no word on
          what to do next.
        */
        <FailureCard failure={model.failure} />
      ) : (
        <>
          <p
            className="capture__timer numeric"
            aria-label={`Elapsed ${formatElapsed(model.elapsedMs)}`}
            data-live={isLive(model) || undefined}
          >
            {formatElapsed(model.elapsedMs)}
          </p>

          {model.state === 'review' ? (
            /*
              Listen back before it goes anywhere. The live canvas gives way to
              the recording's own waveform, scrollable and seekable, with
              play/pause.
            */
            <ReviewPlayer
              key={model.localId}
              clip={clip}
              envelope={reviewEnvelope}
              durationMs={model.elapsedMs}
            />
          ) : (
            <div className="capture__waveform">
              {/*
                Keyed by the recording, so each one gets a fresh canvas. A
                canvas keeps its last bitmap until something repaints it, and
                the waveform's draw effect only re-runs when `active` flips —
                which is when the stream is live, a microphone request later.
                Without the key, whatever the element showed on this screen's
                first frame stayed on it for that whole wait.
              */}
              <Waveform
                key={model.localId}
                read={read}
                active={model.state === 'recording'}
                reducedMotion={reducedMotion}
                label={model.state === 'recording' ? 'Live audio level' : 'Audio level'}
              />
            </div>
          )}
        </>
      )}

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

      <Controls
        model={model}
        onPause={pause}
        onResume={resume}
        onStop={() => void stop()}
        onDiscard={() => {
          // Read before the discard resets it: a take abandoned from inside a
          // note goes back to that note.
          const to = captureReturnPath(model.noteId);
          void discard().then(() => {
            leave(to);
          });
        }}
        onRerecord={() => void rerecord()}
        onRestart={() => void start(model.noteId)}
        onSend={handOff}
        onLeave={() => {
          leave(captureReturnPath(model.noteId));
        }}
      />
    </div>
  );
}

/**
 * Where the capture screen goes when it is done.
 *
 * A recording aimed at a note — "Record into this", `?note=`, or the chooser —
 * goes back to that note, on whichever tab the person left it. A sent one
 * used to be forced onto the Recordings tab so the upload could be watched
 * there, which took someone reading the text to a list of players to look at
 * a progress strip; the note's filing banner (`FilingBanner`) now shows that
 * progress on every tab, and the Recordings tab still lists the row. A
 * recording with no target is the library's to file, so the library is where
 * it goes.
 */
export function captureReturnPath(noteId: string | null): string {
  return noteId ? ROUTES.note(noteId) : ROUTES.home;
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
  onRerecord: () => void;
  /** Ask for the microphone again, into the same target. */
  onRestart: () => void;
  onSend: () => void;
  onLeave: () => void;
}

/**
 * Pause, cancel, stop and send are distinct controls — never one toggle.
 *
 * Round targets with a word under each. The screen is read at arm's length
 * while walking, and the one control that matters most has to be found
 * without reading: Send is the row's only filled shape. The words stay
 * visible because they are also the accessible names.
 */
function Controls({
  model,
  onPause,
  onResume,
  onStop,
  onDiscard,
  onRerecord,
  onRestart,
  onSend,
  onLeave,
}: ControlsProps) {
  if (model.state === 'requesting') {
    return (
      <div className="capture__controls">
        <Control icon="close" label="Cancel" onClick={onDiscard} />
      </div>
    );
  }

  if (model.state === 'recording' || model.state === 'paused') {
    const paused = model.state === 'paused';
    return (
      <div className="capture__controls">
        <Control icon="close" label="Cancel" onClick={onDiscard} />
        <Control
          icon={paused ? 'play' : 'pause'}
          label={paused ? 'Resume' : 'Pause'}
          onClick={paused ? onResume : onPause}
        />
        <Control icon="stop" label="Stop" onClick={onStop} />
        <Control icon="send" label="Send" primary onClick={onSend} />
      </div>
    );
  }

  if (model.state === 'review') {
    return (
      <div className="capture__controls">
        <Control icon="trash" label="Discard" onClick={onDiscard} />
        {/*
          Discard and start again into the same note, in one tap. The restore
          arrow — back around, once more — is the gesture.
        */}
        <Control icon="restore" label="Re-record" onClick={onRerecord} />
        <Control icon="send" label="Send" primary onClick={onSend} />
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
    const resend = canRetryUpload(model);
    /*
     * A refused or missing microphone is not the end of the road either: the
     * person allows it in the site settings, or plugs one in, and taps Try
     * again, which asks for the microphone as opening the screen did. Before
     * this the two device failures were marked unrecoverable and offered
     * nothing but Close.
     */
    const reopen =
      model.failure?.kind === 'permission-denied' || model.failure?.kind === 'no-microphone';
    return (
      <div className="capture__controls">
        <Control
          icon={resend ? 'trash' : 'close'}
          label={resend ? 'Discard' : 'Close'}
          onClick={onDiscard}
        />
        {resend && <Control icon="send" label="Try again" primary onClick={onSend} />}
        {reopen && <Control icon="mic" label="Try again" primary onClick={onRestart} />}
      </div>
    );
  }

  return (
    <div className="capture__controls">
      <Control icon="check" label="Done" onClick={onLeave} />
    </div>
  );
}

/**
 * What went wrong, at reading size, with the one thing that fixes it. The
 * machine's sentence is the app's own (never a browser's), so it is shown as
 * is; the permission case adds the step nobody guesses, because the browser
 * that refused offers no way back to the prompt from inside the page.
 */
function FailureCard({ failure }: { failure: CaptureFailure }) {
  return (
    <div className="capture__failure" role="alert">
      <Icon name="alert" size={28} className="capture__failure-icon" />
      <p className="capture__failure-message">{failure.message}</p>
      {failure.kind === 'permission-denied' && (
        <p className="capture__failure-hint">
          Allow the microphone in your browser&rsquo;s site settings, then try again.
        </p>
      )}
    </div>
  );
}

interface ControlProps {
  icon: IconName;
  label: string;
  /** The one filled shape in the row. */
  primary?: boolean;
  onClick: () => void;
}

function Control({ icon, label, primary = false, onClick }: ControlProps) {
  return (
    <button
      type="button"
      className={primary ? 'capture__control capture__control--primary' : 'capture__control'}
      onClick={onClick}
    >
      <span className="capture__control-disc">
        <Icon name={icon} size={26} />
      </span>
      <span className="capture__control-label">{label}</span>
    </button>
  );
}
