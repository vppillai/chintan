import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { config } from '@/config/env.ts';

import { Mark, Wordmark } from './Wordmark.tsx';

/**
 * The lockup is the mark and the name (R5-BR-L1). The mark is decoration to a
 * screen reader — the text carries the name — and ink to the theme.
 */
describe('Wordmark', () => {
  it('leads with the Bindu C, hidden from assistive tech, and reads as the name alone', () => {
    const { container } = render(<Wordmark />);
    const lockup = container.querySelector('.wordmark');
    expect(lockup).toHaveTextContent(new RegExp(`^${config.appName}$`));
    const mark = lockup?.firstElementChild;
    expect(mark?.tagName).toBe('svg');
    expect(mark).toHaveAttribute('aria-hidden', 'true');
    // Sized in ems so it follows the lockup's type size: the ring's outer edge
    // sits at the serif's cap height.
    expect(mark).toHaveAttribute('width', '1em');
    expect(mark).toHaveAttribute('height', '1em');
  });

  it('draws the ring and the bindu in currentColor, so the mark follows the theme', () => {
    const { container } = render(<Mark size={72} />);
    const svg = container.querySelector('svg');
    expect(svg).toHaveAttribute('viewBox', '0 0 24 24');
    expect(svg).toHaveAttribute('width', '72');
    expect(svg?.querySelector('path')).toHaveAttribute('stroke', 'currentColor');
    expect(svg?.querySelector('circle')).toHaveAttribute('fill', 'currentColor');
    // Nothing in the mark names a colour: the launcher icon alone wears the accent.
    expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3,8}/i);
  });
});
