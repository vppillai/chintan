import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react';

import { Icon } from '@/components/Icon.tsx';
import { useReducedMotion } from '@/hooks/useReducedMotion.ts';
import { formatTime } from '@/features/notes/artifacts.ts';
import { usePlayer } from '@/features/notes/usePlayer.ts';

import { BAR_HEIGHT_SCALE } from './peaks.ts';

/**
 * Listening back before Send.
 *
 * The recording is on disk in chunks the moment it is made, so there is
 * nothing to fetch: the chunks are reassembled into one Blob, given an object
 * URL, and played by an ordinary `<audio>` element. The waveform is the live
 * envelope the recorder already collected for `peaks.json` — the same bars the
 * note screen will draw once it is filed, drawn now.
 *
 * The waveform is wider than the screen for anything longer than a few
 * seconds, and scrolls sideways: a twenty-minute dictation squeezed into
 * three hundred pixels is a stripe, not a waveform, and the one thing a person
 * wants from this screen is to find the place where they stumbled and hear
 * whether it is worth re-recording. A tap seeks; a drag scrolls; the playhead
 * is kept in view while playing.
 */

/** Horizontal scale of the scrollable waveform. */
export const PX_PER_SECOND = 24;
/**
 * A canvas wider than this in device pixels fails to allocate on some phones,
 * so the scale compresses past it. At this cap a twenty-minute recording still
 * scrolls through more than twenty screens' worth of waveform.
 */
export const MAX_TRACK_PX = 8_000;

const BAR_WIDTH = 2;
const BAR_GAP = 1;
/** Further than this and a press is a drag, not a tap. */
const DRAG_SLOP_PX = 6;

export interface ReviewPlayerProps {
  /** Reassembles the recording. Called once per mount. */
  clip: () => Promise<Blob>;
  /** The recorder's amplitude envelope, 0..1 per bucket. */
  envelope: readonly number[];
  /**
   * The recording's length from the machine's clock. The `<audio>` element
   * cannot be trusted for this: a WebM straight out of MediaRecorder has no
   * duration in its header and reports `Infinity` until it has been played
   * through once.
   */
  durationMs: number;
}

export function ReviewPlayer({ clip, envelope, durationMs }: ReviewPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [src, setSrc] = useState<string | null>(null);
  // An environment without object URLs has no way to play a Blob. The
  // waveform and the clock are still shown; only playback is missing.
  const [unplayable, setUnplayable] = useState(() => typeof URL.createObjectURL !== 'function');
  const reducedMotion = useReducedMotion();

  /*
   * One object URL per mount, revoked on the way out. A revoked URL frees the
   * Blob; forgetting to do that across a few dozen review screens is how a
   * capture app quietly holds the day's audio in memory.
   */
  useEffect(() => {
    let url: string | null = null;
    let cancelled = false;
    if (typeof URL.createObjectURL !== 'function') return;
    void clip()
      .then((blob) => {
        if (cancelled) return;
        if (blob.size === 0) {
          setUnplayable(true);
          return;
        }
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch(() => {
        if (!cancelled) setUnplayable(true);
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [clip]);

  const player = usePlayer(src, audioRef, src !== null);
  const duration = durationMs / 1000;
  // The element's own position, bounded by the clock's length so a WebM with
  // no header duration cannot report past the end.
  const currentTime = Math.min(duration, player.currentTime);

  return (
    <div className="review-player" data-testid="review-player">
      {src && <audio ref={audioRef} src={src} preload="auto" crossOrigin="anonymous" />}

      <div className="review-player__row">
        <button
          type="button"
          className="review-player__play"
          aria-label={player.playing ? 'Pause recording' : 'Play recording'}
          disabled={src === null}
          onClick={player.toggle}
        >
          <Icon name={player.playing ? 'stop' : 'play'} size={18} />
        </button>

        <ClipScrubber
          envelope={envelope}
          duration={duration}
          currentTime={currentTime}
          playing={player.playing}
          reducedMotion={reducedMotion}
          onSeek={player.seek}
          disabled={src === null}
        />
      </div>

      <p className="review-player__times">
        <span className="numeric">{formatTime(currentTime)}</span>
        {unplayable ? (
          <span className="review-player__note">Playback is not available here</span>
        ) : player.error ? (
          <span className="review-player__note" role="alert">
            {player.error}
          </span>
        ) : null}
        <span className="numeric">{formatTime(duration)}</span>
      </p>
    </div>
  );
}

interface ClipScrubberProps {
  envelope: readonly number[];
  duration: number;
  currentTime: number;
  playing: boolean;
  reducedMotion: boolean;
  onSeek: (seconds: number) => void;
  disabled?: boolean;
}

/** The track's width in CSS pixels, given the room it has. */
export function trackWidthFor(duration: number, viewportWidth: number): number {
  return Math.max(viewportWidth, Math.min(Math.round(duration * PX_PER_SECOND), MAX_TRACK_PX));
}

/**
 * A scrollable waveform that is also a slider.
 *
 * `role="slider"` with arrow-key seeking, as the note screen's scrubber has,
 * so the position is reachable without a pointer. The scroll container around
 * it is native: touch pans it (`touch-action`), and a mouse drag is turned
 * into a scroll here because the browser will not do that on its own. A press
 * that does not move is a seek.
 */
function ClipScrubber({
  envelope,
  duration,
  currentTime,
  playing,
  reducedMotion,
  onSeek,
  disabled = false,
}: ClipScrubberProps) {
  const scrollerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [viewportWidth, setViewportWidth] = useState(0);
  const drag = useRef<{ startX: number; startScroll: number; moved: boolean } | null>(null);

  const progress = duration > 0 ? Math.min(1, Math.max(0, currentTime / duration)) : 0;
  const trackWidth = trackWidthFor(duration, viewportWidth);

  // The track is as wide as the recording, never narrower than its viewport.
  useLayoutEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller) return;
    setViewportWidth(scroller.clientWidth);
    if (typeof ResizeObserver !== 'function') return;
    const observer = new ResizeObserver(() => {
      setViewportWidth(scroller.clientWidth);
    });
    observer.observe(scroller);
    return () => {
      observer.disconnect();
    };
  }, []);

  useEffect(() => {
    const canvas = canvasRef.current;
    const context = canvas?.getContext('2d');
    if (!canvas || !context) return;

    const ratio = window.devicePixelRatio || 1;
    const width = canvas.clientWidth;
    const height = canvas.clientHeight;
    if (width === 0 || height === 0) return;

    canvas.width = width * ratio;
    canvas.height = height * ratio;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, width, height);

    const styles = getComputedStyle(canvas);
    // Both colours come from the stylesheet, so the waveform is themed and no
    // hue is named outside tokens.css.
    const played = styles.getPropertyValue('--waveform-played').trim();
    const unplayed = styles.getPropertyValue('--waveform-unplayed').trim();
    if (!played || !unplayed) return;

    const slots = Math.max(1, Math.floor(width / (BAR_WIDTH + BAR_GAP)));
    const midpoint = height / 2;
    const playedUntil = progress * width;

    for (let slot = 0; slot < slots; slot += 1) {
      const source =
        envelope.length > 0 ? (envelope[Math.floor((slot / slots) * envelope.length)] ?? 0) : 0;
      // A floor so silence still draws a hairline rather than a gap.
      const barHeight = Math.max(2, source * height * BAR_HEIGHT_SCALE);
      const x = slot * (BAR_WIDTH + BAR_GAP);
      context.fillStyle = x <= playedUntil ? played : unplayed;
      context.fillRect(x, midpoint - barHeight / 2, BAR_WIDTH, barHeight);
    }
  }, [envelope, progress, trackWidth]);

  /*
   * Keep the playhead on screen while playing — but not while the user has
   * hold of the track, and never with an animation under reduced motion.
   */
  useEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller || !playing || drag.current || typeof scroller.scrollTo !== 'function') return;
    const x = progress * trackWidth;
    const margin = 24;
    const left = scroller.scrollLeft;
    const right = left + scroller.clientWidth;
    if (x >= left + margin && x <= right - margin) return;
    scroller.scrollTo({
      left: Math.max(0, x - scroller.clientWidth / 2),
      behavior: reducedMotion ? 'auto' : 'smooth',
    });
  }, [progress, playing, trackWidth, reducedMotion]);

  const seekAt = useCallback(
    (clientX: number) => {
      const scroller = scrollerRef.current;
      if (!scroller || duration <= 0 || trackWidth === 0) return;
      const rect = scroller.getBoundingClientRect();
      const x = clientX - rect.left + scroller.scrollLeft;
      onSeek(Math.min(duration, Math.max(0, (x / trackWidth) * duration)));
    },
    [duration, trackWidth, onSeek],
  );

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return;
    const scroller = scrollerRef.current;
    if (!scroller) return;
    drag.current = { startX: event.clientX, startScroll: scroller.scrollLeft, moved: false };
    // Only a mouse needs the drag-to-scroll emulation; a touch pans the
    // scroller natively, and capturing it would take that away.
    if (event.pointerType === 'mouse' && typeof event.currentTarget.setPointerCapture === 'function') {
      event.currentTarget.setPointerCapture(event.pointerId);
    }
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const state = drag.current;
    const scroller = scrollerRef.current;
    if (!state || !scroller || event.pointerType !== 'mouse') return;
    const delta = event.clientX - state.startX;
    if (!state.moved && Math.abs(delta) < DRAG_SLOP_PX) return;
    state.moved = true;
    scroller.scrollLeft = state.startScroll - delta;
  };

  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    const state = drag.current;
    drag.current = null;
    if (!state || disabled) return;
    if (!state.moved) seekAt(event.clientX);
  };

  const onPointerCancel = () => {
    // The browser took the gesture for a native scroll. Not a seek.
    drag.current = null;
  };

  const step = (delta: number) => {
    onSeek(Math.min(duration, Math.max(0, currentTime + delta)));
  };

  return (
    <div
      ref={scrollerRef}
      className="clip-scrubber"
      data-testid="clip-scrubber"
    >
      <div
        className="clip-scrubber__track"
        style={{ inlineSize: `${trackWidth}px` }}
        role="slider"
        tabIndex={disabled ? -1 : 0}
        aria-label="Playback position"
        aria-valuemin={0}
        aria-valuemax={Math.round(duration)}
        aria-valuenow={Math.round(currentTime)}
        aria-valuetext={`${formatTime(currentTime)} of ${formatTime(duration)}`}
        aria-disabled={disabled}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onKeyDown={(event) => {
          if (disabled) return;
          // Five-second arrows, thirty on Page, because a scrubber stepped in
          // pixels is unusable for a twenty-minute recording.
          const map: Record<string, number> = {
            ArrowRight: 5,
            ArrowLeft: -5,
            ArrowUp: 5,
            ArrowDown: -5,
            PageUp: 30,
            PageDown: -30,
          };
          if (event.key === 'Home') {
            event.preventDefault();
            onSeek(0);
            return;
          }
          if (event.key === 'End') {
            event.preventDefault();
            onSeek(duration);
            return;
          }
          const delta = map[event.key];
          if (delta === undefined) return;
          event.preventDefault();
          step(delta);
        }}
      >
        <canvas ref={canvasRef} className="clip-scrubber__canvas" aria-hidden="true" />
        <span
          className="clip-scrubber__playhead"
          style={{ insetInlineStart: `${progress * 100}%` }}
          aria-hidden="true"
        />
      </div>
    </div>
  );
}
