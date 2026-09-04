import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { ICON_STROKE_WIDTH, Icon, PATHS, type IconName } from './Icon.tsx';

/**
 * One set, one weight (backlog U9). The test pins the contract every glyph is
 * drawn to, so a new icon pasted in from elsewhere at a different weight or
 * off the 24-unit grid fails here rather than on someone's screen.
 */
describe('Icon', () => {
  const names = Object.keys(PATHS) as IconName[];

  it('draws every glyph with the same 1.75 px stroke, round caps and joins, at any size', () => {
    for (const name of names) {
      for (const size of [16, 22, 30]) {
        const { container, unmount } = render(<Icon name={name} size={size} />);
        const svg = container.querySelector('svg');
        expect(svg).toHaveAttribute('viewBox', '0 0 24 24');
        expect(svg).toHaveAttribute('stroke-width', String(ICON_STROKE_WIDTH));
        expect(svg).toHaveAttribute('stroke-linecap', 'round');
        expect(svg).toHaveAttribute('stroke-linejoin', 'round');
        expect(svg).toHaveAttribute('fill', 'none');
        expect(svg).toHaveAttribute('aria-hidden', 'true');
        expect(svg?.querySelector('path')).toHaveAttribute('vector-effect', 'non-scaling-stroke');
        unmount();
      }
    }
    expect(ICON_STROKE_WIDTH).toBe(1.75);
  });

  it('keeps every path on the 24-unit grid with room for the stroke', () => {
    for (const name of names) {
      const path = PATHS[name];
      // The first point is absolute; a glyph drawn for a 48- or 16-unit grid
      // starts somewhere a 24-unit one never would.
      const start = /^M(-?\d+(?:\.\d+)?)[ ,](-?\d+(?:\.\d+)?)/.exec(path);
      expect(start, `${name} does not start with an absolute move`).not.toBeNull();
      for (const coordinate of [Number(start?.[1]), Number(start?.[2])]) {
        expect(coordinate, `${name} starts off the grid`).toBeGreaterThanOrEqual(1);
        expect(coordinate, `${name} starts off the grid`).toBeLessThanOrEqual(23);
      }
      // No single move — absolute or relative — spans more than the grid.
      for (const value of path.match(/-?\d+(?:\.\d+)?/g)?.map(Number) ?? []) {
        expect(Math.abs(value), `${name}: ${String(value)} is off the grid`).toBeLessThanOrEqual(23);
      }
    }
  });
});
