import { useCallback, useEffect, useRef } from 'react';

import { BAR_HEIGHT_SCALE } from '@/features/capture/peaks.ts';

import { formatTime } from './artifacts.ts';

/**
 * The scrubbable waveform, drawn from `peaks.json`.
 *
 * It is a real slider, not a canvas with a click handler: `role="slider"` with
 * arrow-key seeking, so playback position is reachable without a pointer. The
 * canvas underneath is presentation only.
 */

export interface WaveformScrubberProps {
  peaks: readonly number[];
  currentTime: number;
  duration: number;
  onSeek: (seconds: number) => void;
  disabled?: boolean;
}

export function WaveformScrubber({
  peaks,
  currentTime,
  duration,
  onSeek,
  disabled = false,
}: WaveformScrubberProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const trackRef = useRef<HTMLDivElement>(null);
  const progress = duration > 0 ? Math.min(1, currentTime / duration) : 0;

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

    const barWidth = 2;
    const gap = 1;
    const slots = Math.max(1, Math.floor(width / (barWidth + gap)));
    const midpoint = height / 2;
    const playedUntil = progress * width;

    for (let slot = 0; slot < slots; slot += 1) {
      // Peaks are a fixed-resolution envelope, so they are resampled to
      // whatever width the element happens to have.
      const source = peaks.length > 0 ? peaks[Math.floor((slot / slots) * peaks.length)] ?? 0 : 0;
      const barHeight = Math.max(2, source * height * BAR_HEIGHT_SCALE);
      const x = slot * (barWidth + gap);
      context.fillStyle = x <= playedUntil ? played : unplayed;
      context.fillRect(x, midpoint - barHeight / 2, barWidth, barHeight);
    }
  }, [peaks, progress]);

  const seekFromPointer = useCallback(
    (clientX: number) => {
      const track = trackRef.current;
      if (!track || duration <= 0) return;
      const rect = track.getBoundingClientRect();
      if (rect.width === 0) return;
      const fraction = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
      onSeek(fraction * duration);
    },
    [duration, onSeek],
  );

  const step = useCallback(
    (delta: number) => {
      onSeek(Math.min(duration, Math.max(0, currentTime + delta)));
    },
    [currentTime, duration, onSeek],
  );

  return (
    <div className="scrubber">
      <div
        ref={trackRef}
        className="scrubber__track"
        role="slider"
        tabIndex={disabled ? -1 : 0}
        aria-label="Playback position"
        aria-valuemin={0}
        aria-valuemax={Math.round(duration)}
        aria-valuenow={Math.round(currentTime)}
        aria-valuetext={`${formatTime(currentTime)} of ${formatTime(duration)}`}
        aria-disabled={disabled}
        onPointerDown={(event) => {
          if (disabled) return;
          event.currentTarget.setPointerCapture(event.pointerId);
          seekFromPointer(event.clientX);
        }}
        onPointerMove={(event) => {
          if (disabled || !event.currentTarget.hasPointerCapture(event.pointerId)) return;
          seekFromPointer(event.clientX);
        }}
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
        <canvas ref={canvasRef} className="scrubber__canvas" aria-hidden="true" />
        <span
          className="scrubber__playhead"
          style={{ insetInlineStart: `${progress * 100}%` }}
          aria-hidden="true"
        />
      </div>

      <p className="scrubber__times">
        <span className="numeric">{formatTime(currentTime)}</span>
        <span className="numeric scrubber__total">{formatTime(duration)}</span>
      </p>
    </div>
  );
}
