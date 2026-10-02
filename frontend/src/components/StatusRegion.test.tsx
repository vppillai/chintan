import { act, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { StatusRegion, announce } from './StatusRegion.tsx';

describe('StatusRegion', () => {
  it('re-says a repeated message by changing the region text each time', () => {
    // "Already at the bottom" pressed twice was heard once: the region's
    // text did not change, so the reader had nothing new to say.
    render(<StatusRegion />);
    const region = screen.getByTestId('status-region');
    const texts: string[] = [];
    for (let i = 0; i < 3; i += 1) {
      act(() => {
        announce('Already at the bottom');
      });
      texts.push(region.textContent ?? '');
    }
    expect(texts[0]).not.toBe(texts[1]);
    expect(texts[1]).not.toBe(texts[2]);
    // What is read is the sentence, every time.
    for (const text of texts) expect(text.trim()).toBe('Already at the bottom');

    act(() => {
      announce('Marked done');
    });
    expect(region).toHaveTextContent('Marked done');
  });
});
