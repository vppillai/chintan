import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { LONG_PRESS_MS, useLongPress } from './useLongPress.ts';

function Row({ onLongPress, onTap }: { onLongPress: () => void; onTap: () => void }) {
  const press = useLongPress(onLongPress, 50);
  return (
    <button
      type="button"
      {...press.handlers}
      onClick={() => {
        if (press.consumeClick()) return;
        onTap();
      }}
    >
      row
    </button>
  );
}

const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));

describe('useLongPress', () => {
  it('fires after the hold, swallows the click that follows, and ticks the haptics', async () => {
    const onLongPress = vi.fn();
    const onTap = vi.fn();
    const vibrate = vi.fn();
    Object.defineProperty(navigator, 'vibrate', { value: vibrate, configurable: true });
    render(<Row onLongPress={onLongPress} onTap={onTap} />);
    const row = screen.getByRole('button');

    fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 0, clientY: 0 });
    await wait(80);
    fireEvent.pointerUp(row, { pointerType: 'touch' });
    fireEvent.click(row);

    expect(onLongPress).toHaveBeenCalledTimes(1);
    expect(onTap).not.toHaveBeenCalled();
    expect(vibrate).toHaveBeenCalledWith(10);

    // The next, ordinary tap is a tap again.
    fireEvent.click(row);
    expect(onTap).toHaveBeenCalledTimes(1);
  });

  it('is cancelled by lifting early, by moving, and never fires for a mouse', async () => {
    const onLongPress = vi.fn();
    render(<Row onLongPress={onLongPress} onTap={() => {}} />);
    const row = screen.getByRole('button');

    fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 0, clientY: 0 });
    fireEvent.pointerUp(row, { pointerType: 'touch' });
    await wait(80);

    fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 0, clientY: 0 });
    fireEvent.pointerMove(row, { pointerType: 'touch', clientX: 0, clientY: 30 });
    await wait(80);

    fireEvent.pointerDown(row, { pointerType: 'mouse', clientX: 0, clientY: 0 });
    await wait(80);

    expect(onLongPress).not.toHaveBeenCalled();
  });

  it('suppresses the context menu Android raises for the same hold', async () => {
    // On Android the long press that selects a row is also the gesture that
    // opens the context menu and starts selecting text; the `contextmenu`
    // event is where both begin, so the hook cancels it while a press is
    // armed and once one has fired.
    const onLongPress = vi.fn();
    render(<Row onLongPress={onLongPress} onTap={() => {}} />);
    const row = screen.getByRole('button');

    fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 0, clientY: 0 });
    const armed = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    row.dispatchEvent(armed);
    expect(armed.defaultPrevented).toBe(true);

    await wait(80);
    const fired = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    row.dispatchEvent(fired);
    expect(fired.defaultPrevented).toBe(true);
    fireEvent.pointerUp(row, { pointerType: 'touch' });
    fireEvent.click(row);

    // With no press armed the browser's menu is the browser's business.
    const idle = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    row.dispatchEvent(idle);
    expect(idle.defaultPrevented).toBe(false);
  });

  it('holds for half a second by default', () => {
    expect(LONG_PRESS_MS).toBe(500);
  });
});
