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

  it('holds for half a second by default', () => {
    expect(LONG_PRESS_MS).toBe(500);
  });
});
