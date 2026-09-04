import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { MAX_TRACK_PX, PX_PER_SECOND, ReviewPlayer, trackWidthFor } from './ReviewPlayer.tsx';

/*
 * jsdom has no object URLs and no media pipeline, so this covers what the
 * component decides for itself — the track's width, the slider's semantics,
 * how a seek is turned into a time — and leaves the drawing and the audio to
 * the e2e capture spec, which runs a real recorder.
 */

const ENVELOPE = Array.from({ length: 800 }, (_, index) => Math.abs(Math.sin(index / 20)));

function mount(durationMs: number, clip = async () => new Blob(['audio'])) {
  return render(<ReviewPlayer clip={clip} envelope={ENVELOPE} durationMs={durationMs} />);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('the waveform is wider than the screen, on purpose', () => {
  it('scales the track by the recording length, never narrower than its viewport', () => {
    expect(trackWidthFor(10, 300)).toBe(300);
    expect(trackWidthFor(60, 300)).toBe(60 * PX_PER_SECOND);
    // A twenty-minute dictation would be 28,800px; the canvas cannot be that
    // wide on a phone, so it compresses past the cap but still scrolls.
    expect(trackWidthFor(20 * 60, 300)).toBe(MAX_TRACK_PX);
  });

  it('lays the track out at that width inside a sideways-scrolling container', () => {
    mount(60_000);
    const track = screen.getByRole('slider', { name: 'Playback position' });
    expect(track.style.inlineSize).toBe(`${60 * PX_PER_SECOND}px`);
    expect(track.parentElement).toHaveClass('clip-scrubber');
  });
});

describe('the slider', () => {
  it('reports the recording length from the machine clock, not the media element', () => {
    mount(125_000);
    const slider = screen.getByRole('slider', { name: 'Playback position' });
    expect(slider).toHaveAttribute('aria-valuemax', '125');
    expect(slider).toHaveAttribute('aria-valuenow', '0');
    expect(slider).toHaveAttribute('aria-valuetext', '0:00 of 2:05');
    expect(screen.getByText('2:05')).toBeInTheDocument();
  });

  it('says plainly when there is nothing to play here', async () => {
    // No object URLs in this environment: the waveform and the clock still
    // show, and the play control is disabled rather than broken.
    mount(8_000);
    expect(await screen.findByText(/playback is not available here/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /play recording/i })).toBeDisabled();
  });

  it('seeks from a tap on the track, in proportion to the recording length', async () => {
    const createObjectURL = vi.fn(() => 'blob:review');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));

    const view = mount(100_000);
    await screen.findByRole('button', { name: /play recording/i });
    const audio = document.querySelector('audio');
    expect(audio).not.toBeNull();

    const track = screen.getByRole('slider', { name: 'Playback position' });
    const scroller = track.parentElement as HTMLElement;
    vi.spyOn(scroller, 'getBoundingClientRect').mockReturnValue({
      left: 0,
      top: 0,
      width: 300,
      height: 56,
      right: 300,
      bottom: 56,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });

    // The track is 2400px wide for 100 s; a press a quarter of the way along
    // it is a quarter of the way through the recording.
    act(() => {
      fireEvent.pointerDown(track, { clientX: 600, pointerType: 'mouse', pointerId: 1 });
      fireEvent.pointerUp(track, { clientX: 600, pointerType: 'mouse', pointerId: 1 });
    });
    expect(audio!.currentTime).toBe(25);

    // Revoked on the way out, so a review does not pin the audio in memory.
    view.unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:review');
  });

  it('does not seek on a press that turned into a drag', async () => {
    const createObjectURL = vi.fn(() => 'blob:review');
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    mount(100_000);
    await screen.findByRole('button', { name: /play recording/i });
    const audio = document.querySelector('audio')!;
    const track = screen.getByRole('slider', { name: 'Playback position' });

    act(() => {
      fireEvent.pointerDown(track, { clientX: 100, pointerType: 'mouse', pointerId: 1 });
      fireEvent.pointerMove(track, { clientX: 160, pointerType: 'mouse', pointerId: 1 });
      fireEvent.pointerUp(track, { clientX: 160, pointerType: 'mouse', pointerId: 1 });
    });
    expect(audio.currentTime).toBe(0);
  });

  it('steps with the keyboard: five seconds on an arrow, thirty on Page', async () => {
    const createObjectURL = vi.fn(() => 'blob:review');
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    mount(100_000);
    await screen.findByRole('button', { name: /play recording/i });
    const audio = document.querySelector('audio')!;
    const track = screen.getByRole('slider', { name: 'Playback position' });

    fireEvent.keyDown(track, { key: 'ArrowRight' });
    expect(audio.currentTime).toBe(5);
    fireEvent.keyDown(track, { key: 'PageUp' });
    expect(audio.currentTime).toBe(35);
    fireEvent.keyDown(track, { key: 'End' });
    expect(audio.currentTime).toBe(100);
    fireEvent.keyDown(track, { key: 'Home' });
    expect(audio.currentTime).toBe(0);
  });
});
