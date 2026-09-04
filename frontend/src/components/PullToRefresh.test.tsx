import { act, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { PULL_DAMPING, PULL_THRESHOLD_PX, pullLabel } from '@/hooks/usePullToRefresh.ts';

import { PullToRefresh } from './PullToRefresh.tsx';

/*
 * jsdom has TouchEvent but no Touch constructor, so the touches are plain
 * objects on a plain event. The hook reads `clientY` and nothing else.
 */
function touch(type: string, clientY: number): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, 'touches', { value: type === 'touchend' ? [] : [{ clientY }] });
  Object.defineProperty(event, 'changedTouches', { value: [{ clientY }] });
  return event;
}

function mount(onRefresh: () => Promise<unknown>) {
  const view = render(
    <main className="app__main">
      <div className="screen">
        <PullToRefresh onRefresh={onRefresh} />
        <p>a list</p>
      </div>
    </main>,
  );
  const container = view.container.querySelector('.app__main') as HTMLElement;
  const indicator = () => view.container.querySelector('.pull-refresh') as HTMLElement;
  const drag = (from: number, to: number, release = true) => {
    act(() => {
      container.dispatchEvent(touch('touchstart', from));
      container.dispatchEvent(touch('touchmove', to));
      if (release) container.dispatchEvent(touch('touchend', to));
    });
  };
  return { container, indicator, drag };
}

describe('pull to refresh', () => {
  it('refreshes when pulled past the threshold and let go', async () => {
    let release: () => void = () => {};
    const onRefresh = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const { indicator, drag } = mount(onRefresh);

    // 64px of indicator is ~107px of finger travel at 0.6 damping.
    drag(100, 100 + PULL_THRESHOLD_PX / PULL_DAMPING + 10);

    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(indicator().dataset['phase']).toBe('refreshing');
    expect(screen.getByText('Refreshing…')).toBeInTheDocument();
    expect(indicator().style.getPropertyValue('--pull-offset')).toBe(`${PULL_THRESHOLD_PX}px`);

    await act(async () => {
      release();
      await Promise.resolve();
    });
    expect(indicator().dataset['phase']).toBe('idle');
    expect(indicator().style.getPropertyValue('--pull-offset')).toBe('0px');
  });

  it('does nothing for a short pull', () => {
    const onRefresh = vi.fn(async () => {});
    const { indicator, drag } = mount(onRefresh);

    drag(100, 130, false);
    expect(indicator().dataset['phase']).toBe('pulling');
    expect(screen.getByText('Pull to refresh')).toBeInTheDocument();

    drag(100, 130);
    expect(onRefresh).not.toHaveBeenCalled();
    expect(indicator().dataset['phase']).toBe('idle');
  });

  it('says "Release to refresh" once far enough, before the finger lifts', () => {
    const { indicator, drag } = mount(async () => {});
    drag(0, 200, false);
    expect(indicator().dataset['phase']).toBe('armed');
    expect(screen.getByText('Release to refresh')).toBeInTheDocument();
  });

  it('ignores a touch that starts anywhere but the very top of the list', () => {
    const onRefresh = vi.fn(async () => {});
    const { container, indicator, drag } = mount(onRefresh);
    Object.defineProperty(container, 'scrollTop', { value: 40, configurable: true });

    drag(0, 300);

    expect(onRefresh).not.toHaveBeenCalled();
    expect(indicator().dataset['phase']).toBe('idle');
  });

  it('ignores a pull upwards, which is an ordinary scroll', () => {
    const onRefresh = vi.fn(async () => {});
    const { indicator, drag } = mount(onRefresh);
    drag(300, 0);
    expect(onRefresh).not.toHaveBeenCalled();
    expect(indicator().dataset['phase']).toBe('idle');
  });

  it("claims the gesture from the browser once it is a pull, and not before", () => {
    const { container } = mount(async () => {});
    act(() => {
      container.dispatchEvent(touch('touchstart', 100));
    });
    const upward = touch('touchmove', 60);
    const downward = touch('touchmove', 160);
    act(() => {
      container.dispatchEvent(upward);
    });
    expect(upward.defaultPrevented).toBe(false);
    act(() => {
      container.dispatchEvent(touch('touchstart', 100));
      container.dispatchEvent(downward);
    });
    expect(downward.defaultPrevented).toBe(true);
  });

  it('does not refresh twice while one refresh is still running', () => {
    let release: () => void = () => {};
    const onRefresh = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const { drag } = mount(onRefresh);
    drag(0, 300);
    drag(0, 300);
    expect(onRefresh).toHaveBeenCalledTimes(1);
    release();
  });

  it('has a label for every phase but idle', () => {
    expect(pullLabel('idle')).toBe('');
    expect(pullLabel('pulling')).toBe('Pull to refresh');
    expect(pullLabel('armed')).toBe('Release to refresh');
    expect(pullLabel('refreshing')).toBe('Refreshing…');
  });
});
