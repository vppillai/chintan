import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { KEYBOARD_ATTRIBUTE, KEYBOARD_INSET_PROPERTY, useKeyboardInset } from './useKeyboardInset.ts';

/**
 * jsdom has no `visualViewport`; this stands one in — an EventTarget with the
 * three numbers the hook reads — under a 915 px window (a Pixel 7's layout
 * viewport). Each case installs its own and takes it down again.
 */
class FakeViewport extends EventTarget {
  height = 915;
  offsetTop = 0;
  scale = 1;
}

function install(viewport: FakeViewport | undefined): void {
  Object.defineProperty(window, 'visualViewport', { value: viewport, configurable: true });
  Object.defineProperty(window, 'innerHeight', { value: 915, configurable: true });
}

const inset = (): string => document.documentElement.style.getPropertyValue(KEYBOARD_INSET_PROPERTY);

afterEach(() => {
  install(undefined);
  document.documentElement.style.removeProperty(KEYBOARD_INSET_PROPERTY);
  document.documentElement.removeAttribute(KEYBOARD_ATTRIBUTE);
});

const keyboardUp = (): boolean => document.documentElement.hasAttribute(KEYBOARD_ATTRIBUTE);

describe('useKeyboardInset', () => {
  it('writes the part of the window below the visual viewport, and follows it', () => {
    const viewport = new FakeViewport();
    install(viewport);
    renderHook(() => useKeyboardInset());
    expect(inset()).toBe('0px');

    act(() => {
      viewport.height = 515;
      viewport.dispatchEvent(new Event('resize'));
    });
    expect(inset()).toBe('400px');

    // The browser pans the page up under the keyboard: less of it is hidden.
    act(() => {
      viewport.offsetTop = 100;
      viewport.dispatchEvent(new Event('scroll'));
    });
    expect(inset()).toBe('300px');
  });

  it('reads a zoomed viewport as no keyboard', () => {
    const viewport = new FakeViewport();
    viewport.height = 457;
    viewport.scale = 2;
    install(viewport);
    renderHook(() => useKeyboardInset());
    expect(inset()).toBe('0px');
  });

  it('takes the property with it on unmount', () => {
    const viewport = new FakeViewport();
    viewport.height = 515;
    install(viewport);
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(inset()).toBe('400px');
    unmount();
    expect(inset()).toBe('');
  });

  it('marks the keyboard up past 80 px only, and takes the mark with it on unmount', () => {
    const viewport = new FakeViewport();
    install(viewport);
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(keyboardUp()).toBe(false);

    act(() => {
      viewport.height = 915 - 81;
      viewport.dispatchEvent(new Event('resize'));
    });
    expect(keyboardUp()).toBe(true);

    // An accessory bar or a zoom rounding, not a keyboard.
    act(() => {
      viewport.height = 915 - 80;
      viewport.dispatchEvent(new Event('resize'));
    });
    expect(keyboardUp()).toBe(false);

    act(() => {
      viewport.height = 515;
      viewport.dispatchEvent(new Event('resize'));
    });
    expect(keyboardUp()).toBe(true);
    unmount();
    expect(keyboardUp()).toBe(false);
  });

  it('writes nothing where there is no visual viewport to read', () => {
    install(undefined);
    renderHook(() => useKeyboardInset());
    expect(inset()).toBe('');
  });
});
