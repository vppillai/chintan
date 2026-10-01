import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { captureState, endHold, fake, mountBar, resetHold, speak, spoken, touch, wait, where } from '@/test/hold.tsx';

import { CLICK_SUPPRESS_MS, HOLD_ARM_MS, MIN_TALK_MS } from './holdTiming.ts';

/**
 * The hook's browser wiring — what turns an interruption into a reducer
 * event — through the real capture store. `holdGesture.test.ts` proves what
 * a release does; these prove which browser events are one (review
 * 2026-10-01, FE-5), the branches R8-F6 changed and nothing covered.
 */

beforeEach(resetHold);
afterEach(endHold);

const disc = () => screen.getByRole('button', { name: /^Record/ });
const bar = () => screen.getByRole('navigation', { name: 'Main' });

/** A hold with something said in it, so a release has a message to send. */
async function hold(): Promise<HTMLElement> {
  mountBar('/');
  const button = disc();
  fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
  await wait(HOLD_ARM_MS + 50);
  expect(captureState()).toBe('recording');
  await wait(MIN_TALK_MS + 100);
  speak();
  return button;
}

async function expectSent(): Promise<void> {
  await waitFor(() => {
    expect(captureState()).toBe('uploaded');
  });
  expect(fake.creates).toHaveLength(1);
  expect(bar()).not.toHaveAttribute('data-hold');
  expect(spoken()).toHaveTextContent('Sent');
}

async function expectStillHeld(): Promise<void> {
  await wait(50);
  expect(captureState()).toBe('recording');
  expect(bar()).toHaveAttribute('data-hold', 'holding');
}

describe('interruptions are releases, never discards', () => {
  it('pointercancel for the held pointer sends what was said; for another pointer it is nothing', async () => {
    const button = await hold();
    fireEvent.pointerCancel(button, { ...touch, pointerId: 2 });
    await expectStillHeld();
    fireEvent.pointerCancel(button, touch);
    await expectSent();
  });

  it('lostpointercapture on the disc itself releases; bubbling up from a child it does not', async () => {
    const button = await hold();
    // A child's capture is not ours: the glyph losing its says nothing about the disc.
    fireEvent.lostPointerCapture(button.querySelector('svg')!, touch);
    await expectStillHeld();
    fireEvent.lostPointerCapture(button, touch);
    await expectSent();
  });

  it('the window losing focus leaves a pointer hold alone — the permission prompt takes focus — but releases a key hold', async () => {
    await hold();
    fireEvent.blur(window);
    await expectStillHeld();
    fireEvent.pointerUp(disc(), touch);
    await expectSent();

    resetHold();
    fake.creates.length = 0;
    fireEvent.keyDown(document.body, { key: 'r' });
    await wait(HOLD_ARM_MS + 50);
    expect(captureState()).toBe('recording');
    await wait(MIN_TALK_MS + 100);
    speak();
    fireEvent.blur(window);
    await expectSent();
  });

  it('a hidden page releases the hold', async () => {
    await hold();
    const visibility = Object.getOwnPropertyDescriptor(Document.prototype, 'visibilityState');
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    try {
      fireEvent(document, new Event('visibilitychange'));
      await expectSent();
    } finally {
      Reflect.deleteProperty(document, 'visibilityState');
      if (visibility) Object.defineProperty(Document.prototype, 'visibilityState', visibility);
    }
  });

  it('a second finger is ignored, and lifting it does not end the hold', async () => {
    const button = await hold();
    fireEvent.pointerDown(button, { ...touch, pointerId: 2, clientX: 240, clientY: 800 });
    fireEvent.pointerUp(button, { ...touch, pointerId: 2 });
    await expectStillHeld();
    // The first finger's lift still ends it.
    fireEvent.pointerUp(button, touch);
    await expectSent();
  });
});

describe('the click a long press leaves behind', () => {
  it('is swallowed for CLICK_SUPPRESS_MS, and only until the next press', async () => {
    const button = await hold();
    fireEvent.pointerUp(button, touch);
    await expectSent();

    // Chromium's click after the long press (`detail` 1): not a tap.
    fireEvent.click(button, { detail: 1 });
    expect(where()).toBe('/');

    // A fresh tap inside the window is the user's own, and opens the screen.
    fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 800 });
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    expect(where()).toBe('/capture');
  });

  it('is not swallowed once the window has passed', async () => {
    const button = await hold();
    fireEvent.pointerUp(button, touch);
    await expectSent();
    vi.advanceTimersByTime(CLICK_SUPPRESS_MS + 1);
    fireEvent.click(button);
    expect(where()).toBe('/capture');
  });
});
