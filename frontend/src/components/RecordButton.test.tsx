import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { HOLD_ARM_MS, MIN_TALK_MS } from '@/features/capture/holdTiming.ts';
import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import {
  captureState,
  endHold,
  fake,
  mountBar,
  resetHold,
  speak,
  spoken,
  touch,
  wait,
  where,
} from '@/test/hold.tsx';

import { PATHS } from './Icon.tsx';

beforeEach(resetHold);
afterEach(endHold);

const disc = () => screen.getByRole('button', { name: /^(Record|Record into this note)$/ });

/** Holds the disc past the arm and long enough to send, with audio. */
async function holdAndSpeak(): Promise<HTMLElement> {
  const button = disc();
  fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
  await wait(HOLD_ARM_MS + 50);
  expect(captureState()).toBe('recording');
  await wait(MIN_TALK_MS + 100);
  speak();
  return button;
}

/**
 * The disc: a tap opens the recorder, a hold is push-to-talk (R8, F5). It
 * was tap-only from owner feedback 2026-09-27 to 2026-09-30, when the owner
 * asked for WhatsApp's hold on it and for the PTT screen to go.
 */
describe('the record button', () => {
  it('is named Record, wears the microphone, says how to hold, and opens the capture screen on a tap', async () => {
    mountBar();
    const record = screen.getByRole('button', { name: 'Record' });
    expect(record.querySelector('svg path')).toHaveAttribute('d', PATHS.mic);
    expect(record).toHaveTextContent('Record');
    expect(record).toHaveAttribute(
      'aria-description',
      'Hold to talk and release to send. While holding, slide up to lock or left to cancel.',
    );
    expect(record).toHaveAttribute('aria-keyshortcuts', 'R');
    fireEvent.pointerDown(record, { ...touch, clientX: 200, clientY: 800 });
    await wait(HOLD_ARM_MS - 100);
    fireEvent.pointerUp(record, touch);
    fireEvent.click(record);
    expect(where()).toBe('/capture');
    expect(captureState()).toBe('idle');
  });

  it('swallows the click a browser sends after a hold, so a sent hold does not also open /capture (QA B-1)', async () => {
    mountBar();
    const button = await holdAndSpeak();
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    expect(where()).toBe('/');
    await waitFor(() => {
      expect(captureState()).toBe('uploaded');
    });
    expect(fake.creates).toHaveLength(1);
    expect(spoken()).toHaveTextContent('Sent');
  });

  it('treats Enter as a tap', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    mountBar();
    disc().focus();
    await user.keyboard('{Enter}');
    expect(where()).toBe('/capture');
  });

  it('holds on Space and sends on its release, and a quick Space is a tap', async () => {
    mountBar();
    const button = disc();
    fireEvent.keyDown(button, { key: ' ' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
    // A held key repeats; the repeats are not new presses.
    fireEvent.keyDown(button, { key: ' ', repeat: true });
    await wait(MIN_TALK_MS + 100);
    speak();
    fireEvent.keyUp(button, { key: ' ' });
    await waitFor(() => {
      expect(captureState()).toBe('uploaded');
    });
    expect(where()).toBe('/');
    expect(fake.creates).toHaveLength(1);

    act(() => {
      useCaptureStore.getState().reset();
    });
    fireEvent.keyDown(button, { key: ' ' });
    await wait(50);
    fireEvent.keyUp(button, { key: ' ' });
    expect(where()).toBe('/capture');
  });

  it('cancels on Escape: nothing is sent and the recording is gone', async () => {
    mountBar();
    await holdAndSpeak();
    fireEvent.keyDown(document.body, { key: 'Escape' });
    await waitFor(() => {
      expect(captureState()).toBe('idle');
    });
    expect(spoken()).toHaveTextContent('Cancelled');
    fireEvent.pointerUp(disc(), touch);
    expect(fake.creates).toHaveLength(0);
  });

  it('says the last one is still sending rather than going dead under a hold', async () => {
    act(() => {
      useCaptureStore.setState({
        model: { ...INITIAL_CAPTURE, state: 'uploading', localId: 'busy', uploadProgress: 0.4 },
      });
    });
    mountBar();
    const button = disc();
    fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('uploading');
    expect(document.querySelector('.tab-bar__into')).toHaveTextContent('Still sending the last one…');
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    expect(where()).toBe('/');
  });

  it('stands down for a recording live elsewhere, and its release opens /capture to show it', async () => {
    act(() => {
      useCaptureStore.setState({
        model: { ...INITIAL_CAPTURE, state: 'recording', localId: 'other', startedAt: Date.now() },
      });
    });
    mountBar();
    const button = disc();
    fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
    await wait(HOLD_ARM_MS + 50);
    expect(useCaptureStore.getState().model.localId).toBe('other');
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    expect(where()).toBe('/capture');
  });

  it('sends a keyboard hold when the window loses focus, rather than discarding it', async () => {
    // Alt-Tab or a notification takes the keyup with it. `/talk` discarded
    // the recording here; the rule for an interruption is a release.
    mountBar();
    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
    await wait(MIN_TALK_MS + 100);
    speak();
    fireEvent.blur(window);
    await waitFor(() => {
      expect(captureState()).toBe('uploaded');
    });
    expect(fake.creates).toHaveLength(1);
    // And the next press is a new hold, not blocked by the one that never released.
    act(() => {
      useCaptureStore.getState().reset();
    });
    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
  });

  it('locks a first press whose finger lifts to answer the permission prompt, instead of "Too short"', async () => {
    // A fresh install: `getUserMedia` waits on the prompt, and the finger
    // lifts to answer it. That press used to end as "Too short" every time.
    let allow: () => void = () => {};
    fake.micGate = new Promise<void>((resolve) => {
      allow = resolve;
    });
    mountBar();
    const button = disc();
    fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('requesting');
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    // Locked: on to the capture screen with the take still waiting on the
    // prompt, not discarded as a slip.
    expect(where()).toBe('/capture');
    expect(captureState()).toBe('requesting');
    expect(spoken()).not.toHaveTextContent('Too short');

    // Allowed: the same take records, hands-free, for the capture screen.
    allow();
    await waitFor(() => {
      expect(captureState()).toBe('recording');
    });
    expect(fake.creates).toHaveLength(0);
  });
});
