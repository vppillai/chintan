import { useEffect, useRef } from 'react';

/**
 * The live waveform.
 *
 * Real amplitudes from the `AnalyserNode`, drawn to canvas on an animation
 * frame. It is deliberately not a progress bar: a determinate bar pinned at
 * 100% with pulsing opacity is indistinguishable from a stuck upload.
 *
 * This draws only what the microphone is actually hearing, so silence looks
 * like silence — which is the one piece of feedback that tells a user their
 * microphone is muted before they have talked for ten minutes.
 */

const BAR_WIDTH = 3;
const BAR_GAP = 2;

export interface WaveformProps {
  /** Pulled per frame rather than passed as state: 60 renders/sec is absurd. */
  read: (count: number) => number[];
  active: boolean;
  /** Honoured by drawing a static bar chart rather than animating. */
  reducedMotion?: boolean;
  label?: string;
}

export function Waveform({ read, active, reducedMotion = false, label }: WaveformProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const context = canvas.getContext('2d');
    if (!context) return;

    let frame = 0;
    let cancelled = false;

    const draw = (): void => {
      if (cancelled) return;

      const ratio = window.devicePixelRatio || 1;
      const width = canvas.clientWidth;
      const height = canvas.clientHeight;
      if (canvas.width !== width * ratio || canvas.height !== height * ratio) {
        canvas.width = width * ratio;
        canvas.height = height * ratio;
      }
      context.setTransform(ratio, 0, 0, ratio, 0, 0);
      context.clearRect(0, 0, width, height);

      const slots = Math.max(1, Math.floor(width / (BAR_WIDTH + BAR_GAP)));
      const amplitudes = read(slots);

      // The colour comes from the stylesheet — `.waveform { color: … }` — so
      // the waveform follows the theme and no literal colour exists outside
      // tokens.css. There is deliberately no fallback: an empty computed colour
      // means the stylesheet did not load, and painting a guessed hue over a
      // themed ground would look worse than painting nothing.
      const colour = getComputedStyle(canvas).getPropertyValue('color');
      if (!colour) return;
      context.fillStyle = colour;

      const midpoint = height / 2;
      for (let index = 0; index < amplitudes.length; index += 1) {
        const amplitude = amplitudes[index] ?? 0;
        // A floor so a live-but-quiet microphone still shows a line rather
        // than reading as "not recording".
        const barHeight = Math.max(2, amplitude * height * 0.9);
        const x = width - (amplitudes.length - index) * (BAR_WIDTH + BAR_GAP);
        context.fillRect(x, midpoint - barHeight / 2, BAR_WIDTH, barHeight);
      }

      if (active && !reducedMotion) {
        frame = requestAnimationFrame(draw);
      }
    };

    draw();
    // Under reduced motion the canvas is repainted on a slow interval instead
    // of every frame: still truthful, no animation.
    const interval = reducedMotion && active ? setInterval(draw, 500) : null;

    return () => {
      cancelled = true;
      if (frame) cancelAnimationFrame(frame);
      if (interval) clearInterval(interval);
    };
  }, [read, active, reducedMotion]);

  return (
    <canvas
      ref={canvasRef}
      className="waveform"
      role="img"
      aria-label={label ?? (active ? 'Live audio level' : 'Audio level')}
    />
  );
}
