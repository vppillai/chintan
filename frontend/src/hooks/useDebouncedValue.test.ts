import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useDebouncedValue } from './useDebouncedValue.ts';

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('useDebouncedValue', () => {
  it('starts with the value it is given', () => {
    const view = renderHook(() => useDebouncedValue('roof', 250));
    expect(view.result.current).toBe('roof');
  });

  it('follows a run of changes once, after the pause', () => {
    const view = renderHook(({ value }: { value: string }) => useDebouncedValue(value, 250), {
      initialProps: { value: '' },
    });

    for (const typed of ['f', 'fl', 'fla', 'flas', 'flash']) {
      view.rerender({ value: typed });
      act(() => {
        vi.advanceTimersByTime(60);
      });
      // Still typing: nothing has settled.
      expect(view.result.current).toBe('');
    }

    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(view.result.current).toBe('flash');
  });

  it('drops a value that was replaced before it settled', () => {
    const view = renderHook(({ value }: { value: string }) => useDebouncedValue(value, 250), {
      initialProps: { value: 'roof' },
    });
    view.rerender({ value: 'roofing' });
    act(() => {
      vi.advanceTimersByTime(200);
    });
    view.rerender({ value: 'roof' });
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(view.result.current).toBe('roof');
  });
});
