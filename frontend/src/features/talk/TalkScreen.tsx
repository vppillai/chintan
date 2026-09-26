import { useCallback, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router';

import { Icon } from '@/components/Icon.tsx';
import { LocalUploadItem, useLocalUpload } from '@/features/capture/FilingRow.tsx';
import { TargetChooser } from '@/features/capture/TargetChooser.tsx';
import { Waveform } from '@/features/capture/Waveform.tsx';
import { formatElapsed, type CaptureModel } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { useHoldToTalk, type HoldPhase } from '@/features/capture/useHoldToTalk.ts';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';

/**
 * `/talk`: one giant hold button, for adding thoughts one after another.
 *
 * The capture screen is a round trip per recording. This screen is a
 * walkie-talkie: hold, speak, release, and it is on its way while the button
 * is already ready for the next one — the manifest's "PTT" shortcut lands
 * here. PTT is the name (owner feedback 2026-09-26); the button's accessible
 * name spells the gesture out for a screen reader. Where each recording goes
 * is the pill above the button, and `?note=` seeds it as it does the capture
 * screen's. On a keyboard the Space bar is the button.
 *
 * What it shows is the store's truth, as everywhere: the level and the clock
 * only while the microphone is live, "Sending…" after a release until the
 * server has the audio and then "Sent · filing" while the machine holds the
 * landed upload, and — because nothing else on this screen would — the
 * upload's own row while it is still leaving this device or when it failed,
 * with the row's Retry and Discard. The library's filing row and the note's
 * banner pick it up from there.
 */
export function TalkScreen() {
  const [params] = useSearchParams();
  const [target, setTarget] = useState<string | null>(params.get('note'));
  const hold = useHoldToTalk({ noteId: target, holdDelayMs: 0 });
  const model = useCaptureStore((state) => state.model);
  const amplitudes = useCaptureStore((state) => state.amplitudes);
  const reducedMotion = useReducedMotion();
  const read = useCallback((count: number) => amplitudes(count), [amplitudes]);
  const buttonRef = useRef<HTMLButtonElement>(null);

  /*
   * The upload after a release, for its effects as much as its row: it asks
   * for the filing row's list and the target note the moment the PUT lands,
   * and releases the machine once nothing on this screen can show more. The
   * row is drawn only while there is something to do or watch — sending, or
   * failed — not for the seconds a landed upload waits for a server row this
   * screen never lists.
   */
  const upload = useLocalUpload([]);
  const uploadRow = upload && upload.state !== 'uploaded' ? upload : null;

  const holding = hold.phase === 'holding';
  const live = model.state === 'recording' || model.state === 'paused';

  /*
   * Space, held, is the button — from the page, not only from the button
   * with focus, so the screen works the moment it opens. A field, the pill's
   * open list or any other control keeps its own Space; Escape mid-hold
   * cancels as sliding away does. The keydown's default is stopped so no
   * click follows the release and the page does not scroll under it. The
   * window losing focus cancels too, as `pointercancel` does for a finger:
   * Alt-Tab, the lock screen or a notification takes the keyup with it, and
   * without this the microphone stayed open until the cap.
   */
  const { press, release, cancel } = hold;
  useEffect(() => {
    const ownsKey = (event: KeyboardEvent): boolean => {
      const at = event.target;
      if (!(at instanceof HTMLElement)) return true;
      return at === document.body || at.id === 'main' || at === buttonRef.current;
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape' && holding) {
        cancel();
        return;
      }
      if (event.key !== ' ' || event.repeat || !ownsKey(event)) return;
      event.preventDefault();
      press();
    };
    const onKeyUp = (event: KeyboardEvent): void => {
      if (event.key !== ' ' || !ownsKey(event)) return;
      event.preventDefault();
      release();
    };
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('keyup', onKeyUp);
    window.addEventListener('blur', cancel);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('keyup', onKeyUp);
      window.removeEventListener('blur', cancel);
    };
  }, [press, release, cancel, holding]);

  const label = buttonLabel(hold.phase, hold.away, model);

  // A refused microphone, and the other failures the upload row does not carry.
  const failure =
    model.state === 'failed' && model.failure && !uploadRow ? model.failure.message : null;

  return (
    <div className="talk" data-phase={hold.phase}>
      <h1 className="visually-hidden">PTT</h1>

      <TargetChooser noteId={target} onChoose={setTarget} disabled={holding} />

      {/* The level and the clock keep their room when idle, so the button does not jump. */}
      <div className="talk__level" aria-hidden="true">
        {holding && (
          <Waveform
            key={model.localId}
            read={read}
            active={model.state === 'recording'}
            reducedMotion={reducedMotion}
          />
        )}
      </div>
      <p
        className="talk__timer numeric"
        data-live={live || undefined}
        aria-label={holding ? `Elapsed ${formatElapsed(model.elapsedMs)}` : undefined}
      >
        {holding ? formatElapsed(model.elapsedMs) : ''}
      </p>

      <button
        ref={buttonRef}
        type="button"
        className="talk__button"
        data-holding={holding || undefined}
        data-away={hold.away || undefined}
        aria-label={holding ? label : 'PTT: hold to talk, release to send'}
        {...hold.handlers}
        onClick={() => {
          // A press is a hold from the first frame; the click is its echo.
          hold.consumeClick();
        }}
      >
        <Icon name="mic" size={56} strokeWidth={2.5} />
        <span className="talk__label">{label}</span>
      </button>

      <p className="talk__status" role="status" aria-live="polite">
        {statusLine(hold.phase, model)}
      </p>

      {failure && (
        <p className="talk__failure" role="alert">
          {failure}
        </p>
      )}

      {uploadRow && <LocalUploadItem model={uploadRow} />}

      <p className="talk__hint">
        Hold, speak, release to send. Slide away to cancel.
        <span className="talk__keys"> On a keyboard, hold Space.</span>
      </p>
    </div>
  );
}

function buttonLabel(phase: HoldPhase, away: boolean, model: CaptureModel): string {
  if (phase !== 'holding') return 'PTT';
  if (away) return 'Release to cancel';
  if (model.state === 'requesting') return 'Starting the microphone…';
  return 'Release to send';
}

/**
 * The line under the button. The hold's own notices first; after that the
 * store, not the hold's "sent" phase: that phase rests after `HOLD_NOTICE_MS`,
 * and a real clip on a phone link is still uploading then, so gated on it
 * the line went blank over a row still reading "Uploading… N%" and never said
 * Sent. "Sent" only once the server has the audio — a status already saying
 * so above that row contradicted it — and it stays while the machine holds
 * the landed upload: until the next hold resets it or `useLocalUpload` lets
 * it go, since on this screen no server row arrives to take it earlier. A
 * failed upload is the row's and the failure line's to explain. Nothing
 * while holding: a take that settled under the finger — the track ended —
 * is still the hold's until release.
 */
function statusLine(phase: HoldPhase, model: CaptureModel): string {
  if (phase === 'busy') return 'Still sending the last one…';
  if (phase === 'hint') return 'Too short — hold to talk';
  if (phase === 'holding') return '';
  if (model.state === 'stopping' || model.state === 'review' || model.state === 'uploading') {
    return 'Sending…';
  }
  return model.state === 'uploaded' ? 'Sent · filing' : '';
}
